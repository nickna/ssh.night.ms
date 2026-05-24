package screens

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// openKeys switches the screen to the SSH-keys list modal and triggers an
// initial reload from the DB.
func (m *Profile) openKeys() tea.Cmd {
	m.previousMode = m.mode
	m.mode = modeKeys
	m.keysCursor = 0
	return m.reloadKeys()
}

func (m *Profile) reloadKeys() tea.Cmd {
	userID := m.sess.Identity.UserID
	queries := m.sess.Queries
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		keys, err := queries.ListSshCredentialsForUser(ctx, userID)
		return keysReloadedMsg{keys: keys, err: err}
	}
}

func (m *Profile) handleKeysKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = m.previousMode
		return m, nil
	case "up", "k":
		if m.keysCursor > 0 {
			m.keysCursor--
		}
	case "down", "j":
		if m.keysCursor < len(m.keys)-1 {
			m.keysCursor++
		}
	case "d", "delete":
		return m, m.requestKeyDelete()
	}
	return m, nil
}

// requestKeyDelete enforces the lockout guards before showing a confirm
// modal. With RequireSshKey on, removing the last key would brick the
// account; without a password, removing the last key also would.
func (m *Profile) requestKeyDelete() tea.Cmd {
	if m.keysCursor >= len(m.keys) {
		return nil
	}
	target := m.keys[m.keysCursor]
	isLast := len(m.keys) == 1
	if isLast {
		if m.snap != nil && m.snap.RequireSshKey {
			m.notice = "passwordless mode is on — disable it before removing your last key."
			m.noticeKind = "err"
			return nil
		}
		if m.snap != nil && !m.snap.HasPassword {
			m.notice = "set a password first — removing your last key would lock you out."
			m.noticeKind = "err"
			return nil
		}
	}
	m.confirm = components.NewConfirm(
		"remove SSH key",
		fmt.Sprintf("remove %q (%s)? this cannot be undone.", credentialLabelOr(target, "(unlabeled)"), target.Subject),
	)
	m.confirmKind = fmt.Sprintf("removeKey:%d", target.ID)
	m.previousMode = modeKeys
	m.mode = modeConfirm
	return nil
}

func (m *Profile) deleteKey(id int64) tea.Cmd {
	queries := m.sess.Queries
	userID := m.sess.Identity.UserID
	svc := m.sess.Profile

	// Find the credential to capture its fingerprint for the audit-log.
	var fingerprint string
	for _, c := range m.keys {
		if c.ID == id {
			fingerprint = c.Subject
			break
		}
	}

	m.working = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if _, err := queries.DeleteCredentialByID(ctx, gen.DeleteCredentialByIDParams{ID: id, UserID: userID}); err != nil {
			return keyRemovedMsg{id: id, err: err}
		}
		// Best-effort audit log; deletion already succeeded.
		_ = svc.LogKeyRemoval(ctx, userID, fingerprint)
		return keyRemovedMsg{id: id}
	}
}

// renderKeysModal builds the modal body for the SSH-keys list view.
func (m *Profile) renderKeysModal() string {
	innerW := m.sess.Width - 12
	if innerW > 90 {
		innerW = 90
	}
	if innerW < 50 {
		innerW = 50
	}
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("SSH keys")
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("keys registered to your account. add new keys on the web — pasting public keys over SSH is fiddly.")
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("↑/↓ select · d remove · Esc back")

	rows := make([]string, 0, len(m.keys)+1)
	if len(m.keys) == 0 {
		rows = append(rows, lipgloss.NewStyle().Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).Render("no keys on file."))
	}
	for i, k := range m.keys {
		label := credentialLabelOr(k, "(unlabeled)")
		algo, _ := credentialAlgorithm(k)
		added := m.sess.DisplayPrefs.FormatDate(k.CreatedAt.Time)
		lastUsed := "—"
		if k.LastUsedAt.Valid {
			lastUsed = m.sess.DisplayPrefs.FormatDate(k.LastUsedAt.Time)
		}
		line := fmt.Sprintf("%s  %s  added %s  last used %s",
			runewidth.Truncate(label, 24, "…"), algo, added, lastUsed)
		fp := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorDim)).
			Render("  " + runewidth.Truncate(k.Subject, innerW-4, "…"))
		if i == m.keysCursor {
			line = lipgloss.NewStyle().Bold(true).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorYellow)).Render("▸ " + line)
		} else {
			line = "  " + line
		}
		rows = append(rows, line, fp)
	}

	body := strings.Join(rows, "\n")
	return theme.ModalFrame.Width(innerW + 6).Render(
		strings.Join([]string{header, blurb, "", body, "", hint}, "\n"),
	)
}

// credentialLabelOr returns *c.Label or fallback when nil/empty.
func credentialLabelOr(c gen.IdentityCredential, fallback string) string {
	if c.Label == nil || *c.Label == "" {
		return fallback
	}
	return *c.Label
}

// credentialAlgorithm decodes the JSON metadata blob to extract "algorithm".
// Returns "ssh-rsa"/"ssh-ed25519"/etc; empty when the blob is missing or
// malformed.
func credentialAlgorithm(c gen.IdentityCredential) (string, error) {
	if len(c.Metadata) == 0 {
		return "ssh-unknown", nil
	}
	var meta struct {
		Algorithm string `json:"algorithm"`
	}
	if err := json.Unmarshal(c.Metadata, &meta); err != nil {
		return "ssh-unknown", err
	}
	if meta.Algorithm == "" {
		return "ssh-unknown", nil
	}
	return meta.Algorithm, nil
}
