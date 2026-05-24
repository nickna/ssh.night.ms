package screens

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Sysop is the moderation console — left pane users, right pane recent
// audit, bottom command line. Port of src/Night.Ms.SshServer/Tui/Screens/
// AdminScreen.cs.
type Sysop struct {
	sess    *session.Session
	users   []gen.ListUsersAlphabeticalRow
	audit   []gen.RecentAuditWithActorRow
	status  string
	loading bool

	cmd textinput.Model

	// metrics is the cached system-metrics sample re-read every 2s via a
	// tea.Tick. Mirrors src/Night.Ms.SshServer/Diagnostics/SystemMetricsCollector.
	metrics sysopMetrics

	// pendingWall is set when the user typed "wall <msg>" — we render a
	// confirm prompt and wait for Y/N before firing. Mirrors the .NET
	// AdminScreen.WallAsync MessageBox.Query gate.
	pendingWall string
}

type sysopMetrics struct {
	at         time.Time
	allocMB    float64
	sysMB      float64
	goroutines int
}

// sysopTickMsg fires every 2 seconds while the sysop screen is mounted so
// the footer line keeps refreshing without user input.
type sysopTickMsg struct{}

// NewSysop is registered as the DestSysop screen. The lobby only routes
// sysops here (the carousel hides the slot otherwise), so we don't
// re-check Identity.IsSysop — defense-in-depth would belong in the
// nav layer.
func NewSysop(sess *session.Session) tea.Model {
	t := textinput.New()
	t.Placeholder = "ban <handle> | sysop <handle> | wall <msg> | refresh | help"
	t.CharLimit = 200
	t.Focus()
	return &Sysop{sess: sess, cmd: t, loading: true}
}

type sysopLoadedMsg struct {
	users []gen.ListUsersAlphabeticalRow
	audit []gen.RecentAuditWithActorRow
	err   error
}

type sysopCmdDoneMsg struct {
	status string
	reload bool
}

func (m *Sysop) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.sampleMetricsCmd(), m.scheduleMetricsTick())
}

// sampleMetricsCmd snapshots runtime.MemStats + goroutine count off the
// main loop so the Update doesn't pay for a forced GC scan.
func (m *Sysop) sampleMetricsCmd() tea.Cmd {
	return func() tea.Msg {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return sysopMetrics{
			at:         time.Now(),
			allocMB:    float64(stats.Alloc) / 1024 / 1024,
			sysMB:      float64(stats.Sys) / 1024 / 1024,
			goroutines: runtime.NumGoroutine(),
		}
	}
}

func (m *Sysop) scheduleMetricsTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return sysopTickMsg{} })
}

func (m *Sysop) loadCmd() tea.Cmd {
	q := m.sess.Queries
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		users, uErr := q.ListUsersAlphabetical(ctx)
		audit, aErr := q.RecentAuditWithActor(ctx)
		if uErr != nil {
			return sysopLoadedMsg{err: uErr}
		}
		if aErr != nil {
			return sysopLoadedMsg{err: aErr}
		}
		return sysopLoadedMsg{users: users, audit: audit}
	}
}

func (m *Sysop) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sysopLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.status = "[!] load: " + msg.err.Error()
			return m, nil
		}
		m.users = msg.users
		m.audit = msg.audit
		return m, nil

	case sysopCmdDoneMsg:
		m.status = msg.status
		if msg.reload {
			return m, m.loadCmd()
		}
		return m, nil

	case sysopMetrics:
		m.metrics = msg
		return m, nil

	case sysopTickMsg:
		return m, tea.Batch(m.sampleMetricsCmd(), m.scheduleMetricsTick())

	case tea.KeyMsg:
		// Wall confirmation has its own one-shot key handling.
		if m.pendingWall != "" {
			switch strings.ToLower(msg.String()) {
			case "y", "enter":
				body := m.pendingWall
				m.pendingWall = ""
				return m, m.wallCmd(body)
			case "n", "esc":
				m.pendingWall = ""
				m.status = "wall broadcast cancelled."
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestLobby)
		case "enter":
			line := strings.TrimSpace(m.cmd.Value())
			if line == "" {
				return m, nil
			}
			m.cmd.SetValue("")
			// Special-case: wall asks for confirmation BEFORE firing.
			if v, target := splitVerb(line); v == "wall" && target != "" {
				if len(target) > 500 {
					m.status = "[!] wall message too long (max 500 chars)."
					return m, nil
				}
				m.pendingWall = target
				return m, nil
			}
			return m, m.dispatch(line)
		}
	}

	var cmd tea.Cmd
	m.cmd, cmd = m.cmd.Update(msg)
	return m, cmd
}

