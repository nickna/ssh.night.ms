package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// OAuthProviderKind discriminates supported providers. The string value
// matches the identity_credentials.provider column convention ("Google",
// "Microsoft") so the SSH and web stacks both read/write rows with the
// same shape.
type OAuthProviderKind string

const (
	OAuthGoogle    OAuthProviderKind = "Google"
	OAuthMicrosoft OAuthProviderKind = "Microsoft"
)

// GoogleLinkScopes is the scope set requested when linking a Google
// account. openid+email+profile cover the OIDC userinfo we need to display
// the link; gmail.readonly + drive.readonly + documents.readonly are the
// data scopes the planned Gmail / Drive / Docs integrations need. Drop
// scopes here only after auditing every consumer — narrower scopes
// silently break those features.
var GoogleLinkScopes = []string{
	"openid", "email", "profile",
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/drive.readonly",
	"https://www.googleapis.com/auth/documents.readonly",
}

// MicrosoftLinkScopes is the scope set for Microsoft account linking.
// offline_access is what gets us a refresh token; without it the access
// token expires after an hour and we can't refresh it from the background
// service. Mail.Read + Files.Read cover the planned Outlook + OneDrive
// integrations; Notes.ReadWrite backs the OneNote read/edit feature
// (internal/onenote).
//
// Scopes are fixed at link time and the refresher preserves them — adding a
// scope here only takes effect for accounts linked (or re-authorized) after
// the change ships. Existing Microsoft links keep their old scope set until
// the user re-runs /auth/microsoft/start (which forces prompt=consent). The
// usertoken.Source surfaces the gap as ErrMissingScope so callers render a
// "re-authorize to enable OneNote" CTA instead of a 500.
var MicrosoftLinkScopes = []string{
	"openid", "email", "profile", "offline_access",
	"User.Read", "Mail.Read", "Files.Read", "Notes.ReadWrite",
}

// Device-code flow endpoints. Authorization-code endpoints come from
// oauth2/endpoints; the device-code endpoints aren't in stdlib so they're
// inlined here. Google's token endpoint for device flow is the same URL as
// the standard token endpoint; Microsoft uses the same /token endpoint for
// both flows as well.
const (
	GoogleDeviceCodeURL     = "https://oauth2.googleapis.com/device/code"
	GoogleDeviceTokenURL    = "https://oauth2.googleapis.com/token"
	MicrosoftDeviceCodeURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/devicecode"
	MicrosoftDeviceTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
)

// OAuthProvider bundles the oauth2.Config + provider-specific userinfo
// fetch. New providers add a constructor + a userinfoer; the rest of the
// flow is provider-agnostic.
type OAuthProvider struct {
	Kind   OAuthProviderKind
	Config *oauth2.Config

	// fetchUserInfo retrieves the canonical (subject, email, name) for the
	// authenticated user. Implementations decide which API to call.
	fetchUserInfo func(ctx context.Context, token *oauth2.Token) (OAuthUser, error)
}

// OAuthUser is what we keep from a successful userinfo fetch. Subject is
// the provider's stable per-account identifier (Google's `sub` claim,
// Microsoft's directory OID) and is the unique linking key in
// identity_credentials.
type OAuthUser struct {
	Subject string
	Email   string
	Name    string
}

// AuthCodeURL builds the browser-redirect URL that starts the auth-code
// flow. We always ask for offline access + force the consent screen so
// Google reliably returns a refresh token on second and later
// authorizations. Without prompt=consent, Google silently omits the
// refresh_token from the response, which breaks the background refresher.
// Microsoft is unaffected by the prompt param (it issues refresh tokens
// based on the offline_access scope being present).
func (p *OAuthProvider) AuthCodeURL(state string) string {
	return p.Config.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
}

// Exchange swaps the authorization code for a token and immediately fetches
// userinfo. Callers persist the returned *oauth2.Token (access + refresh +
// expiry) into oauth_tokens so the refresher and downstream API clients can
// reuse it.
func (p *OAuthProvider) Exchange(ctx context.Context, code string) (*oauth2.Token, OAuthUser, error) {
	tok, err := p.Config.Exchange(ctx, code)
	if err != nil {
		return nil, OAuthUser{}, fmt.Errorf("oauth: token exchange: %w", err)
	}
	user, err := p.fetchUserInfo(ctx, tok)
	if err != nil {
		return nil, OAuthUser{}, fmt.Errorf("oauth: userinfo: %w", err)
	}
	return tok, user, nil
}

// FetchUserInfo runs the provider-specific userinfo call. Exposed so the
// device-code service (which gets the token directly from a polling
// exchange, not from a redirect callback) can reuse the same fetcher.
func (p *OAuthProvider) FetchUserInfo(ctx context.Context, tok *oauth2.Token) (OAuthUser, error) {
	return p.fetchUserInfo(ctx, tok)
}

// RefreshToken exchanges a refresh_token for a fresh access_token. Google
// usually does not rotate the refresh token; Microsoft always does. The
// returned *oauth2.Token has the (possibly rotated) refresh_token in
// RefreshToken — callers MUST persist that field even when it differs from
// the one they passed in, otherwise the next refresh will fail with
// invalid_grant.
func (p *OAuthProvider) RefreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	src := p.Config.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return src.Token()
}

