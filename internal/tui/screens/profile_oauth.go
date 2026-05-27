package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/devicecode"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

//
// Messages
//

type oauthListReloadedMsg struct {
	rows []gen.ListOAuthCredentialsForUserRow
	err  error
}

type oauthFlowStartedMsg struct {
	flow     *devicecode.Flow
	provider auth.OAuthProviderKind
	err      error
}

type oauthPollResultMsg struct {
	result devicecode.Result
	err    error
}

type oauthPollTickMsg struct {
	flowID string
}

type oauthUnlinkedMsg struct {
	id  int64
	err error
}

//
// Entry / list view
//

// openOAuth switches the screen to the linked-accounts modal and triggers
// a fresh reload. previousMode is saved so Esc returns the user to the
// profile tab they came from (modeTabProfile or modeTabSettings — the
// latter shouldn't happen in practice since the button only sits on the
// profile tab, but the symmetry doesn't hurt).
func (m *Profile) openOAuth() tea.Cmd {
	m.previousMode = m.mode
	m.mode = modeOAuth
	m.oauthErr = ""
	m.oauthCursor = 0
	return m.reloadOAuth()
}

func (m *Profile) reloadOAuth() tea.Cmd {
	userID := m.sess.Identity.UserID
	queries := m.sess.Queries
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		rows, err := queries.ListOAuthCredentialsForUser(ctx, userID)
		return oauthListReloadedMsg{rows: rows, err: err}
	}
}

func (m *Profile) handleOAuthKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = m.previousMode
		m.oauthErr = ""
		return m, nil
	case "up", "k":
		if m.oauthCursor > 0 {
			m.oauthCursor--
		}
	case "down", "j":
		if m.oauthCursor < len(m.oauthCreds)-1 {
			m.oauthCursor++
		}
	case "a":
		return m, m.openOAuthAdd()
	case "r":
		// Re-authorize the selected row: starts a fresh device flow for
		// the same provider. The device-code service detects the
		// existing (provider, subject) and upserts tokens.
		return m, m.beginOAuthReauth()
	case "d", "delete":
		return m, m.requestOAuthUnlink()
	}
	return m, nil
}

func (m *Profile) requestOAuthUnlink() tea.Cmd {
	if m.oauthCursor >= len(m.oauthCreds) {
		return nil
	}
	target := m.oauthCreds[m.oauthCursor]
	label := oauthRowDisplayName(target)
	m.confirm = components.NewConfirm(
		"unlink connected account",
		fmt.Sprintf("unlink %s account (%s)? you'll need to re-authorize to use it again.", target.Provider, label),
	)
	m.confirmKind = fmt.Sprintf("removeOAuth:%d", target.ID)
	m.confirmReturnMode = modeOAuth
	m.mode = modeConfirm
	return nil
}

// unlinkOAuth deletes the credential row (and via FK cascade, the
// oauth_tokens row). The audit event is fire-and-forget. Token revocation
// at the provider would happen here in a follow-up — Google has a revoke
// endpoint, Microsoft doesn't expose one — but unlink succeeds locally
// regardless of provider response.
func (m *Profile) unlinkOAuth(id int64) tea.Cmd {
	queries := m.sess.Queries
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if _, err := queries.DeleteCredentialByID(ctx, gen.DeleteCredentialByIDParams{ID: id, UserID: userID}); err != nil {
			return oauthUnlinkedMsg{id: id, err: err}
		}
		return oauthUnlinkedMsg{id: id}
	}
}

//
// Provider picker
//

func (m *Profile) openOAuthAdd() tea.Cmd {
	if m.sess.OAuthDeviceCode == nil {
		m.oauthErr = "OAuth linking isn't configured on this server."
		return nil
	}
	m.mode = modeOAuthAdd
	m.oauthErr = ""
	return nil
}

func (m *Profile) handleOAuthAddKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = modeOAuth
		m.oauthErr = ""
		return m, nil
	case "g", "G":
		return m, m.beginOAuthDevice(auth.OAuthGoogle)
	case "m", "M":
		return m, m.beginOAuthDevice(auth.OAuthMicrosoft)
	}
	return m, nil
}

func (m *Profile) beginOAuthDevice(provider auth.OAuthProviderKind) tea.Cmd {
	if m.sess.OAuthDeviceCode == nil {
		m.oauthErr = "OAuth linking isn't configured on this server."
		return nil
	}
	svc := m.sess.OAuthDeviceCode
	userID := m.sess.Identity.UserID
	m.oauthBusy = true
	m.oauthErr = ""
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(15 * time.Second)
		defer cancel()
		flow, err := svc.Begin(ctx, userID, provider)
		return oauthFlowStartedMsg{flow: flow, provider: provider, err: err}
	}
}

