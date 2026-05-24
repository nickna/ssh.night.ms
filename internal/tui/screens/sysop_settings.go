package screens

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/settings"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// renderSettings draws the Settings tab body — one row per catalog entry with
// the current value, the default value, and a source indicator showing whether
// the row is db-overridden or falling through to the default.
//
// Editing happens at the bottom prompt (`set <key> <value>` / `reset <key>`)
// rather than inline so the flow matches the Users and Bans tabs and the
// command + audit path stays uniform.
func (m *Sysop) renderSettings(w, h int) string {
	var b strings.Builder
	b.WriteString(sysopHeader.Render("runtime settings — set <key> <value> · reset <key>:"))
	b.WriteString("\n")

	if m.sess.Settings == nil {
		b.WriteString(sysopErr.Render("settings cache not configured — restart with NIGHTMS_REDIS_CONN set"))
		return lipgloss.NewStyle().Width(w).Render(b.String())
	}
	snap := m.sess.Settings.Get()

	// Clamp the cursor to the catalog length; the catalog is closed so this
	// only matters defensively if a setting is removed mid-session.
	if m.settingsCursor >= len(settings.Catalog) {
		m.settingsCursor = len(settings.Catalog) - 1
	}
	if m.settingsCursor < 0 {
		m.settingsCursor = 0
	}

	// Column widths chosen so the catalog renders in 80 columns: key 26,
	// value 22, default 22, type 4, plus separators. Description trails.
	keyW := 26
	valW := 22
	defW := 22

	for i, def := range settings.Catalog {
		cur := snap.String(def.Key)
		dflt := m.sess.Settings.DefaultString(def.Key)
		overridden := cur != dflt
		source := sysopMuted.Render("default")
		if overridden {
			source = sysopFlag.Render("custom ")
		}

		curRendered := truncateRow(cur, valW)
		if def.Type == settings.TypeBool {
			if cur == "true" {
				curRendered = sysopFlag.Render(truncateRow(cur, valW))
			} else {
				curRendered = sysopMuted.Render(truncateRow(cur, valW))
			}
		}

		line := fmt.Sprintf(
			"%-*s  %-*s  default %-*s  %s  %s",
			keyW, truncateRow(def.Key, keyW),
			valW, curRendered,
			defW, truncateRow(dflt, defW),
			source,
			sysopMuted.Render("("+def.Type+")"),
		)
		if i == m.settingsCursor {
			line = lipgloss.NewStyle().Background(lipgloss.Color(theme.ColorAccentDim)).Foreground(lipgloss.Color(theme.ColorBackground)).Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Per-row description rendered below the table so the highlighted row's
	// purpose is visible without growing every row to two lines.
	if m.settingsCursor < len(settings.Catalog) {
		desc := settings.Catalog[m.settingsCursor].Description
		b.WriteString("\n")
		b.WriteString(sysopHint.Render("  " + desc))
		b.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// handleSettingsKey processes cursor-navigation keys on the Settings tab. It
// returns handled=true for the keys it consumes so the outer router doesn't
// also forward them to the textinput. Returning handled=false lets the
// textinput receive the keystroke (used for printable chars typed into the
// `set ...` prompt).
func (m *Sysop) handleSettingsKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	// Cursor navigation only when the command line is empty so typing into
	// the prompt doesn't move the cursor underneath.
	if m.cmd.Value() != "" {
		return false, m, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
		return true, m, nil
	case "down", "j":
		if m.settingsCursor < len(settings.Catalog)-1 {
			m.settingsCursor++
		}
		return true, m, nil
	case "home", "g":
		m.settingsCursor = 0
		return true, m, nil
	case "end", "G":
		m.settingsCursor = len(settings.Catalog) - 1
		return true, m, nil
	}
	return false, m, nil
}

// setSettingCmd persists a setting via the cache, writes an audit row with the
// old → new pair, and reloads the tab on success. target is the raw rest of
// the line after the verb — "<key> <value>" with value spanning to EOL so
// string settings (motd, signups_disabled_message) can include spaces.
func (m *Sysop) setSettingCmd(target string) tea.Cmd {
	if m.sess.Settings == nil {
		return done("[!] settings cache not configured.", false)
	}
	key, value, ok := strings.Cut(target, " ")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !ok || key == "" || value == "" {
		return done("[!] usage: set <key> <value>", false)
	}
	cache := m.sess.Settings
	queries := m.sess.Queries
	actorID := m.sess.Identity.UserID
	oldValue := cache.Get().String(key)
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		actor := actorID
		var actorPtr *int64
		if actor != 0 {
			actorPtr = &actor
		}
		if err := cache.Set(ctx, key, value, actorPtr); err != nil {
			return sysopCmdDoneMsg{status: "[!] set " + key + ": " + err.Error()}
		}
		writeSettingsAudit(ctx, queries, actorID, "settings.set", key, oldValue, value)
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("set %s = %s (was %s) — ok.", key, value, oldValue),
			reload: true,
		}
	}
}

// resetSettingCmd deletes the row for key, returning the snapshot to its
// default. Audit captures the old value + a "<default>" sentinel for new so
// the events feed shows the reset clearly.
func (m *Sysop) resetSettingCmd(target string) tea.Cmd {
	if m.sess.Settings == nil {
		return done("[!] settings cache not configured.", false)
	}
	key := strings.TrimSpace(target)
	if key == "" {
		return done("[!] usage: reset <key>", false)
	}
	cache := m.sess.Settings
	queries := m.sess.Queries
	actorID := m.sess.Identity.UserID
	oldValue := cache.Get().String(key)
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		n, err := cache.Reset(ctx, key)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] reset " + key + ": " + err.Error()}
		}
		if n == 0 {
			return sysopCmdDoneMsg{status: "reset " + key + " — already at default."}
		}
		writeSettingsAudit(ctx, queries, actorID, "settings.reset", key, oldValue, cache.DefaultString(key))
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("reset %s — back to default %s (was %s).", key, cache.DefaultString(key), oldValue),
			reload: true,
		}
	}
}

// writeSettingsAudit writes an audit_log row with a JSON details payload
// {key, old, new}. Failure is logged but never blocks the user — the same
// fire-and-forget contract writeAudit uses for the simpler actions.
func writeSettingsAudit(ctx context.Context, q *gen.Queries, actorID int64, action, key, oldValue, newValue string) {
	aid := actorID
	details, err := json.Marshal(map[string]string{
		"key": key,
		"old": oldValue,
		"new": newValue,
	})
	if err != nil {
		details = []byte(`{}`)
	}
	_ = q.InsertAuditLog(ctx, gen.InsertAuditLogParams{
		ActorID:    &aid,
		Action:     action,
		TargetType: "settings",
		TargetID:   nil,
		Details:    details,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}
