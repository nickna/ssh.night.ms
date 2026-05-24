package auth

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHasher_PHCRoundTrip(t *testing.T) {
	h := NewHasher(DefaultArgon2Params())
	hashBytes, algo, err := h.Hash("hunter2-correct-horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if algo != "" {
		t.Errorf("expected empty algo for PHC, got %q", algo)
	}
	if !strings.HasPrefix(string(hashBytes), "$argon2id$v=19$") {
		t.Fatalf("expected PHC prefix, got %q", string(hashBytes)[:32])
	}
	if got := h.Verify("hunter2-correct-horse", hashBytes, algo); !got.OK || got.NeedsRehash {
		t.Errorf("Verify round-trip: ok=%v rehash=%v (want true,false)", got.OK, got.NeedsRehash)
	}
}

func TestHasher_PHCWrongPassword(t *testing.T) {
	h := NewHasher(DefaultArgon2Params())
	hashBytes, algo, _ := h.Hash("right")
	if got := h.Verify("wrong", hashBytes, algo); got.OK {
		t.Errorf("expected verify(wrong) to fail")
	}
}

// Synthesize a row in the .NET legacy shape (salt||hash bytes + algo descriptor
// string). Verifies the legacy path accepts it and signals NeedsRehash so the
// caller knows to lazy-migrate to PHC on the next successful login.
func TestHasher_LegacyRoundTrip(t *testing.T) {
	const password = "phase2-test-passphrase-2026"
	salt := []byte("0123456789ABCDEF") // 16 bytes
	const memKB, iters, parallel, hashLen = 65536, 3, 1, 32
	digest := argon2.IDKey([]byte(password), salt, iters, memKB, parallel, hashLen)
	stored := append([]byte{}, salt...)
	stored = append(stored, digest...)
	algo := "argon2id:m=65536,t=3,p=1,s=16,h=32"

	h := NewHasher(DefaultArgon2Params())
	got := h.Verify(password, stored, algo)
	if !got.OK {
		t.Fatalf("legacy verify failed: %+v", got)
	}
	if !got.NeedsRehash {
		t.Errorf("legacy verify should always signal NeedsRehash (so caller migrates to PHC)")
	}

	if h.Verify("not-the-password", stored, algo).OK {
		t.Errorf("legacy verify accepted wrong password")
	}
}

// Verify that a PHC row produced under stronger params triggers NeedsRehash when
// the hasher's configured defaults are weaker (or vice versa). Skipped on the
// no-drift configuration; here we explicitly mismatch.
func TestHasher_PHCDriftRehash(t *testing.T) {
	original := NewHasher(Argon2Params{
		MemoryKB: 16384, Iterations: 2, Parallelism: 1, SaltBytes: 16, HashBytes: 32,
	})
	hashBytes, _, _ := original.Hash("drifted")

	current := NewHasher(DefaultArgon2Params()) // stronger params
	got := current.Verify("drifted", hashBytes, "")
	if !got.OK {
		t.Fatalf("verify failed: %+v", got)
	}
	if !got.NeedsRehash {
		t.Errorf("expected NeedsRehash when params drifted, got false")
	}
}

func TestHasher_VerifyDummy(t *testing.T) {
	// VerifyDummy should not panic and should not actually verify anything.
	// It exists for timing-equivalence, so we just smoke-test it.
	h := NewHasher(Argon2Params{
		MemoryKB: 8192, Iterations: 1, Parallelism: 1, SaltBytes: 16, HashBytes: 32,
	})
	h.VerifyDummy("any-password") // first call: lazy init dummy
	h.VerifyDummy("")             // second call: empty password must not panic
}

func TestHasher_UnknownFormatFailsClean(t *testing.T) {
	h := NewHasher(DefaultArgon2Params())
	got := h.Verify("password", []byte("not-a-hash"), "unknown:format")
	if got.OK {
		t.Errorf("expected verify to fail for unknown format")
	}
}

func TestParseLegacyAlgo_HappyPath(t *testing.T) {
	p, err := parseLegacyAlgo("argon2id:m=65536,t=3,p=1,s=16,h=32")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := Argon2Params{MemoryKB: 65536, Iterations: 3, Parallelism: 1, SaltBytes: 16, HashBytes: 32}
	if p != want {
		t.Errorf("got %+v want %+v", p, want)
	}
}

func TestParseLegacyAlgo_BadInputs(t *testing.T) {
	bad := []string{
		"",
		"argon2id",
		"argon2id:",
		"argon2id:m=abc,t=3,p=1,s=16,h=32",
		"argon2id:m=65536,t=3,p=1,s=16,h=32,extra=1",
		"argon2id:m=0,t=3,p=1,s=16,h=32",
		"bcrypt:cost=10",
	}
	for _, s := range bad {
		if _, err := parseLegacyAlgo(s); err == nil {
			t.Errorf("parseLegacyAlgo(%q) should have errored", s)
		}
	}
}

// Sanity-check that argon2.Version (19) is the format version we emit, so a
// future bump in x/crypto/argon2 produces a clear test failure rather than a
// silent format mismatch with the .NET stack.
func TestArgon2VersionIsAsExpected(t *testing.T) {
	if argon2.Version != 19 {
		t.Errorf("argon2.Version changed: got %d want 19. Update Hash format and verify cross-stack interop.", argon2.Version)
	}
}

// Hashes produced for the same password must differ (salt randomization).
func TestHash_SaltsAreRandomized(t *testing.T) {
	h := NewHasher(DefaultArgon2Params())
	a, _, _ := h.Hash("same-password")
	b, _, _ := h.Hash("same-password")
	if bytes.Equal(a, b) {
		t.Errorf("two hashes of same password should differ (salt randomization)")
	}
}