// dispatch parses one command line + dispatches the matching async tea.Cmd.
// All mutating commands return sysopCmdDoneMsg{reload: true} so the user
// sees the new state without manually typing 'refresh'.
func (m *Sysop) dispatch(line string) tea.Cmd {
	verb, target := splitVerb(line)
	switch verb {
	case "help", "?":
		return done("ban | unban | sysop | unsysop | clear-passwordless | wall <msg> | refresh | help", false)
	case "refresh":
		return func() tea.Msg { return sysopCmdDoneMsg{status: "refreshed.", reload: true} }
	case "wall":
		return m.wallCmd(target)
	case "ban":
		return m.toggleCmd(target, "ban", true)
	case "unban":
		return m.toggleCmd(target, "unban", false)
	case "sysop":
		return m.toggleCmd(target, "sysop", true)
	case "unsysop":
		return m.toggleCmd(target, "unsysop", false)
	case "clear-passwordless":
		return m.clearPasswordlessCmd(target)
	}
	return done("[!] unknown command: "+verb+" — type help", false)
}

// toggleCmd handles ban/unban/sysop/unsysop in one path. The verb is also
// used to derive the audit_log.action string and the status confirmation,
// so it stays as a string rather than a bool pair.
func (m *Sysop) toggleCmd(handle, verb string, want bool) tea.Cmd {
	if handle == "" {
		return done("[!] "+verb+" requires a handle.", false)
	}
	queries := m.sess.Queries
	actor := m.sess.Identity
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()

		target, err := queries.GetUserByHandle(ctx, handle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return sysopCmdDoneMsg{status: "[!] no such user: " + handle}
			}
			return sysopCmdDoneMsg{status: "[!] lookup failed: " + err.Error()}
		}
		if target.ID == actor.UserID {
			return sysopCmdDoneMsg{status: "[!] you can't change your own status."}
		}

		var action string
		switch verb {
		case "ban":
			if err := queries.SetUserBanned(ctx, gen.SetUserBannedParams{ID: target.ID, IsBanned: true}); err != nil {
				return sysopCmdDoneMsg{status: "[!] " + verb + ": " + err.Error()}
			}
			action = "user.ban"
		case "unban":
			if err := queries.SetUserBanned(ctx, gen.SetUserBannedParams{ID: target.ID, IsBanned: false}); err != nil {
				return sysopCmdDoneMsg{status: "[!] " + verb + ": " + err.Error()}
			}
			action = "user.unban"
		case "sysop":
			if err := queries.SetUserSysop(ctx, gen.SetUserSysopParams{ID: target.ID, IsSysop: true}); err != nil {
				return sysopCmdDoneMsg{status: "[!] " + verb + ": " + err.Error()}
			}
			action = "user.promote_sysop"
		case "unsysop":
			if err := queries.SetUserSysop(ctx, gen.SetUserSysopParams{ID: target.ID, IsSysop: false}); err != nil {
				return sysopCmdDoneMsg{status: "[!] " + verb + ": " + err.Error()}
			}
			action = "user.demote_sysop"
		}
		_ = writeAudit(ctx, queries, actor.UserID, action, "user", target.ID)
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("%s %s — ok.", action, target.Handle),
			reload: true,
		}
	}
}

