// Package tokenseal provides AEAD-sealed storage for sensitive secrets
// that must live in Postgres at rest — currently OAuth access + refresh
// tokens for linked Google/Microsoft accounts.
//
// Algorithm: AES-256-GCM. Stdlib only, hardware-accelerated everywhere we
// deploy (AES-NI on x86_64, ARMv8 Crypto Extensions on arm64), and the GCM
// authentication tag rejects any tampering with the stored ciphertext.
//
// Key derivation: HKDF-SHA256 from the supplied master key with the info
// string "nightms/oauth-token-v1". This domain-separates the encryption key
// from any other use of the master key (e.g. the cookie-signing secret), so
// a hypothetical leak of one doesn't immediately yield the other. The
// master key itself comes from either NIGHTMS_OAUTH_TOKEN_SECRET (preferred
// for production; rotatable independent of the session cookie secret) or,
// when that env var is unset, the existing process-wide cookie secret.
//
// Sealed-blob layout, packed into a Postgres bytea column:
//
//	[ 1 byte version=0x01 ][ 12 byte nonce ][ ciphertext || GCM tag ]
//
// The version byte exists so a future algorithm swap (say, ChaCha20-Poly1305
// for environments without AES-NI) can decrypt v1 rows in place.
package tokenseal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	versionV1     byte = 0x01
	nonceSize          = 12 // AES-GCM standard nonce
	keySize            = 32 // AES-256
	hkdfInfoV1         = "nightms/oauth-token-v1"
	minSealedSize      = 1 + nonceSize + 16 // version + nonce + at least the GCM tag
)

// ErrInvalidCiphertext is returned by Open when the sealed blob is shorter
// than the framing demands, fails the AEAD tag check, or carries an
// unrecognized version byte. Callers should treat all three the same — the
// stored row is unusable and the user must re-link the account.
var ErrInvalidCiphertext = errors.New("tokenseal: ciphertext invalid or tampered")

// Sealer encrypts and decrypts opaque secret bytes with a single derived
// AES-256-GCM key. Construct once at process startup; safe for concurrent
// use because cipher.AEAD is goroutine-safe.
type Sealer struct {
	aead cipher.AEAD
}

// New derives the AES-256-GCM key from masterKey via HKDF-SHA256 and
// returns a ready-to-use Sealer. masterKey must be at least 32 bytes — the
// minimum sane entropy for the input keying material. Use this once at
// process startup; the *Sealer is goroutine-safe.
func New(masterKey []byte) (*Sealer, error) {
	if len(masterKey) < keySize {
		return nil, fmt.Errorf("tokenseal: master key must be ≥ %d bytes, got %d", keySize, len(masterKey))
	}
	derived := make([]byte, keySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, masterKey, nil, []byte(hkdfInfoV1)), derived); err != nil {
		return nil, fmt.Errorf("tokenseal: derive key: %w", err)
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("tokenseal: aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("tokenseal: gcm init: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns the encrypted form of plaintext. The output begins with a
// version byte and a random nonce so Open can find the right key + nonce
// without external metadata. Two calls with the same plaintext return
// different ciphertexts (the nonce is freshly random each time).
func (s *Sealer) Seal(plaintext []byte) []byte {
	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+s.aead.Overhead())
	out[0] = versionV1
	if _, err := rand.Read(out[1 : 1+nonceSize]); err != nil {
		// crypto/rand.Read drawing from /dev/urandom (or the equivalent
		// on Windows/macOS) does not realistically fail. If it does the
		// process is in deep trouble — panicking is the safest move.
		panic(fmt.Sprintf("tokenseal: rand failed: %v", err))
	}
	nonce := out[1 : 1+nonceSize]
	return s.aead.Seal(out, nonce, plaintext, nil)
}

// Open returns the plaintext that was Sealed earlier, or ErrInvalidCiphertext
// if the blob has been tampered with, truncated, or sealed under a
// different key/version. Callers must NOT distinguish these failure modes
// to clients — they all mean "the data is unusable".
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < minSealedSize {
		return nil, ErrInvalidCiphertext
	}
	if sealed[0] != versionV1 {
		return nil, ErrInvalidCiphertext
	}
	nonce := sealed[1 : 1+nonceSize]
	ciphertext := sealed[1+nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}
