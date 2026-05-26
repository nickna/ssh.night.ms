package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	gossh "golang.org/x/crypto/ssh"
)

// ErrUnparseableSSHKey is returned by ParseAuthorizedKey when the input
// can't be read as an OpenSSH public-key line. Callers surface a generic
// "not a recognizable OpenSSH public key" message; the underlying parse
// error is wrapped so debug logs can still see it.
var ErrUnparseableSSHKey = errors.New("auth: unparseable openssh public key")

// ParseAuthorizedKey normalizes a pasted authorized_keys line, parses it,
// and returns the SHA256 fingerprint, algorithm string (e.g. "ssh-ed25519"),
// and the JSON metadata payload to store in identity_credentials.metadata.
//
// Whitespace handling: CRs are stripped and newlines/tabs collapsed to spaces
// before parsing, so a user who pastes a key that the terminal hard-wrapped
// across two lines still gets a valid parse.
func ParseAuthorizedKey(raw string) (fingerprint, algorithm string, metadata []byte, err error) {
	cleaned := normalizeAuthorizedKey(raw)
	if cleaned == "" {
		return "", "", nil, ErrUnparseableSSHKey
	}
	pub, _, _, _, parseErr := gossh.ParseAuthorizedKey([]byte(cleaned))
	if parseErr != nil {
		return "", "", nil, errors.Join(ErrUnparseableSSHKey, parseErr)
	}
	fingerprint = gossh.FingerprintSHA256(pub)
	algorithm = pub.Type()
	metadata, err = json.Marshal(map[string]any{
		"algorithm": algorithm,
		"blob_b64":  base64.StdEncoding.EncodeToString(pub.Marshal()),
	})
	if err != nil {
		return "", "", nil, err
	}
	return fingerprint, algorithm, metadata, nil
}

func normalizeAuthorizedKey(raw string) string {
	s := strings.ReplaceAll(raw, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

// IsDuplicateCredential reports whether err is the Postgres unique-violation
// that fires when a key fingerprint already exists in identity_credentials
// (the index is global across users).
func IsDuplicateCredential(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