// beginOAuthReauth starts a device flow on the *currently selected* row's
// provider. The device-code service's commitApproved logic detects the
// existing (provider, subject) and routes to the upsert branch — replacing
// just the oauth_tokens row without touching the credential.
func (m *Profile) beginOAuthReauth() tea.Cmd {
	if m.oauthCursor >= len(m.oauthCreds) {
		return nil
	}
	row := m.oauthCreds[m.oauthCursor]
	provider := auth.OAuthProviderKind(row.Provider)
	return m.beginOAuthDevice(provider)
}

//
// Device-code waiting view
//

func (m *Profile) handleOAuthDeviceKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		// Cancel: drop the flow on the floor. Redis TTL will reap it.
		m.oauthFlow = nil
		m.oauthFlowStatus = ""
		m.mode = modeOAuth
		return m, nil
	}
	return m, nil
}

// pollOAuthFlow runs one Poll call. Returns a oauthPollResultMsg either
// way; on Pending/SlowDown the Update handler schedules the next tick.
func (m *Profile) pollOAuthFlow() tea.Cmd {
	if m.oauthFlow == nil || m.sess.OAuthDeviceCode == nil {
		return nil
	}
	svc := m.sess.OAuthDeviceCode
	flowID := m.oauthFlow.ID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(20 * time.Second)
		defer cancel()
		res, err := svc.Poll(ctx, flowID)
		return oauthPollResultMsg{result: res, err: err}
	}
}

// scheduleOAuthPoll wraps pollOAuthFlow in a tea.Tick so the IdP-specified
// interval is honored between polls.
func (m *Profile) scheduleOAuthPoll(after time.Duration) tea.Cmd {
	if after <= 0 {
		after = 3 * time.Second
	}
	return tea.Tick(after, func(time.Time) tea.Msg {
		if m.oauthFlow == nil {
			return nil
		}
		return oauthPollTickMsg{flowID: m.oauthFlow.ID}
	})
}

//
// Render
//

func (m *Profile) renderOAuthModal() string {
	innerW := m.sess.Width - 12
	if innerW > 90 {
		innerW = 90
	}
	if innerW < 50 {
		innerW = 50
	}
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("connected accounts")
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("Google or Microsoft accounts linked to this handle. We refresh tokens automatically so Gmail/Drive/Outlook/OneDrive integrations stay alive.")
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("↑/↓ select · a add · r re-authorize · d unlink · Esc back")

	rows := make([]string, 0, len(m.oauthCreds)+1)
	if len(m.oauthCreds) == 0 {
		rows = append(rows, lipgloss.NewStyle().Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).
			Render("no connected accounts yet — press 'a' to link one."))
	}
	for i, row := range m.oauthCreds {
		display := oauthRowDisplayName(row)
		added := m.sess.DisplayPrefs.FormatDate(row.CreatedAt.Time)
		status := oauthRowStatus(row)
		line := fmt.Sprintf("%s · %s · added %s",
			runewidth.Truncate(row.Provider, 12, "…"),
			runewidth.Truncate(display, 32, "…"),
			added,
		)
		statusLine := lipgloss.NewStyle().
			Foreground(oauthStatusColor(status)).
			Render("  " + status)
		if i == m.oauthCursor {
			line = lipgloss.NewStyle().Bold(true).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorYellow)).Render("▸ " + line)
		} else {
			line = "  " + line
		}
		rows = append(rows, line, statusLine)
	}

	parts := []string{header, blurb, ""}
	if m.oauthErr != "" {
		parts = append(parts, lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! "+m.oauthErr), "")
	}
	parts = append(parts, strings.Join(rows, "\n"), "", hint)
	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(parts, "\n"))
}

func (m *Profile) renderOAuthAddModal() string {
	innerW := 56
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("link an account")
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("pick a provider. We'll show you a short code to enter on the provider's verification page — no password ever touches the BBS.")
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("g Google · m Microsoft · Esc cancel")
	parts := []string{header, blurb, "", hint}
	if m.oauthBusy {
		parts = append(parts, "", lipgloss.NewStyle().Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).Render("contacting provider…"))
	}
	if m.oauthErr != "" {
		parts = append(parts, "", lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! "+m.oauthErr))
	}
	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(parts, "\n"))
}

