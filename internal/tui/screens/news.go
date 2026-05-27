package screens

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/news"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// News is the multi-source news list + inline article reader. Each registered
// source gets its own tab; switching tabs lazily fetches that source the
// first time the user opens it. Enter on a story delegates to the shared
// ArticleReader component, which handles extract + scroll + render.
type News struct {
	sess *session.Session

	mode newsMode

	// Per-source state, parallel to sess.News.Sources(). One slot per source
	// so switching tabs preserves cursor + cached stories without re-fetching.
	sources   []sourceState
	sourceIdx int

	// Reader-mode delegate. Owns the loaded article + scroll position.
	reader *components.ArticleReader
}

// sourceState is the per-tab list state. `loaded` distinguishes
// "fetched and empty" from "never tried" so the tab can lazy-fetch on
// first visit.
type sourceState struct {
	stories []news.Story
	cursor  int
	loading bool
	loaded  bool
	err     string
}

type newsMode int

const (
	modeNewsList newsMode = iota
	modeNewsReader
)

const newsLimit = 30

func NewNews(sess *session.Session) tea.Model {
	m := &News{sess: sess, reader: components.NewArticleReader()}
	if reg := sess.News; reg != nil {
		m.sources = make([]sourceState, reg.Len())
		// Land on the user's preferred source when it matches a registered
		// id; otherwise default to the first source.
		if pref := sess.DisplayPrefs.PreferredNewsSource; pref != "" {
			for i, s := range reg.Sources() {
				if s.ID == pref {
					m.sourceIdx = i
					break
				}
			}
		}
	}
	return m
}

type newsLoadedMsg struct {
	sourceIdx int
	stories   []news.Story
	err       error
}

// prefPersistedMsg is the no-op envelope returned by the async preference
// writer. We don't surface it in the UI; the cmd just needs to return a
// tea.Msg so bubbletea is happy.
type prefPersistedMsg struct{}

func (m *News) Init() tea.Cmd { return m.fetchActive() }

// fetchActive kicks off a fetch for the currently active source. No-op when
// the source slot is already loading or already loaded (call refreshActive
// for a forced refetch).
func (m *News) fetchActive() tea.Cmd {
	if m.sess.News == nil || m.sess.News.Len() == 0 {
		return nil
	}
	idx := m.sourceIdx
	src, ok := m.sourceAt(idx)
	if !ok {
		return nil
	}
	st := &m.sources[idx]
	if st.loading || st.loaded {
		return nil
	}
	st.loading = true
	st.err = ""
	provider := src.Provider
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(12 * time.Second)
		defer cancel()
		stories, err := provider.TopStories(ctx, newsLimit)
		return newsLoadedMsg{sourceIdx: idx, stories: stories, err: err}
	}
}

// refreshActive forces a re-fetch of the active source, clearing its loaded
// flag so fetchActive will run.
func (m *News) refreshActive() tea.Cmd {
	if m.sess.News == nil || m.sourceIdx >= len(m.sources) {
		return nil
	}
	st := &m.sources[m.sourceIdx]
	if st.loading {
		return nil
	}
	st.loaded = false
	st.err = ""
	return m.fetchActive()
}

func (m *News) sourceAt(i int) (news.Source, bool) {
	if m.sess.News == nil {
		return news.Source{}, false
	}
	srcs := m.sess.News.Sources()
	if i < 0 || i >= len(srcs) {
		return news.Source{}, false
	}
	return srcs[i], true
}

func (m *News) activeState() *sourceState {
	if m.sourceIdx >= 0 && m.sourceIdx < len(m.sources) {
		return &m.sources[m.sourceIdx]
	}
	return nil
}

// switchSource selects target source by ordinal. No-op when target is out of
// range or already active. On switch, kicks off the target's first fetch if
// needed and persists the new preference to the user's row.
func (m *News) switchSource(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.sources) || idx == m.sourceIdx {
		return nil
	}
	m.sourceIdx = idx
	src, _ := m.sourceAt(idx)
	cmds := []tea.Cmd{m.fetchActive(), m.persistPreference(src.ID)}
	return tea.Batch(cmds...)
}

