package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// generateEd25519AuthorizedLine builds a real ed25519 public key and
// returns its authorized_keys line. Test fixture only.
func generateEd25519AuthorizedLine(t *testing.T, comment string) (line, wantFingerprint, wantAlgorithm string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("gossh.NewPublicKey: %v", err)
	}
	authBytes := gossh.MarshalAuthorizedKey(sshPub)
	line = strings.TrimRight(string(authBytes), "\n")
	if comment != "" {
		line = line + " " + comment
	}
	return line, gossh.FingerprintSHA256(sshPub), sshPub.Type()
}

func TestParseAuthorizedKey_ValidEd25519(t *testing.T) {
	line, wantFp, wantAlgo := generateEd25519AuthorizedLine(t, "user@host")

	fp, algo, meta, err := ParseAuthorizedKey(line)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fp != wantFp {
		t.Errorf("fingerprint = %q, want %q", fp, wantFp)
	}
	if algo != wantAlgo {
		t.Errorf("algorithm = %q, want %q", algo, wantAlgo)
	}
	var got struct {
		Algorithm string `json:"algorithm"`
		BlobB64   string `json:"blob_b64"`
	}
	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatalf("metadata not valid JSON: %v", err)
	}
	if got.Algorithm != wantAlgo {
		t.Errorf("metadata.algorithm = %q, want %q", got.Algorithm, wantAlgo)
	}
	if got.BlobB64 == "" {
		t.Errorf("metadata.blob_b64 is empty")
	}
}

func TestParseAuthorizedKey_WhitespaceTolerant(t *testing.T) {
	line, wantFp, _ := generateEd25519AuthorizedLine(t, "user@host")

	cases := map[string]string{
		"leading whitespace":  "   " + line,
		"trailing whitespace": line + "  \n",
		"CRLF terminator":     line + "\r\n",
		"surrounding tabs":    "\t" + line + "\t",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			fp, _, _, err := ParseAuthorizedKey(input)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if fp != wantFp {
				t.Errorf("fingerprint = %q, want %q", fp, wantFp)
			}
		})
	}
}

func TestParseAuthorizedKey_Garbage(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"not a key",
		"ssh-ed25519 not-base64 user@host",
		"\n\n\n",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, _, _, err := ParseAuthorizedKey(input)
			if err == nil {
				t.Fatalf("expected error for %q", input)
			}
			if !errors.Is(err, ErrUnparseableSSHKey) {
				t.Errorf("err = %v, want ErrUnparseableSSHKey", err)
			}
		})
	}
}