func (m *Sysop) clearPasswordlessCmd(handle string) tea.Cmd {
	if handle == "" {
		return done("[!] clear-passwordless requires a handle.", false)
	}
	queries := m.sess.Queries
	actor := m.sess.Identity
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		target, err := queries.GetUserByHandle(ctx, handle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return sysopCmdDoneMsg{status: "[!] no such user: " + handle}
			}
			return sysopCmdDoneMsg{status: "[!] lookup failed: " + err.Error()}
		}
		if !target.RequireSshKey {
			return sysopCmdDoneMsg{
				status: fmt.Sprintf("[!] %s doesn't have passwordless mode enabled.", target.Handle),
			}
		}
		if err := queries.ClearUserRequireSSHKey(ctx, target.ID); err != nil {
			return sysopCmdDoneMsg{status: "[!] clear-passwordless: " + err.Error()}
		}
		_ = writeAudit(ctx, queries, actor.UserID, "user.passwordless.reset_by_sysop", "user", target.ID)
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("clear-passwordless %s — ok.", target.Handle),
			reload: true,
		}
	}
}

// wallCmd publishes a wall broadcast over the realtime bus and logs an
// audit row. Length-capped at 500 chars to keep an accidental paste from
// nuking 200 connected terminals.
func (m *Sysop) wallCmd(message string) tea.Cmd {
	if message == "" {
		return done("[!] wall requires a message. Usage: wall <message>", false)
	}
	if len(message) > 500 {
		return done("[!] wall message too long (max 500 chars).", false)
	}
	wall := m.sess.Wall
	if wall == nil {
		return done("[!] wall dispatcher not configured.", false)
	}
	from := m.sess.Identity.Handle
	queries := m.sess.Queries
	actorID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		if err := wall.Publish(ctx, from, message); err != nil {
			return sysopCmdDoneMsg{status: "[!] wall publish: " + err.Error()}
		}
		_ = writeAudit(ctx, queries, actorID, "wall.broadcast", "system", 0)
		return sysopCmdDoneMsg{status: "wall broadcast sent.", reload: true}
	}
}

// done is the tea.Cmd convenience for "show this status, optionally reload".
func done(status string, reload bool) tea.Cmd {
	return func() tea.Msg { return sysopCmdDoneMsg{status: status, reload: reload} }
}

// splitVerb splits "verb arg…" into (verb-lower, trimmed-rest). The .NET
// version uses a 2-element split with TrimEntries; this matches.
func splitVerb(line string) (string, string) {
	verb, rest, _ := strings.Cut(line, " ")
	return strings.ToLower(strings.TrimSpace(verb)), strings.TrimSpace(rest)
}