// persistPreference fires an async UPDATE of users.preferred_news_source. UI
// stays silent on success or failure — humans don't smash Tab fast enough to
// need debouncing, and a transient DB blip should never wedge the screen.
// Also mirrors the new value into the in-memory DisplayPrefs so a return-to-
// lobby-then-back round-trip remembers the choice without a re-read.
func (m *News) persistPreference(sourceID string) tea.Cmd {
	m.sess.DisplayPrefs.PreferredNewsSource = sourceID
	if m.sess.Queries == nil || m.sess.Identity.UserID == 0 {
		return nil
	}
	q := m.sess.Queries
	uid := m.sess.Identity.UserID
	logger := m.sess.Logger
	pref := &sourceID
	if sourceID == "" {
		pref = nil
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if err := q.SetUserPreferredNewsSource(ctx, gen.SetUserPreferredNewsSourceParams{
			ID:                  uid,
			PreferredNewsSource: pref,
		}); err != nil && logger != nil {
			logger.Warn("news: persist preferred source failed",
				slog.Int64("user_id", uid),
				slog.String("source", sourceID),
				slog.Any("err", err))
		}
		return prefPersistedMsg{}
	}
}

func (m *News) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case newsLoadedMsg:
		if msg.sourceIdx >= 0 && msg.sourceIdx < len(m.sources) {
			st := &m.sources[msg.sourceIdx]
			st.loading = false
			st.loaded = true
			if msg.err != nil {
				st.err = msg.err.Error()
				return m, nil
			}
			st.stories = msg.stories
			if st.cursor >= len(st.stories) {
				st.cursor = 0
			}
		}

	case components.ArticleReaderLoadedMsg:
		m.reader.Loaded(msg)

	case prefPersistedMsg:
		// Nothing to render — the persist cmd just needed a Msg to land.

	case tea.KeyMsg:
		if m.mode == modeNewsReader {
			return m.handleReaderKey(msg)
		}
		return m.handleListKey(msg)
	}
	return m, nil
}

func (m *News) handleListKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := m.activeState()
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "tab":
		if len(m.sources) > 1 {
			return m, m.switchSource((m.sourceIdx + 1) % len(m.sources))
		}
	case "shift+tab":
		if len(m.sources) > 1 {
			return m, m.switchSource((m.sourceIdx - 1 + len(m.sources)) % len(m.sources))
		}
	case "up", "k":
		if st != nil && st.cursor > 0 {
			st.cursor--
		}
	case "down", "j":
		if st != nil && st.cursor < len(st.stories)-1 {
			st.cursor++
		}
	case "r":
		return m, m.refreshActive()
	case "enter":
		if st != nil && !st.loading && len(st.stories) > 0 {
			return m, m.extractSelected()
		}
	default:
		// Numeric hotkeys jump directly to a source. The lookup walks the
		// source list rather than parsing the key as an index so it stays
		// in sync with whatever Hotkey each Source declares.
		if r := []rune(k.String()); len(r) == 1 && len(m.sources) > 1 {
			for i, s := range m.sess.News.Sources() {
				if s.Hotkey == r[0] {
					return m, m.switchSource(i)
				}
			}
		}
	}
	return m, nil
}

// extractSelected hands the cursor-selected story's URL to the reader
// component. Every Story.URL is guaranteed populated by the provider (HN
// substitutes the item page, Lobsters the comments page), so no per-source
// fallback is needed here.
func (m *News) extractSelected() tea.Cmd {
	st := m.activeState()
	if st == nil || st.cursor >= len(st.stories) {
		return nil
	}
	target := st.stories[st.cursor].URL
	if target == "" {
		return nil
	}
	m.mode = modeNewsReader
	return m.reader.Load(m.sess.Ctx(), target)
}

func (m *News) handleReaderKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.reader.Update(k) {
		return m, nil
	}
	if k.String() == "esc" {
		m.mode = modeNewsList
		m.reader.Clear()
	}
	return m, nil
}

var (
	newsTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	newsHint       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	newsScore      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
	newsAuthor     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	newsHost       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	newsActiveRow  = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(theme.ColorSurfaceAlt))
	newsKidsCount  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	newsErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	newsTabBar     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	newsTabOn      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).Underline(true)
	newsTabOff     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
)

func (m *News) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	if m.mode == modeNewsReader {
		return m.viewReader()
	}
	return m.viewList()
}

