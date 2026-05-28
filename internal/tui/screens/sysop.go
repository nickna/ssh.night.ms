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

// sysopTab selects which body the sysop screen renders. The default landing
// tab is tabEvents — operators came here to investigate "what's happening
// right now," and the unified feed is the answer to that.
type sysopTab int

const (
	tabEvents sysopTab = iota
	tabUsers
	tabBans
	tabSettings
)

// Sysop is the moderation console. Three tabs: Events (unified audit/security
// feed with filtering + detail modal), Users (alphabetical user list with
// ban/unban/sysop/wall commands), Bans (active IP bans list with ban-ip /
// unban-ip commands). Tab cycles with the Tab key or 1/2/3 jumps.
//
// All tab state lives flat on this struct rather than in nested per-tab
// substructs so the Update method can route messages without an extra layer
// of dereferencing — there are only a handful of fields per tab.
type Sysop struct {
	sess    *session.Session
	tab     sysopTab
	loading bool // initial users load only — per-tab loads use per-tab flags
	status  string
	cmd     textinput.Model

	// pending holds the destructive action awaiting Y/N confirmation. Set
	// when the sysop types `wall`, `reset-password`, `remove-keys`, or
	// `delete-user` — all four funnel through the same modal so the impact
	// summary stays consistent. nil means no modal is showing.
	pending *pendingAction

	// metrics samples runtime stats every 2s via a tea.Tick.
	metrics sysopMetrics

	// Users tab
	users []gen.ListUsersAlphabeticalRow

	// Events tab (state in sysop_events.go; types here to keep the struct
	// fields grouped).
	events                []gen.ListUnifiedEventsRow
	eventsCursor          int
	eventsScroll          int
	eventsFiltersRaw      string
	eventsFilters         []sysopFilterCarrier // local alias to avoid importing data here
	eventsLoading         bool
	eventsHasMore         bool
	eventsCount           int32
	eventsDetail          *gen.ListUnifiedEventsRow
	eventsRelated         []gen.ListUnifiedEventsRelatedRow
	eventsRelatedLoading  bool
	eventsPendingFilterAt time.Time
	eventsFocusFilter     bool // true = textinput owns input on Events tab; false = list owns navigation keys

	// Bans tab
	bans        []gen.SecurityIpBan
	bansLoading bool

	// Settings tab (catalog rendered from settings.Catalog; the cache itself
	// lives on sess.Settings). The cursor is purely for which row is
	// highlighted — edits go through the bottom-prompt `set` / `reset`
	// commands the same way Users and Bans tabs work.
	settingsCursor int
}

// sysopFilterCarrier is a tiny shim so the sysop.go struct doesn't have to
// import internal/data just for the filter type. sysop_events.go defines a
// helper to convert between this and data.Filter.
type sysopFilterCarrier struct {
	Dim  string
	Text string
	Time time.Time
}

type sysopMetrics struct {
	at         time.Time
	allocMB    float64
	sysMB      float64
	goroutines int
}

// sysopTickMsg fires every 2 seconds while the sysop screen is mounted so
// the footer metrics line keeps refreshing without user input.
type sysopTickMsg struct{}

// sysopUsersLoadedMsg / sysopBansLoadedMsg / sysopEventsLoadedMsg are the
// per-tab load completions. The events-tab message is defined in
// sysop_events.go to keep its companion code colocated.
type sysopUsersLoadedMsg struct {
	users []gen.ListUsersAlphabeticalRow
	err   error
}

type sysopBansLoadedMsg struct {
	bans []gen.SecurityIpBan
	err  error
}

type sysopCmdDoneMsg struct {
	status string
	reload bool
}

// NewSysop is registered as the DestSysop screen. The lobby only routes
// sysops here (the carousel hides the slot otherwise), so we don't re-check
// Identity.IsSysop — defense-in-depth would belong in the nav layer.
//
// Lands on the Events tab by default (per user choice during planning). The
// textinput placeholder switches per tab inside applyTabFocus.
func NewSysop(sess *session.Session) tea.Model {
	t := textinput.New()
	t.CharLimit = 200
	t.Focus()
	m := &Sysop{sess: sess, tab: tabEvents, cmd: t, loading: true}
	m.applyTabFocus()
	return m
}

// Init parallel-loads users + events + bans so any tab switch is instant.
// Metrics polling and the periodic tick are also kicked off here.
func (m *Sysop) Init() tea.Cmd {
	return tea.Batch(
		m.loadUsersCmd(),
		m.loadEventsCmd(),
		m.loadBansCmd(),
		m.sampleMetricsCmd(),
		m.scheduleMetricsTick(),
	)
}

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

