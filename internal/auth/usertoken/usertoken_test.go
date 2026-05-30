package usertoken

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

type fakeQueries struct {
	creds    []gen.IdentityCredential
	tokenRow gen.GetOAuthTokenByCredentialIDRow
	marked   bool
}

func (f *fakeQueries) ListCredentialsForUser(_ context.Context, _ int64) ([]gen.IdentityCredential, error) {
	return f.creds, nil
}
func (f *fakeQueries) GetOAuthTokenByCredentialID(_ context.Context, _ int64) (gen.GetOAuthTokenByCredentialIDRow, error) {
	return f.tokenRow, nil
}
func (f *fakeQueries) UpsertOAuthToken(_ context.Context, _ gen.UpsertOAuthTokenParams) error {
	return nil
}
func (f *fakeQueries) MarkTokenRefreshFailed(_ context.Context, _ gen.MarkTokenRefreshFailedParams) error {
	f.marked = true
	return nil
}

func newSealer(t *testing.T) *tokenseal.Sealer {
	t.Helper()
	s, err := tokenseal.New(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return s
}

func msSource(q Queries, sealer *tokenseal.Sealer) *Source {
	return &Source{
		Queries:  q,
		Sealer:   sealer,
		Provider: auth.NewMicrosoftProvider("", "", ""), // Kind=Microsoft; RefreshToken unused in these paths
	}
}

func TestToken_FreshPassthrough(t *testing.T) {
	sealer := newSealer(t)
	q := &fakeQueries{
		creds: []gen.IdentityCredential{{ID: 5, UserID: 1, Provider: "Microsoft"}},
		tokenRow: gen.GetOAuthTokenByCredentialIDRow{
			CredentialID:         5,
			EncryptedAccessToken: sealer.Seal([]byte("access-123")),
			AccessExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Scopes:               []string{"Notes.ReadWrite", "User.Read"},
		},
	}
	got, err := msSource(q, sealer).Token(context.Background(), 1, "Notes.ReadWrite")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "access-123" {
		t.Fatalf("token = %q, want access-123", got)
	}
}

func TestToken_NoLink(t *testing.T) {
	sealer := newSealer(t)
	q := &fakeQueries{creds: []gen.IdentityCredential{{ID: 9, UserID: 1, Provider: "Google"}}}
	_, err := msSource(q, sealer).Token(context.Background(), 1, "Notes.ReadWrite")
	if !errors.Is(err, ErrNoLink) {
		t.Fatalf("err = %v, want ErrNoLink", err)
	}
}

func TestToken_NeedsReauth(t *testing.T) {
	sealer := newSealer(t)
	q := &fakeQueries{
		creds:    []gen.IdentityCredential{{ID: 5, UserID: 1, Provider: "Microsoft"}},
		tokenRow: gen.GetOAuthTokenByCredentialIDRow{CredentialID: 5, NeedsReauth: true},
	}
	_, err := msSource(q, sealer).Token(context.Background(), 1, "Notes.ReadWrite")
	if !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("err = %v, want ErrNeedsReauth", err)
	}
}

func TestToken_MissingScope(t *testing.T) {
	sealer := newSealer(t)
	q := &fakeQueries{
		creds: []gen.IdentityCredential{{ID: 5, UserID: 1, Provider: "Microsoft"}},
		tokenRow: gen.GetOAuthTokenByCredentialIDRow{
			CredentialID:         5,
			EncryptedAccessToken: sealer.Seal([]byte("access-123")),
			AccessExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Scopes:               []string{"User.Read"}, // no Notes.ReadWrite
		},
	}
	_, err := msSource(q, sealer).Token(context.Background(), 1, "Notes.ReadWrite")
	if !errors.Is(err, ErrMissingScope) {
		t.Fatalf("err = %v, want ErrMissingScope", err)
	}
}

func TestHasScopes_CaseInsensitive(t *testing.T) {
	if !hasScopes([]string{"notes.readwrite"}, []string{"Notes.ReadWrite"}) {
		t.Fatal("expected case-insensitive scope match")
	}
	if hasScopes([]string{"User.Read"}, []string{"Notes.ReadWrite"}) {
		t.Fatal("expected missing scope to fail")
	}
	if !hasScopes(nil, nil) {
		t.Fatal("empty required should pass")
	}
}
