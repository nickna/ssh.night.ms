// Package auth holds password hashing, public-key lookup, rate limiting, and the
// sysop bootstrap.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2Params holds the parameters fed to Argon2id. The defaults match the
// legacy stack's PasswordHashingOptions defaults so a row produced by either
// stack can be verified by the other during the cutover window.
type Argon2Params struct {
	MemoryKB    uint32 // m=
	Iterations  uint32 // t=
	Parallelism uint8  // p=
	SaltBytes   uint32 // s=
	HashBytes   uint32 // h=
}

func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKB:    65536,
		Iterations:  3,
		Parallelism: 1,
		SaltBytes:   16,
		HashBytes:   32,
	}
}

// Hasher wraps the Argon2id primitive with both legacy and PHC verify paths,
// plus a constant-wallclock VerifyDummy for timing-equivalence on unknown users.
type Hasher struct {
	params Argon2Params

	dummyOnce sync.Once
	dummyHash []byte
}

func NewHasher(p Argon2Params) *Hasher { return &Hasher{params: p} }

// Hash produces a PHC-encoded $argon2id$ string. We always write PHC for new rows;
// the legacy "salt||hash + algo-string" format is read-only, used only to verify
// rows produced by an earlier deploy until lazy migration replaces them.
//
// Return shape:
//   - hashBytes: the ASCII bytes of the PHC string. Written into users.password_hash (bytea).
//   - algo:      empty string. The legacy users.password_algo column is unused for PHC rows.
//
// Callers should write algo as NULL (or "") into the column.
func (h *Hasher) Hash(password string) (hashBytes []byte, algo string, err error) {
	salt := make([]byte, h.params.SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, "", fmt.Errorf("argon2id: read random salt: %w", err)
	}
	digest := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.MemoryKB,
		h.params.Parallelism,
		h.params.HashBytes,
	)
	phc := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.MemoryKB,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
	return []byte(phc), "", nil
}

// VerifyResult tells the caller what happened. NeedsRehash signals the caller
// should re-hash with current params and persist (legacy-format wins always rehash).
type VerifyResult struct {
	OK          bool
	NeedsRehash bool
}

// Verify accepts both legacy (algo-string + salt||hash bytes) and PHC (bytes
// are the PHC string, algo unused).
//
// Detection:
//   - If algo starts with "argon2id:" → legacy.
//   - Else if hash starts with "$argon2id$" → PHC.
//   - Otherwise → format error, fails verify.
//
// Always burns one Argon2id evaluation worth of wall time on the constant-time
// comparison path so legitimate verify failures and format errors look the same
// to a timing attacker.
func (h *Hasher) Verify(password string, storedHash []byte, algo string) VerifyResult {
	if strings.HasPrefix(algo, "argon2id:") {
		ok := h.verifyLegacy(password, storedHash, algo)
		// Legacy hits always need re-hash so the row migrates to PHC on next login.
		return VerifyResult{OK: ok, NeedsRehash: ok}
	}
	if hasPrefix(storedHash, "$argon2id$") {
		ok, drift := h.verifyPHC(password, storedHash)
		return VerifyResult{OK: ok, NeedsRehash: ok && drift}
	}
	// Unknown format. Burn equivalent time to keep timing analysis blind.
	h.VerifyDummy(password)
	return VerifyResult{}
}

// VerifyDummy runs one Argon2id evaluation against a precomputed throwaway hash
// so the verify path takes the same wall time as a real verify even for unknown
// handles or rows with no password set. Always returns false (the caller doesn't
// look at the result — the wall-clock work is the point).
func (h *Hasher) VerifyDummy(password string) {
	h.dummyOnce.Do(func() {
		// Deterministic seed for the same params so the dummy's CPU work matches
		// real verifies on the same configuration.
		seed, _, err := h.Hash("nightms-dummy-hash-seed-1234567890")
		if err != nil {
			// Hash failure here is a startup/crypto bug; surface via a nil dummy
			// (Verify will run a fresh argon2 anyway against the candidate).
			return
		}
		h.dummyHash = seed
	})
	if h.dummyHash == nil {
		// Fallback: just do an argon2 evaluation directly so we still burn time.
		_ = argon2.IDKey([]byte(password), make([]byte, h.params.SaltBytes),
			h.params.Iterations, h.params.MemoryKB, h.params.Parallelism, h.params.HashBytes)
		return
	}
	_ = h.Verify(password, h.dummyHash, "")
}

