package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// OAuthProviderKind discriminates supported providers. The string value
// also matches the identity_credentials.provider column convention from the
// .NET stack ("Google", "Microsoft") — so a row inserted by either stack
// works seamlessly with the other.
type OAuthProviderKind string

const (
	OAuthGoogle    OAuthProviderKind = "Google"
	OAuthMicrosoft OAuthProviderKind = "Microsoft"
)

// OAuthProvider bundles the oauth2.Config + the provider-specific userinfo
// fetch. New providers add a constructor + a userinfoer; the rest of the
// flow is provider-agnostic.
type OAuthProvider struct {
	Kind   OAuthProviderKind
	Config *oauth2.Config

	// fetchUserInfo retrieves the canonical (subject, email, name) for the
	// authenticated user. Implementations decide which API to call.
	fetchUserInfo func(ctx context.Context, token *oauth2.Token) (OAuthUser, error)
}

// OAuthUser is what we keep from a successful userinfo fetch. Email + Name
// drive the "claim handle" suggestion on first signup; Subject is stored
// permanently in identity_credentials as the linking key.
type OAuthUser struct {
	Subject string
	Email   string
	Name    string
}

// AuthCodeURL builds the redirect URL the user's browser hits to start the
// OAuth dance. The state token must be opaque to the IdP — we generate it
// in the web handler and stash it in a short-lived cookie.
func (p *OAuthProvider) AuthCodeURL(state string) string {
	return p.Config.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange swaps the authorization code for a token and immediately fetches
// userinfo. Returns (token, user) so callers can re-use the token if they
// want (we currently don't — single use).
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

// NewGoogleProvider configures Google login against the OpenID Connect
// userinfo endpoint. Scope set to openid+email+profile so we get a stable
// `sub`, the email, and the display name on first use.
func NewGoogleProvider(clientID, clientSecret, redirectURL string) *OAuthProvider {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     endpoints.Google,
	}
	return &OAuthProvider{Kind: OAuthGoogle, Config: cfg, fetchUserInfo: googleUserInfo}
}

// NewMicrosoftProvider configures Microsoft personal/work/school accounts
// via the "common" multi-tenant endpoint. Scope set to openid+email+profile
// so the /me Graph endpoint returns a stable `id` (the directory OID) plus
// email + name.
func NewMicrosoftProvider(clientID, clientSecret, redirectURL string) *OAuthProvider {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email", "profile", "User.Read"},
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
