package screens

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/carbonyl"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// recentHistoryLimit is the row count surfaced on the Web screen's Recent
// region. Larger than the visible window so the user can scroll, smaller than
// the per-user cap so the query stays cheap.
const recentHistoryLimit = 25

// focusRegion is the currently keyboard-focused area within the Web screen's
// browse mode. Tab cycles through these in order.
type focusRegion int

const (
	focusURL focusRegion = iota
	focusBookmarks
	focusHistory
)

// Web is the Carbonyl-backed full-browser screen. Distinct from Browser
// (reader-mode) — picked from the lobby's "Web" carousel item, which only
// appears when the session can host Carbonyl (SSH + binary present + kill
// switch on). The screen lays out three stacked regions: URL input,
// Bookmarks, Recent. Tab cycles focus; Enter on the focused region launches
// the URL the region "points at".
type Web struct {
	sess  *session.Session
	input textinput.Model

	// Lists loaded lazily from Postgres at screen entry and reloaded after
	// any mutation. nil-vs-empty distinction is intentional: nil = not yet
	// loaded (show "loading…"), empty slice = loaded but empty.
	bookmarks []gen.ListWebBookmarksRow
	history   []gen.RecentWebHistoryRow

	// Focused region within browse mode. Carbonyl launch ignores this and
	// owns the terminal until it exits.
	focus    focusRegion
	bmCursor int
	hsCursor int
	lastURL  string // populated on successful exit; powers "Ctrl+B to bookmark"

	// Inline editor for bookmark add/rename. Non-nil while editing.
	editor *bookmarkEditor

	// Confirm modal for destructive ops (delete bookmark, clear history).
	confirm     *components.Confirm
	confirmKind string

	// resize chan owned per-launch; nil when not running. WindowSizeMsg
	// forwards into it while richModeActive.
	richModeActive bool
	richResize     chan carbonyl.WinSize

	// status shown above the URL row — usually an error from the last
	// launch or a success ack. Cleared on next user input.
	status string
}

// Result messages for async operations. Each one is consumed in Update and
// drives a reload (or a status-line message in the visit-recording case,
// which is silent on success).

type webExitedMsg struct {
	url string
	err error
}

type bookmarksLoadedMsg struct {
	rows []gen.ListWebBookmarksRow
	err  error
}

type historyLoadedMsg struct {
	rows []gen.RecentWebHistoryRow
	err  error
}

type bookmarkSavedMsg struct {
	err error
}

type bookmarkDeletedMsg struct {
	err error
}

type historyDeletedMsg struct {
	err error
}

type historyClearedMsg struct {
	err error
}

func NewWeb(sess *session.Session) tea.Model {
	in := textinput.New()
	in.Placeholder = "https://example.com"
	in.CharLimit = 2048
	in.Focus()
	return &Web{sess: sess, input: in}
}

func (m *Web) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.reloadBookmarks(), m.reloadHistory())
}