// renderSourceTabs draws the "[HN] · Lobsters" tab bar. Returns an empty
// string when fewer than two sources are registered — no point eating a
// header line for a degenerate case.
func (m *News) renderSourceTabs() string {
	srcs := m.sess.News.Sources()
	if len(srcs) < 2 {
		return ""
	}
	parts := make([]string, 0, len(srcs))
	for i, s := range srcs {
		if i == m.sourceIdx {
			parts = append(parts, newsTabOn.Render("["+s.DisplayName+"]"))
		} else {
			parts = append(parts, newsTabOff.Render(s.DisplayName))
		}
	}
	return newsTabBar.Render(strings.Join(parts, " · "))
}

func (m *News) viewList() string {
	var b strings.Builder

	if m.sess.News == nil || m.sess.News.Len() == 0 {
		b.WriteString(newsTitleStyle.Render("News"))
		b.WriteString("\n\n")
		b.WriteString(newsErrStyle.Render("! no news sources configured"))
		return b.String()
	}

	src, _ := m.sourceAt(m.sourceIdx)
	st := m.activeState()

	b.WriteString(newsTitleStyle.Render("News"))
	if tabs := m.renderSourceTabs(); tabs != "" {
		b.WriteString("   ")
		b.WriteString(tabs)
	}
	b.WriteString("\n")

	hint := "↑/↓ select · Enter open · r refresh · Esc back"
	if len(m.sess.News.Sources()) > 1 {
		hint = "Tab switch · 1/2 jump · " + hint
	}
	b.WriteString(newsHint.Render(hint))
	b.WriteString("\n\n")

	switch {
	case st == nil:
		b.WriteString(newsErrStyle.Render("! source unavailable"))
		return b.String()
	case st.loading:
		b.WriteString(newsHint.Render("fetching from " + src.Host + "…"))
		return b.String()
	case st.err != "":
		b.WriteString(newsErrStyle.Render("! " + st.err))
		b.WriteString("\n\n")
		b.WriteString(newsHint.Render("press r to retry"))
		return b.String()
	case len(st.stories) == 0:
		b.WriteString(newsHint.Render("no stories returned — try r to refresh"))
		return b.String()
	}

	width := m.sess.Width - 2
	if width < 40 {
		width = 40
	}
	metaW := 28
	titleW := width - 5 - 5 - metaW - 3
	if titleW < 20 {
		titleW = 20
	}

	visibleRows := m.sess.Height - 6
	if visibleRows < 3 {
		visibleRows = 3
	}
	start := 0
	if st.cursor >= visibleRows {
		start = st.cursor - visibleRows + 1
	}
	end := start + visibleRows
	if end > len(st.stories) {
		end = len(st.stories)
	}

	for i := start; i < end; i++ {
		s := st.stories[i]
		rank := fmt.Sprintf("%2d.", i+1)
		score := newsScore.Render(fmt.Sprintf("%4d", s.Score))
		title := runewidth.Truncate(s.Title, titleW, "…")
		host := s.Host()
		if host == "" {
			host = "(self)"
		}
		meta := fmt.Sprintf(" %s · %s · %s",
			newsAuthor.Render("@"+s.Author),
			newsHost.Render(host),
			newsKidsCount.Render(fmt.Sprintf("%d comments", s.KidsCnt)))
		row := fmt.Sprintf("%s  %s  %s%s", rank, score, title, meta)
		if i == st.cursor {
			b.WriteString("▸ " + newsActiveRow.Render(row))
		} else {
			b.WriteString("  " + row)
		}
		b.WriteString("\n")
	}
	if end < len(st.stories) {
		b.WriteString(newsHint.Render(fmt.Sprintf("  … +%d more below", len(st.stories)-end)))
		b.WriteString("\n")
	} else if start > 0 {
		b.WriteString(newsHint.Render(fmt.Sprintf("  %d shown", len(st.stories))))
		b.WriteString("\n")
	}

	if st.cursor < len(st.stories) {
		s := st.stories[st.cursor]
		b.WriteString("\n")
		b.WriteString(newsHint.Render("link: ") + newsHost.Render(s.URL))
	}
	return b.String()
}

func (m *News) viewReader() string {
	return m.reader.View(m.sess.Width, m.sess.Height, "News › Reader")
}