// writeAudit is a small helper around InsertAuditLogSimple. Failure is
// logged but never blocks the user — the mutating change is already
// committed at this point. Pass targetID = 0 for system-scoped actions
// (wall broadcast etc.) so the column lands NULL.
func writeAudit(ctx context.Context, q *gen.Queries, actorID int64, action, targetType string, targetID int64) error {
	aid := actorID
	var tidPtr *int64
	if targetID != 0 {
		tid := targetID
		tidPtr = &tid
	}
	return q.InsertAuditLogSimple(ctx, gen.InsertAuditLogSimpleParams{
		ActorID:    &aid,
		Action:     action,
		TargetType: targetType,
		TargetID:   tidPtr,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
}

var (
	sysopTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	sysopHint   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	sysopHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccentDim))
	sysopBan    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed)).Bold(true)
	sysopFlag   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
	sysopMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	sysopErr    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *Sysop) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var b strings.Builder
	b.WriteString(sysopTitle.Render("sysop console — " + m.sess.Identity.Handle))
	b.WriteString("  " + sysopHint.Render("type 'help' + Enter · Esc back to lobby"))
	b.WriteString("\n\n")
	if m.loading {
		b.WriteString(sysopHint.Render("loading…"))
		return b.String()
	}

	// Two-pane layout — left = users, right = audit. Width-budgeted at half
	// each minus a 2-col gutter.
	paneW := (m.sess.Width - 2) / 2
	if paneW < 30 {
		paneW = 30
	}
	listH := m.sess.Height - 8 // leave room for header + cmd + status
	if listH < 6 {
		listH = 6
	}

	left := m.renderUsers(paneW, listH)
	right := m.renderAudit(paneW, listH)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right))
	b.WriteString("\n\n")

	if m.pendingWall != "" {
		// Confirm prompt — replaces the input until resolved.
		b.WriteString(sysopErr.Render(fmt.Sprintf("Wall broadcast to ALL sessions:")))
		b.WriteString("\n  ")
		b.WriteString(sysopFlag.Render(`"` + m.pendingWall + `"`))
		b.WriteString("\n")
		b.WriteString(sysopHint.Render("[Y] send  ·  [N] cancel"))
		b.WriteString("\n")
	} else {
		b.WriteString(sysopHint.Render("> ") + m.cmd.View())
		b.WriteString("\n")
	}
	if m.status != "" {
		if strings.HasPrefix(m.status, "[!]") {
			b.WriteString(sysopErr.Render(m.status))
		} else {
			b.WriteString(sysopHint.Render(m.status))
		}
		b.WriteString("\n")
	}
	// Live metrics footer — mirrors .NET SystemMetricsSnapshot.FormatCompact.
	if !m.metrics.at.IsZero() {
		b.WriteString(sysopMuted.Render(fmt.Sprintf(
			"heap %.1f MB · sys %.1f MB · goroutines %d  (refreshed %s)",
			m.metrics.allocMB, m.metrics.sysMB, m.metrics.goroutines,
			m.metrics.at.Format("15:04:05"),
		)))
	}
	return b.String()
}

func (m *Sysop) renderUsers(w, h int) string {
	var b strings.Builder
	b.WriteString(sysopHeader.Render("users (S=sysop, B=banned, K=ssh-key-only):"))
	b.WriteString("\n")
	rows := m.users
	if len(rows) > h-1 {
		rows = rows[:h-1]
	}
	for _, u := range rows {
		flags := []rune{'-', '-', '-'}
		if u.IsSysop {
			flags[0] = 'S'
		}
		if u.IsBanned {
			flags[1] = 'B'
		}
		if u.RequireSshKey {
			flags[2] = 'K'
		}
		flagStr := string(flags)
		if u.IsBanned {
			flagStr = sysopBan.Render(flagStr)
		} else if u.IsSysop {
			flagStr = sysopFlag.Render(flagStr)
		}
		seen := "<never>"
		if u.LastSeenAt.Valid {
			seen = m.sess.DisplayPrefs.FormatDateTime(u.LastSeenAt.Time)
		}
		line := fmt.Sprintf("%s %-20s %s", flagStr, truncateRow(u.Handle, 20), sysopMuted.Render(seen))
		b.WriteString(line)
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

func (m *Sysop) renderAudit(w, h int) string {
	var b strings.Builder
	b.WriteString(sysopHeader.Render("audit log (recent 50):"))
	b.WriteString("\n")
	rows := m.audit
	if len(rows) > h-1 {
		rows = rows[:h-1]
	}
	for _, a := range rows {
		ts := "             "
		if a.CreatedAt.Valid {
			ts = a.CreatedAt.Time.UTC().Format("01-02 15:04")
		}
		actor := a.ActorHandle
		if actor == "" {
			actor = "<system>"
		}
		var target string
		if a.TargetID != nil {
			target = fmt.Sprintf("%s#%d", a.TargetType, *a.TargetID)
		} else {
			target = a.TargetType
		}
		line := fmt.Sprintf("%s %-12s %-22s %s",
			sysopMuted.Render(ts),
			truncateRow(actor, 12),
			truncateRow(a.Action, 22),
			truncateRow(target, 20),
		)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// truncateRow keeps row text inside the two-pane budget; finance.go has its
// own truncate() helper, hence the differentiated name.
func truncateRow(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