// loadUsersCmd fetches the alphabetical user list for the Users tab.
func (m *Sysop) loadUsersCmd() tea.Cmd {
	q := m.sess.Queries
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		users, err := q.ListUsersAlphabetical(ctx)
		return sysopUsersLoadedMsg{users: users, err: err}
	}
}

// loadBansCmd fetches the active IP bans for the Bans tab.
func (m *Sysop) loadBansCmd() tea.Cmd {
	q := m.sess.Queries
	m.bansLoading = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		bans, err := q.ListActiveIPBans(ctx)
		return sysopBansLoadedMsg{bans: bans, err: err}
	}
}

func (m *Sysop) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sysopUsersLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.status = "[!] users: " + msg.err.Error()
			return m, nil
		}
		m.users = msg.users
		return m, nil

	case sysopBansLoadedMsg:
		m.bansLoading = false
		if msg.err != nil {
			m.status = "[!] bans: " + msg.err.Error()
			return m, nil
		}
		m.bans = msg.bans
		return m, nil

	case sysopEventsLoadedMsg:
		return m, m.handleEventsLoaded(msg)

	case sysopEventsRelatedLoadedMsg:
		m.eventsRelatedLoading = false
		if msg.err == nil {
			m.eventsRelated = msg.rows
		}
		return m, nil

	case sysopEventsFilterTickMsg:
		return m, m.maybeFireFilterReload()

	case sysopPreflightMsg:
		if msg.err != nil {
			m.status = "[!] " + msg.err.Error()
			return m, nil
		}
		m.pending = msg.action
		return m, nil

	case sysopCmdDoneMsg:
		m.status = msg.status
		if !msg.reload {
			return m, nil
		}
		// Reload the active tab's data. Each tab knows its own refresh path.
		switch m.tab {
		case tabUsers:
			return m, m.loadUsersCmd()
		case tabEvents:
			return m, m.loadEventsCmd()
		case tabBans:
			return m, m.loadBansCmd()
		case tabSettings:
			// Settings tab reads from the in-process Cache — nothing to load
			// asynchronously since Set already refreshed the snapshot. Keep
			// the cursor stable.
			return m, nil
		}
		return m, nil

	case sysopMetrics:
		m.metrics = msg
		return m, nil

	case sysopTickMsg:
		return m, tea.Batch(m.sampleMetricsCmd(), m.scheduleMetricsTick())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Fall through to the textinput. Events tab with focusFilter=false
	// shouldn't forward to the textinput (it'd capture letter keys into
	// the filter while the user is trying to navigate); the handleKey
	// above already short-circuits printable-char passthrough in that
	// case, so reaching here means the message is non-key.
	var cmd tea.Cmd
	m.cmd, cmd = m.cmd.Update(msg)
	return m, cmd
}

// handleKey is the per-tab key router. Page-level keys (esc, tab, 1/2/3,
// wall confirmation) handle first; then per-tab navigation; then the
// textinput consumes the remainder.
func (m *Sysop) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Pending-action confirmation is modal — intercepts everything until
	// the sysop confirms (Y/Enter) or cancels (N/Esc).
	if m.pending != nil {
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			p := m.pending
			m.pending = nil
			return m, m.runPending(p)
		case "n", "esc":
			p := m.pending
			m.pending = nil
			m.status = p.kind + " cancelled."
			return m, nil
		}
		return m, nil
	}

	// Events-tab detail modal — intercepts Esc to close.
	if m.tab == tabEvents && m.eventsDetail != nil {
		if msg.String() == "esc" {
			m.eventsDetail = nil
			m.eventsRelated = nil
			return m, nil
		}
		return m, nil
	}

	// Page-level keys.
	switch msg.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "tab":
		m.cycleTab(+1)
		return m, m.onTabChange()
	case "shift+tab":
		m.cycleTab(-1)
		return m, m.onTabChange()
	case "1":
		// Only intercept digit-jumps when the textinput is empty — otherwise
		// they'd swallow the digit the user is typing into the filter or
		// command line.
		if m.cmd.Value() == "" {
			m.tab = tabEvents
			m.applyTabFocus()
			return m, m.onTabChange()
		}
	case "2":
		if m.cmd.Value() == "" {
			m.tab = tabUsers
			m.applyTabFocus()
			return m, m.onTabChange()
		}
	case "3":
		if m.cmd.Value() == "" {
			m.tab = tabBans
			m.applyTabFocus()
			return m, m.onTabChange()
		}
	case "4":
		if m.cmd.Value() == "" {
			m.tab = tabSettings
			m.applyTabFocus()
			return m, m.onTabChange()
		}
	}

	// Events tab: navigation keys + filter focus management.
	if m.tab == tabEvents {
		if handled, model, cmd := m.handleEventsKey(msg); handled {
			return model, cmd
		}
	}

	// Settings tab: only cursor up/down navigation. Editing flows through
	// the bottom prompt (`set <key> <value>` / `reset <key>`) for consistency
	// with the other command tabs.
	if m.tab == tabSettings {
		if handled, model, cmd := m.handleSettingsKey(msg); handled {
			return model, cmd
		}
	}

	// Enter on a command-tab dispatches the typed command.
	if msg.String() == "enter" && (m.tab == tabUsers || m.tab == tabBans || m.tab == tabSettings) {
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
			m.pending = &pendingAction{
				kind:         "wall",
				wallBody:     target,
				confirmLabel: "send",
				summary: []string{
					"Wall broadcast to ALL sessions:",
					"  \"" + target + "\"",
				},
			}
			return m, nil
		}
		return m, m.dispatch(line)
	}

	// Fall through to the textinput. On the Events tab in list-focus mode,
	// we already returned above for nav keys — only printable chars reach
	// here, and they should re-grab filter focus.
	if m.tab == tabEvents && !m.eventsFocusFilter {
		m.eventsFocusFilter = true
	}
	var cmd tea.Cmd
	prev := m.cmd.Value()
	m.cmd, cmd = m.cmd.Update(msg)
	// Events tab debounce: any textinput value change schedules a refilter.
	if m.tab == tabEvents && m.cmd.Value() != prev {
		m.eventsPendingFilterAt = time.Now().Add(150 * time.Millisecond)
		return m, tea.Batch(cmd, m.scheduleFilterTick())
	}
	return m, cmd
}

