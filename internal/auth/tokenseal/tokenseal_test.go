package tokenseal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func mustNew(t *testing.T, key []byte) *Sealer {
	t.Helper()
	s, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := mustNew(t, randKey(t))
	plaintext := []byte("ya29.A0AfH6SMBxv...refresh-token-shaped-string")
	sealed := s.Seal(plaintext)
	got, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch: %q vs %q", got, plaintext)
	}
}

func TestSealProducesDistinctCiphertexts(t *testing.T) {
	s := mustNew(t, randKey(t))
	a := s.Seal([]byte("identical-input"))
	b := s.Seal([]byte("identical-input"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of same plaintext produced identical ciphertexts — nonce randomness broken")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	s := mustNew(t, randKey(t))
	sealed := s.Seal([]byte("hello"))
	// Flip a bit in the ciphertext body (past version + nonce).
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := s.Open(tampered); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsTamperedNonce(t *testing.T) {
	s := mustNew(t, randKey(t))
	sealed := s.Seal([]byte("hello"))
	tampered := append([]byte(nil), sealed...)
	tampered[1] ^= 0x01 // flip a bit in the nonce
	if _, err := s.Open(tampered); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsWrongVersionByte(t *testing.T) {
	s := mustNew(t, randKey(t))
	sealed := s.Seal([]byte("hello"))
	tampered := append([]byte(nil), sealed...)
	tampered[0] = 0xFF
	if _, err := s.Open(tampered); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsTooShort(t *testing.T) {
	s := mustNew(t, randKey(t))
	if _, err := s.Open([]byte{0x01, 0x02}); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	keyA := randKey(t)
	keyB := randKey(t)
	sealed := mustNew(t, keyA).Seal([]byte("secret"))
	if _, err := mustNew(t, keyB).Open(sealed); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext when opening with wrong key, got %v", err)
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("expected New to reject a 16-byte key")
	}
}