func (m *Profile) renderOAuthDeviceModal() string {
	innerW := 60
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).
		Render(fmt.Sprintf("link %s account", m.oauthFlowProvider))
	if m.oauthFlow == nil {
		return theme.ModalFrame.Width(innerW + 6).Render(header + "\n\n…\n")
	}

	urlStyled := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).
		Render(m.oauthFlow.VerificationURL)
	codeStyled := lipgloss.NewStyle().Bold(true).
		Background(lipgloss.Color(theme.ColorSurfaceAlt)).
		Foreground(lipgloss.Color(theme.ColorYellow)).
		Padding(0, 2).
		Render(m.oauthFlow.UserCode)

	instructions := lipgloss.NewStyle().Width(innerW).Render(strings.Join([]string{
		"1. on any device, open:",
		"   " + urlStyled,
		"",
		"2. enter this code when prompted:",
		"   " + codeStyled,
		"",
		"3. approve the scopes — this terminal will catch up automatically.",
	}, "\n"))

	status := m.oauthFlowStatus
	if status == "" {
		status = "waiting…"
	}
	statusLine := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Render(status)

	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Render("Esc cancel")
	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join([]string{
		header, "", instructions, "", statusLine, "", hint,
	}, "\n"))
}

//
// Helpers
//

// oauthRowDisplayName picks the most user-recognizable label for a linked
// account row. Prefers the email captured in metadata; falls back to the
// provider's stable subject.
func oauthRowDisplayName(row gen.ListOAuthCredentialsForUserRow) string {
	if len(row.Metadata) > 0 {
		if email := extractMetadataString(row.Metadata, "email"); email != "" {
			return email
		}
	}
	return row.Subject
}

// extractMetadataString reads a top-level string field from the JSONB
// metadata blob without unmarshalling the whole thing. Returns "" on miss.
func extractMetadataString(raw []byte, field string) string {
	// Tiny scanner — the metadata blob is always shallow {"email":"...",
	// "name":"..."} so a full json.Unmarshal would be overkill. Returns the
	// raw string contents between the matching quotes.
	needle := []byte(`"` + field + `":"`)
	i := strings.Index(string(raw), string(needle))
	if i < 0 {
		return ""
	}
	start := i + len(needle)
	for j := start; j < len(raw); j++ {
		if raw[j] == '"' && raw[j-1] != '\\' {
			return string(raw[start:j])
		}
	}
	return ""
}

// oauthRowStatus returns a short user-facing status string driven by the
// joined oauth_tokens row. The LEFT JOIN may yield nulls if the credential
// somehow lost its token row — surface that distinctly so the user knows
// to re-authorize.
func oauthRowStatus(row gen.ListOAuthCredentialsForUserRow) string {
	if !row.AccessExpiresAt.Valid {
		return "no token on file · press 'r' to authorize"
	}
	needsReauth := false
	if row.NeedsReauth != nil {
		needsReauth = *row.NeedsReauth
	}
	if needsReauth {
		return "needs re-authorization · press 'r'"
	}
	lastRefresh := "—"
	if row.LastRefreshedAt.Valid {
		lastRefresh = humanizeAgo(time.Since(row.LastRefreshedAt.Time))
	}
	exp := row.AccessExpiresAt.Time
	if time.Until(exp) <= 0 {
		return fmt.Sprintf("expired · refresher will retry · last refresh %s ago", lastRefresh)
	}
	return fmt.Sprintf("active · expires in %s · last refresh %s ago",
		humanizeAgo(time.Until(exp)), lastRefresh)
}

func oauthStatusColor(status string) lipgloss.Color {
	switch {
	case strings.HasPrefix(status, "active"):
		return lipgloss.Color(theme.ColorGreen)
	case strings.HasPrefix(status, "needs"), strings.HasPrefix(status, "no token"):
		return lipgloss.Color(theme.ColorRed)
	case strings.HasPrefix(status, "expired"):
		return lipgloss.Color(theme.ColorYellow)
	}
	return lipgloss.Color(theme.ColorDim)
}

// humanizeAgo renders a time.Duration as a short human string suitable for
// the status line ("3 min", "2 hr", "5 days"). Negative durations are
// treated as zero.
func humanizeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Map device-flow result statuses to user-facing strings consumed by
// renderOAuthDeviceModal.
func oauthStatusForResult(kind devicecode.ResultKind) string {
	switch kind {
	case devicecode.ResultPending:
		return "waiting for you to approve…"
	case devicecode.ResultSlowDown:
		return "the provider is asking us to slow down — still waiting…"
	case devicecode.ResultDenied:
		return "you cancelled the consent — press Esc to back out."
	case devicecode.ResultExpired:
		return "the code expired before you approved — press Esc and try again."
	case devicecode.ResultDuplicate:
		return "this account is already linked to a different handle."
	}
	return ""
}

// oauthBeginErrorMessage maps a devicecode.Begin error to a user-facing
// string. Callers fall through to err.Error() for unknown failures.
func oauthBeginErrorMessage(err error) string {
	if errors.Is(err, devicecode.ErrProviderUnavailable) {
		return "linking from terminal isn't available for this provider."
	}
	if errors.Is(err, devicecode.ErrRateLimited) {
		return "too many recent attempts — wait a minute and try again."
	}
	return err.Error()
}