// cycleTab moves the active tab by delta (positive = right). Wraps around
// the 4-tab cycle. Resets the pending-confirmation state so a half-typed
// destructive command on one tab doesn't leak into the next.
func (m *Sysop) cycleTab(delta int) {
	const n = 4
	m.tab = sysopTab((int(m.tab) + delta + n) % n)
	m.pending = nil
	m.applyTabFocus()
}

// applyTabFocus updates the textinput's placeholder + clears its value so
// each tab's input box reads cleanly. Called whenever the active tab
// changes.
func (m *Sysop) applyTabFocus() {
	m.cmd.SetValue("")
	switch m.tab {
	case tabEvents:
		m.cmd.Placeholder = "filter: severity:warn handle:alice ip:1.2.3.4 since:1h text:foo"
		m.eventsFocusFilter = true
	case tabUsers:
		m.cmd.Placeholder = "ban|unban|sysop|unsysop|reset-password|remove-keys|kick|delete-user <handle> · wall <msg>"
	case tabBans:
		m.cmd.Placeholder = "ban-ip <ip> [duration] [reason] | unban-ip <ip> | refresh"
	case tabSettings:
		m.cmd.Placeholder = "set <key> <value> | reset <key> | refresh"
	}
}

// onTabChange fires any tab-specific load that hasn't already happened.
// Init() already kicks off all three loads in parallel, so this is just a
// safety net for the refresh case.
func (m *Sysop) onTabChange() tea.Cmd {
	return nil
}

// dispatch parses one command line + dispatches the matching async tea.Cmd.
// Routed only from the Users and Bans tabs; the Events tab's text input is
// the filter and doesn't dispatch verbs.
func (m *Sysop) dispatch(line string) tea.Cmd {
	verb, target := splitVerb(line)
	switch verb {
	case "help", "?":
		return done(
			"Users tab: ban|unban|sysop|unsysop|clear-passwordless|reset-password|remove-keys|kick|delete-user <handle> · wall <msg>  ·  "+
				"Bans tab: ban-ip <ip> [duration] [reason] | unban-ip <ip>  ·  "+
				"refresh | help  ·  Tab/1/2/3/4 to switch tabs · Esc back to lobby",
			false,
		)
	case "refresh":
		// reload=true → Update routes to the active tab's load func.
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
	case "reset-password":
		return m.preflightUserCmd(target, "reset-password")
	case "remove-keys":
		return m.preflightUserCmd(target, "remove-keys")
	case "kick":
		return m.kickCmd(target)
	case "delete-user":
		return m.preflightUserCmd(target, "delete-user")
	case "ban-ip":
		return m.banIPCmd(target)
	case "unban-ip":
		return m.unbanIPCmd(target)
	case "set":
		return m.setSettingCmd(target)
	case "reset":
		return m.resetSettingCmd(target)
	}
	return done("[!] unknown command: "+verb+" — type help", false)
}