func (m *Web) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case webExitedMsg:
		m.richModeActive = false
		if m.richResize != nil {
			close(m.richResize)
			m.richResize = nil
		}
		m.input.Focus()
		if msg.err != nil {
			text := msg.err.Error()
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[:i]
			}
			m.status = "! " + text
		} else {
			m.status = "exited " + msg.url + "  (Ctrl+B to bookmark)"
			m.lastURL = msg.url
		}
		// Re-fetch history so the just-visited row jumps to the top.
		return m, tea.Batch(textinput.Blink, m.reloadHistory())

	case bookmarksLoadedMsg:
		if msg.err != nil {
			// Mark the region loaded (non-nil) so it stops showing "loading…",
			// and surface the failure instead of swallowing it.
			if m.bookmarks == nil {
				m.bookmarks = []gen.ListWebBookmarksRow{}
			}
			m.status = "! bookmarks load failed: " + msg.err.Error()
			return m, nil
		}
		// sqlc returns a nil slice for zero rows, which is indistinguishable
		// from the not-yet-loaded sentinel. Coerce to a non-nil empty slice so
		// "loaded but empty" renders "(empty …)" rather than "loading…" forever.
		m.bookmarks = msg.rows
		if m.bookmarks == nil {
			m.bookmarks = []gen.ListWebBookmarksRow{}
		}
		if m.bmCursor >= len(m.bookmarks) {
			m.bmCursor = max0(len(m.bookmarks) - 1)
		}
		return m, nil

	case historyLoadedMsg:
		if msg.err != nil {
			if m.history == nil {
				m.history = []gen.RecentWebHistoryRow{}
			}
			m.status = "! history load failed: " + msg.err.Error()
			return m, nil
		}
		m.history = msg.rows
		if m.history == nil {
			m.history = []gen.RecentWebHistoryRow{}
		}
		if m.hsCursor >= len(m.history) {
			m.hsCursor = max0(len(m.history) - 1)
		}
		return m, nil

	case bookmarkSavedMsg:
		m.editor = nil
		if m.focus == focusURL {
			m.input.Focus()
		}
		if msg.err != nil {
			m.status = "! save failed: " + msg.err.Error()
			return m, nil
		}
		m.status = "bookmark saved"
		return m, m.reloadBookmarks()

	case bookmarkDeletedMsg:
		if msg.err != nil {
			m.status = "! delete failed: " + msg.err.Error()
			return m, nil
		}
		m.status = "bookmark removed"
		return m, m.reloadBookmarks()

	case historyDeletedMsg:
		if msg.err != nil {
			m.status = "! delete failed: " + msg.err.Error()
			return m, nil
		}
		return m, m.reloadHistory()

	case historyClearedMsg:
		if msg.err != nil {
			m.status = "! clear failed: " + msg.err.Error()
			return m, nil
		}
		m.status = "history cleared"
		m.hsCursor = 0
		return m, m.reloadHistory()

	case tea.WindowSizeMsg:
		if m.richModeActive && m.richResize != nil {
			select {
			case m.richResize <- carbonyl.WinSize{Rows: msg.Height, Cols: msg.Width}:
			default:
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.richModeActive {
			// Drop key events while Carbonyl owns the terminal — defensive.
			return m, nil
		}
		// Confirm modal takes precedence: route keys to it until resolved.
		if m.confirm != nil {
			m.confirm.Update(msg)
			if m.confirm.Submitted {
				cmd := m.applyConfirm()
				m.confirm = nil
				m.confirmKind = ""
				return m, cmd
			}
			if m.confirm.Cancelled {
				m.confirm = nil
				m.confirmKind = ""
			}
			return m, nil
		}
		// Inline editor takes precedence over normal navigation.
		if m.editor != nil {
			return m.handleEditorKey(msg)
		}
		return m.handleBrowseKey(msg)
	}

	// Non-key updates while in browse mode (e.g. cursor blink) feed the
	// URL textinput only when it has focus.
	if !m.richModeActive && m.editor == nil && m.focus == focusURL {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	if m.editor != nil {
		var cmd tea.Cmd
		m.editor.title, cmd = m.editor.title.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleBrowseKey dispatches a keypress to the focused region. URL-row keys
// (typing into the textinput) take priority when focus is on URL; the
// per-region handlers in web_lists.go only see keys that match their bindings.
func (m *Web) handleBrowseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys, work from any focus.
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "tab":
		m.cycleFocus(+1)
		return m, nil
	case "shift+tab":
		m.cycleFocus(-1)
		return m, nil
	case "ctrl+b":
		// Quick-bookmark from the URL textinput's current contents (or the
		// last-visited URL if the input is empty).
		target := strings.TrimSpace(m.input.Value())
		if target == "" {
			target = m.lastURL
		}
		if target == "" {
			m.status = "type a URL first, or visit one to bookmark"
			return m, nil
		}
		if !strings.Contains(target, "://") {
			target = "https://" + target
		}
		m.openEditorForAdd(target)
		return m, nil
	}

	switch m.focus {
	case focusURL:
		switch k.String() {
		case "enter":
			return m, m.launch(strings.TrimSpace(m.input.Value()))
		case "down":
			// Quick jump into lists if the user instinctively presses down.
			if len(m.bookmarks) > 0 {
				m.focus = focusBookmarks
			} else if len(m.history) > 0 {
				m.focus = focusHistory
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		// Typing into the URL clears stale status (but keep launch errors
		// visible — they start with "!").
		if m.status != "" && !strings.HasPrefix(m.status, "!") {
			m.status = ""
		}
		return m, cmd

	case focusBookmarks:
		return m.handleBookmarksKey(k)
	case focusHistory:
		return m.handleHistoryKey(k)
	}
	return m, nil
}

// cycleFocus advances the focused region by dir (+1 / -1), skipping empty
// list regions so Tab doesn't land on a useless empty pane.
func (m *Web) cycleFocus(dir int) {
	order := []focusRegion{focusURL, focusBookmarks, focusHistory}
	idx := 0
	for i, r := range order {
		if r == m.focus {
			idx = i
			break
		}
	}
	for try := 0; try < len(order); try++ {
		idx = (idx + dir + len(order)) % len(order)
		next := order[idx]
		if next == focusURL {
			m.input.Focus()
			return
		}
		if next == focusBookmarks && len(m.bookmarks) > 0 {
			m.focus = next
			m.input.Blur()
			return
		}
		if next == focusHistory && len(m.history) > 0 {
			m.focus = next
			m.input.Blur()
			return
		}
	}
	// Fell through — no non-empty list, just stay on URL.
	m.focus = focusURL
	m.input.Focus()
}

// applyConfirm runs the action queued behind a confirm-Yes. Kinds:
//   - "deleteBookmark:<id>"
//   - "clearHistory"
func (m *Web) applyConfirm() tea.Cmd {
	if m.confirm == nil || m.confirm.Choice != 1 {
		return nil
	}
	switch {
	case strings.HasPrefix(m.confirmKind, "deleteBookmark:"):
		var id int64
		_, _ = fmt.Sscanf(m.confirmKind, "deleteBookmark:%d", &id)
		return m.deleteBookmark(id)
	case m.confirmKind == "clearHistory":
		return m.clearHistory()
	}
	return nil
}

// launch validates the URL and starts Carbonyl. Mirrors the original web.go
// path — the only addition is that on a successful exit we record the visit.
func (m *Web) launch(raw string) tea.Cmd {
	if raw == "" {
		m.status = "type a URL first"
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if err := carbonyl.ValidateURL(raw); err != nil {
		m.status = "! " + err.Error()
		return nil
	}
	if !m.sess.IsSSH {
		m.status = "rich mode requires SSH (web terminal can't host it)"
		return nil
	}
	if m.sess.Settings == nil || !m.sess.Settings.Get().CarbonylEnabled {
		m.status = "rich mode is disabled (set carbonyl_enabled=true in the sysop tab)"
		return nil
	}
	if m.sess.LaunchCarbonyl == nil {
		m.status = "rich mode unavailable: carbonyl binary missing on the server"
		return nil
	}
	if m.sess.TeaProgram == nil {
		m.status = "internal: tea program reference missing"
		return nil
	}

	m.richResize = make(chan carbonyl.WinSize, 4)
	m.richModeActive = true
	m.status = ""

	target := raw
	prog := m.sess.TeaProgram
	launch := m.sess.LaunchCarbonyl
	ctx := m.sess.Ctx()
	cols := m.sess.Width
	rows := m.sess.Height
	uid := m.sess.Identity.UserID
	handle := m.sess.Identity.Handle
	resizeCh := m.richResize
	queries := m.sess.Queries

	return func() tea.Msg {
		if err := prog.ReleaseTerminal(); err != nil {
			return webExitedMsg{url: target, err: fmt.Errorf("release terminal: %w", err)}
		}
		req := carbonyl.LaunchRequest{
			URL:         target,
			UserID:      uid,
			Handle:      handle,
			InitialCols: cols,
			InitialRows: rows,
		}
		launchErr := launch(ctx, req, resizeCh)
		restoreErr := prog.RestoreTerminal()
		if prog != nil {
			prog.Printf("\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1006l")
		}
		// Best-effort visit recording. Detached from the screen's ctx
		// (which dies with the SSH session) so a disconnect mid-write
		// doesn't lose the row — but bounded by its own short timeout
		// so a stuck pool can't pin the goroutine.
		if launchErr == nil && queries != nil && uid > 0 {
			recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := queries.RecordWebVisit(recordCtx, gen.RecordWebVisitParams{UserID: uid, Url: target}); err != nil {
				// Visit recording failures are off-band. We log nothing
				// here because the slog default at this layer isn't
				// piped to the SSH terminal anyway; the server-side log
				// already captured the exit line.
				_ = err
			}
		}
		if launchErr != nil {
			return webExitedMsg{url: target, err: launchErr}
		}
		if restoreErr != nil {
			return webExitedMsg{url: target, err: fmt.Errorf("restore terminal: %w", restoreErr)}
		}
		return webExitedMsg{url: target}
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	webTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	webHint   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	webPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	webErr    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	webNote   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
	webDim    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	webHead   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	webRowOn  = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(theme.ColorSurfaceAlt)).Foreground(lipgloss.Color(theme.ColorYellow))
)

func (m *Web) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.richModeActive {
		return webHint.Render("launching full browser…")
	}
	if m.confirm != nil {
		// Render the confirm modal centered over a dimmed body. Keep the
		// frame minimal; the modal itself carries the prompt.
		w := m.sess.Width - 12
		if w > 60 {
			w = 60
		}
		if w < 30 {
			w = 30
		}
		return m.confirm.View(w)
	}

	var b strings.Builder
	b.WriteString(webTitle.Render("Web"))
	b.WriteString("  ")
	b.WriteString(webHint.Render("full Chromium-based browser"))
	b.WriteString("\n\n")

	if m.status != "" {
		style := webNote
		if strings.HasPrefix(m.status, "!") {
			style = webErr
		}
		b.WriteString(style.Render(m.status))
		b.WriteString("\n\n")
	}

	// URL row.
	urlArrow := "  "
	if m.focus == focusURL {
		urlArrow = webNote.Render("▸ ")
	}
	b.WriteString(urlArrow)
	b.WriteString(webPrompt.Render("URL"))
	b.WriteString("  ")
	b.WriteString(m.input.View())
	b.WriteString("\n     ")
	b.WriteString(webHint.Render("Enter load · Ctrl+B bookmark · Tab next pane · Esc lobby"))
	b.WriteString("\n\n")

	// Editor takes the rest of the body when active; lists stay collapsed
	// behind it so the user's eye doesn't have two places to land.
	if m.editor != nil {
		b.WriteString(m.renderEditor())
		return b.String()
	}

	// Bookmarks region.
	b.WriteString(m.renderBookmarks())
	b.WriteString("\n")

	// Recent region.
	b.WriteString(m.renderHistory())
	b.WriteString("\n")

	// Footer reminders. Only show the in-browser hints when there's room —
	// short terminals truncate; this is the lowest-priority text.
	b.WriteString(webHint.Render("While in the browser:"))
	b.WriteString("\n  ")
	b.WriteString(webHint.Render("Esc, Q   exit Carbonyl"))
	b.WriteString("\n  ")
	b.WriteString(webHint.Render("Ctrl+\\   emergency exit"))
	return b.String()
}

// defaultBookmarkTitle returns the host name as the suggested title for a
// fresh bookmark. Stripped "www." prefix since it's noise in a short list.
func defaultBookmarkTitle(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Host
	host = strings.TrimPrefix(host, "www.")
	return host
}