// --- legacy ---

func (h *Hasher) verifyLegacy(password string, storedHash []byte, algo string) bool {
	p, err := parseLegacyAlgo(algo)
	if err != nil {
		// Algo unparseable: still spend the time so we can't be probed for format errors.
		_ = argon2.IDKey([]byte(password), make([]byte, h.params.SaltBytes),
			h.params.Iterations, h.params.MemoryKB, h.params.Parallelism, h.params.HashBytes)
		return false
	}
	if uint32(len(storedHash)) != p.SaltBytes+p.HashBytes {
		return false
	}
	salt := storedHash[:p.SaltBytes]
	expected := storedHash[p.SaltBytes:]
	actual := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKB, p.Parallelism, p.HashBytes)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func parseLegacyAlgo(algo string) (Argon2Params, error) {
	// e.g. "argon2id:m=65536,t=3,p=1,s=16,h=32"
	const prefix = "argon2id:"
	if !strings.HasPrefix(algo, prefix) {
		return Argon2Params{}, errors.New("argon2id: not a legacy algo string")
	}
	body := algo[len(prefix):]
	var p Argon2Params
	for _, part := range strings.Split(body, ",") {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return Argon2Params{}, fmt.Errorf("argon2id: legacy algo malformed at %q", part)
		}
		k, v := part[:eq], part[eq+1:]
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Argon2Params{}, fmt.Errorf("argon2id: legacy algo non-numeric %q=%q", k, v)
		}
		switch k {
		case "m":
			p.MemoryKB = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			p.Parallelism = uint8(n)
		case "s":
			p.SaltBytes = uint32(n)
		case "h":
			p.HashBytes = uint32(n)
		default:
			return Argon2Params{}, fmt.Errorf("argon2id: legacy algo unknown key %q", k)
		}
	}
	if p.MemoryKB == 0 || p.Iterations == 0 || p.Parallelism == 0 || p.SaltBytes == 0 || p.HashBytes == 0 {
		return Argon2Params{}, errors.New("argon2id: legacy algo missing a field")
	}
	return p, nil
}

// --- PHC ---

func (h *Hasher) verifyPHC(password string, storedHash []byte) (ok, drift bool) {
	// $argon2id$v=19$m=65536,t=3,p=1$saltB64$hashB64
	parts := strings.Split(string(storedHash), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, false
	}
	var memKB, iters uint32
	var par uint8
	for _, kv := range strings.Split(parts[3], ",") {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return false, false
		}
		k, v := kv[:eq], kv[eq+1:]
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return false, false
		}
		switch k {
		case "m":
			memKB = uint32(n)
		case "t":
			iters = uint32(n)
		case "p":
			par = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false
	}
	actual := argon2.IDKey([]byte(password), salt, iters, memKB, par, uint32(len(expected)))
	if subtle.ConstantTimeCompare(expected, actual) != 1 {
		return false, false
	}
	drift = memKB != h.params.MemoryKB ||
		iters != h.params.Iterations ||
		par != h.params.Parallelism ||
		uint32(len(salt)) != h.params.SaltBytes ||
		uint32(len(expected)) != h.params.HashBytes
	return true, drift
}

// hasPrefix is a small helper that avoids allocating string(storedHash) when
// the byte slice clearly doesn't start with the PHC sentinel.
func hasPrefix(b []byte, prefix string) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}