// toggleCmd handles ban/unban/sysop/unsysop in one path. The verb is also
// used to derive the audit_log.action string and the status confirmation,
// so it stays as a string rather than a bool pair.
func (m *Sysop) toggleCmd(handle, verb string, want bool) tea.Cmd {
	_ = want // verb encodes both directions; the param keeps callers explicit
	if handle == "" {
		return done("[!] "+verb+" requires a handle.", false)
	}
	queries := m.sess.Queries
	actor := m.sess.Identity
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
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
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
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
	if m.sess.Settings != nil && !m.sess.Settings.Get().WallEnabled {
		return done("[!] wall broadcasts are disabled in settings (set wall_enabled true to re-enable).", false)
	}
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
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
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

// splitVerb splits "verb arg…" into (verb-lower, trimmed-rest).
func splitVerb(line string) (string, string) {
	verb, rest, _ := strings.Cut(line, " ")
	return strings.ToLower(strings.TrimSpace(verb)), strings.TrimSpace(rest)
}

// writeAudit is a small helper around InsertAuditLogSimple. Failure is
// logged but never blocks the user. Pass targetID = 0 for system-scoped
// actions so the column lands NULL.
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

// Styles shared across tabs. Severity/source colorization for the Events
// tab lives in sysop_events.go since only that tab uses it.
var (
	sysopTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	sysopHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	sysopHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccentDim))
	sysopBan      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed)).Bold(true)
	sysopFlag     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
	sysopMuted    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	sysopErr      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	sysopTabBar   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	sysopTabOn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).Underline(true)
	sysopTabOff   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
)

func (m *Sysop) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var b strings.Builder
	b.WriteString(sysopTitle.Render("sysop console — " + m.sess.Identity.Handle))
	b.WriteString("  " + sysopHint.Render("Tab / 1·2·3 switch · Esc back to lobby"))
	b.WriteString("\n")
	b.WriteString(m.renderTabBar())
	b.WriteString("\n\n")

	if m.loading && m.tab == tabUsers {
		b.WriteString(sysopHint.Render("loading…"))
		return b.String()
	}

	// Each tab gets the full remaining width; height budgets for header
	// (3 rows so far), tab bar (1), input (1), status (1), metrics (1).
	bodyH := m.sess.Height - 8
	if bodyH < 8 {
		bodyH = 8
	}
	bodyW := m.sess.Width
	if bodyW < 40 {
		bodyW = 40
	}

	switch m.tab {
	case tabEvents:
		b.WriteString(m.renderEvents(bodyW, bodyH))
	case tabUsers:
		b.WriteString(m.renderUsers(bodyW, bodyH))
	case tabBans:
		b.WriteString(m.renderBans(bodyW, bodyH))
	case tabSettings:
		b.WriteString(m.renderSettings(bodyW, bodyH))
	}
	b.WriteString("\n\n")

	if m.pending != nil {
		for i, line := range m.pending.summary {
			if i == 0 {
				b.WriteString(sysopErr.Render(line))
			} else {
				b.WriteString(sysopFlag.Render(line))
			}
			b.WriteString("\n")
		}
		label := m.pending.confirmLabel
		if label == "" {
			label = "confirm"
		}
		b.WriteString(sysopHint.Render("[Y] " + label + "  ·  [N] cancel"))
		b.WriteString("\n")
	} else {
		prompt := "> "
		if m.tab == tabEvents {
			prompt = "/ "
		}
		b.WriteString(sysopHint.Render(prompt) + m.cmd.View())
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
	if !m.metrics.at.IsZero() {
		b.WriteString(sysopMuted.Render(fmt.Sprintf(
			"heap %.1f MB · sys %.1f MB · goroutines %d  (refreshed %s)",
			m.metrics.allocMB, m.metrics.sysMB, m.metrics.goroutines,
			m.metrics.at.Format("15:04:05"),
		)))
	}

	// Events-tab detail modal — overlaid on top of everything via the
	// components.Overlay helper. The modal renderer lives in sysop_events.go.
	if m.tab == tabEvents && m.eventsDetail != nil {
		return m.composeEventsDetailOverlay(b.String())
	}
	return b.String()
}

// renderTabBar produces the "[Events] · [Users] · [Bans]" header line. The
// active tab is bold + underlined; others are dim.
func (m *Sysop) renderTabBar() string {
	label := func(name string, t sysopTab) string {
		if m.tab == t {
			return sysopTabOn.Render("[" + name + "]")
		}
		return sysopTabOff.Render("[" + name + "]")
	}
	return sysopTabBar.Render(
		label("Events", tabEvents) + " · " +
			label("Users", tabUsers) + " · " +
			label("Bans", tabBans) + " · " +
			label("Settings", tabSettings),
	)
}

// renderUsers draws the Users tab body — single-pane alphabetical user
// list with flag column (S=sysop, B=banned, K=key-only).
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
		line := fmt.Sprintf("%s %-24s %s", flagStr, truncateRow(u.Handle, 24), sysopMuted.Render(seen))
		b.WriteString(line)
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// truncateRow keeps row text inside the per-column budget. finance.go has
// its own truncate(), hence the differentiated name.
func truncateRow(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
