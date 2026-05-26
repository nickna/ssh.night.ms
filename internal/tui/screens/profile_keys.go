package screens

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/auth"
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
	case "a":
		return m, m.openAddKey()
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
		Render("keys registered to your account.")
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("↑/↓ select · a add · d remove · Esc back")

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

// openAddKey switches to the add-key modal with two fresh textinputs.
// Patterned on openPassword: each invocation builds new inputs so a
// cancelled attempt can't leak the previous paste into the next.
func (m *Profile) openAddKey() tea.Cmd {
	m.previousMode = modeKeys
	m.mode = modeAddKey
	m.addKeyErr = ""
	m.addKeyBusy = false
	m.addKeyFocus = 0

	pub := textinput.New()
	pub.Placeholder = "ssh-ed25519 AAAA… user@host"
	pub.CharLimit = 4096
	pub.Width = 56
	m.addKeyPublic = pub

	lbl := textinput.New()
	lbl.Placeholder = "(optional)"
	lbl.CharLimit = 80
	lbl.Width = 32
	m.addKeyLabel = lbl

	m.applyAddKeyFocus()
	return textinput.Blink
}

func (m *Profile) applyAddKeyFocus() {
	m.addKeyPublic.Blur()
	m.addKeyLabel.Blur()
	switch m.addKeyFocus {
	case 0:
		m.addKeyPublic.Focus()
	case 1:
		m.addKeyLabel.Focus()
	}
}

func (m *Profile) handleAddKeyKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.addKeyBusy {
		// Swallow input while the insert is in flight; the result msg will
		// re-enable the form.
		return m, nil
	}
	switch k.String() {
	case "esc":
		m.mode = modeKeys
		m.addKeyErr = ""
		return m, nil
	case "tab":
		m.addKeyFocus = (m.addKeyFocus + 1) % 2
		m.applyAddKeyFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.addKeyFocus = (m.addKeyFocus + 1) % 2
		m.applyAddKeyFocus()
		return m, textinput.Blink
	case "enter":
		return m, m.submitAddKey()
	}
	var cmd tea.Cmd
	switch m.addKeyFocus {
	case 0:
		m.addKeyPublic, cmd = m.addKeyPublic.Update(k)
	case 1:
		m.addKeyLabel, cmd = m.addKeyLabel.Update(k)
	}
	return m, cmd
}

func (m *Profile) submitAddKey() tea.Cmd {
	if m.addKeyBusy {
		return nil
	}
	raw := m.addKeyPublic.Value()
	fingerprint, _, metadata, parseErr := auth.ParseAuthorizedKey(raw)
	if parseErr != nil {
		m.addKeyErr = "not a recognizable OpenSSH public key."
		return nil
	}

	// Match the web handler: empty label becomes "untitled" so the keys
	// list always has a non-empty display string.
	label := strings.TrimSpace(m.addKeyLabel.Value())
	if label == "" {
		label = "untitled"
	}
	labelPtr := &label

	userID := m.sess.Identity.UserID
	queries := m.sess.Queries
	svc := m.sess.Profile

	m.addKeyBusy = true
	m.addKeyErr = ""
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		_, err := queries.InsertSshCredential(ctx, gen.InsertSshCredentialParams{
			UserID:    userID,
			Subject:   fingerprint,
			Metadata:  metadata,
			Label:     labelPtr,
			CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if err != nil {
			if auth.IsDuplicateCredential(err) {
				return keyAddedMsg{err: fmt.Errorf("this public key is already registered")}
			}
			return keyAddedMsg{err: err}
		}
		// Best-effort audit log; insert already succeeded.
		_ = svc.LogKeyAdd(ctx, userID, fingerprint)
		return keyAddedMsg{fingerprint: fingerprint}
	}
}

// renderAddKeyModal builds the modal body for the add-key form.
func (m *Profile) renderAddKeyModal() string {
	innerW := 62
	header := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render("add ssh key")
	blurb := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).Width(innerW).
		Render("paste your public key (the one line from ~/.ssh/id_*.pub).")
	hint := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("Tab cycle · Enter add · Esc cancel")
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))

	lines := []string{header, blurb, ""}
	lines = append(lines, labelStyle.Render("public key"))
	lines = append(lines, m.addKeyPublic.View(), "")
	lines = append(lines, labelStyle.Render("label (optional)"))
	lines = append(lines, m.addKeyLabel.View(), "")
	if m.addKeyErr != "" {
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! "+m.addKeyErr), "")
	}
	if m.addKeyBusy {
		lines = append(lines, lipgloss.NewStyle().Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).Render("adding…"), "")
	}
	lines = append(lines, hint)
	return theme.ModalFrame.Width(innerW + 6).Render(strings.Join(lines, "\n"))
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