// RevokeToken best-effort revokes the access token (or refresh token —
// either accepted by Google's revoke endpoint, which invalidates the
// entire grant) at the provider. Google has a dedicated /revoke endpoint;
// Microsoft v2 has no app-scoped revoke (the only Graph endpoint
// /me/revokeSignInSessions wipes ALL apps for the user, which would be
// wrong here), so the Microsoft path is a no-op. Callers should treat
// errors as soft: delete the local row regardless of provider response.
func (p *OAuthProvider) RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	switch p.Kind {
	case OAuthGoogle:
		return revokeGoogle(ctx, token)
	case OAuthMicrosoft:
		// TODO: Microsoft does not offer an app-scoped revoke endpoint at
		// the v2 OAuth surface. Re-evaluate when they add one. For now we
		// rely on the access token's 1-hour TTL.
		return nil
	}
	return fmt.Errorf("oauth: revoke: unknown provider kind %q", p.Kind)
}

func revokeGoogle(ctx context.Context, token string) error {
	body := strings.NewReader("token=" + url.QueryEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/revoke", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("google revoke status %d: %s", resp.StatusCode, string(msg))
}

// NewGoogleProvider configures Google linking against the OpenID Connect
// userinfo endpoint plus the readonly Gmail/Drive/Docs scopes. The same
// constructor handles both browser auth-code and device-code flows —
// device-code reuses the Config's client_id/secret + scopes; the device
// endpoints are wired in the devicecode package.
func NewGoogleProvider(clientID, clientSecret, redirectURL string) *OAuthProvider {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       append([]string{}, GoogleLinkScopes...),
		Endpoint:     endpoints.Google,
	}
	return &OAuthProvider{Kind: OAuthGoogle, Config: cfg, fetchUserInfo: googleUserInfo}
}

// NewMicrosoftProvider configures Microsoft account linking against the
// "common" multi-tenant endpoint. Includes offline_access so the token
// response carries a refresh_token, plus Mail.Read + Files.Read for the
// planned Outlook + OneDrive integrations.
func NewMicrosoftProvider(clientID, clientSecret, redirectURL string) *OAuthProvider {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       append([]string{}, MicrosoftLinkScopes...),
		Endpoint:     endpoints.AzureAD("common"),
	}
	return &OAuthProvider{Kind: OAuthMicrosoft, Config: cfg, fetchUserInfo: microsoftUserInfo}
}

// googleUserInfo calls Google's OIDC userinfo endpoint. The `sub` claim is
// the stable provider-side identifier and never changes for a given Google
// account — that's what we link by.
func googleUserInfo(ctx context.Context, tok *oauth2.Token) (OAuthUser, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return OAuthUser{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return OAuthUser{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return OAuthUser{}, fmt.Errorf("google userinfo status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return OAuthUser{}, err
	}
	if payload.Sub == "" {
		return OAuthUser{}, fmt.Errorf("google userinfo: empty sub")
	}
	return OAuthUser{Subject: payload.Sub, Email: payload.Email, Name: payload.Name}, nil
}

// microsoftUserInfo calls Microsoft Graph's /me endpoint. The `id` field is
// the directory OID — stable per account regardless of UPN changes. We fall
// back to mail / userPrincipalName for the displayable email.
func microsoftUserInfo(ctx context.Context, tok *oauth2.Token) (OAuthUser, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://graph.microsoft.com/v1.0/me?$select=id,mail,userPrincipalName,displayName", nil)
	if err != nil {
		return OAuthUser{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return OAuthUser{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return OAuthUser{}, fmt.Errorf("ms userinfo status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		ID                string `json:"id"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
		DisplayName       string `json:"displayName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return OAuthUser{}, err
	}
	if payload.ID == "" {
		return OAuthUser{}, fmt.Errorf("ms userinfo: empty id")
	}
	email := payload.Mail
	if email == "" {
		email = payload.UserPrincipalName
	}
	return OAuthUser{Subject: payload.ID, Email: email, Name: payload.DisplayName}, nil
}

// LinkLookup classifies the result of "does this (provider, subject) row
// already exist, and if so does it belong to the user trying to link?"
// Both the auth-code callback and the device-code service call
// ResolveExistingLink and switch on this enum so the two paths can't drift.
type LinkLookup int

const (
	// LinkLookupNotFound: no row exists. Caller proceeds with fresh link.
	LinkLookupNotFound LinkLookup = iota
	// LinkLookupSameUser: a row exists and belongs to the calling user.
	// Caller treats this as re-auth (upsert tokens, no new credential row).
	LinkLookupSameUser
	// LinkLookupOtherUser: a row exists but belongs to a different SSH
	// account. Caller refuses the link and surfaces a clear error.
	LinkLookupOtherUser
)

// ResolveExistingLink looks up (provider, subject) and classifies who owns
// it. The IdentityCredential is non-nil on SameUser / OtherUser and
// carries everything the caller needs for the upsert / error message.
func ResolveExistingLink(
	ctx context.Context,
	queries *gen.Queries,
	userID int64,
	provider OAuthProviderKind,
	subject string,
) (LinkLookup, *gen.IdentityCredential, error) {
	cred, err := queries.GetCredentialByProviderSubject(ctx, gen.GetCredentialByProviderSubjectParams{
		Provider: string(provider),
		Subject:  subject,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LinkLookupNotFound, nil, nil
		}
		return 0, nil, err
	}
	if cred.UserID == userID {
		return LinkLookupSameUser, &cred, nil
	}
	return LinkLookupOtherUser, &cred, nil
}
