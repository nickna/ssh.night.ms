package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/providers/news"
	"github.com/nickna/ssh.night.ms/internal/reader"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// News is the HackerNews-backed news list + inline article reader. Enter
// on a story with a URL fires reader.Extract; the screen switches to
// reader rendering on success.
type News struct {
	sess *session.Session

	mode newsMode

	// list state
	stories []news.Story
	cursor  int
	loading bool
	err     string

	// reader state
	article       *reader.Article
	readerScroll  int
	readerLoading bool
	readerErr     string
}

type newsMode int

const (
	modeNewsList newsMode = iota
	modeNewsReader
)

const newsLimit = 30

func NewNews(sess *session.Session) tea.Model { return &News{sess: sess, loading: true} }

type newsLoadedMsg struct {
	stories []news.Story
	err     error
}

type readerLoadedMsg struct {
	article *reader.Article
	err     error
}

func (m *News) Init() tea.Cmd { return m.fetch() }

func (m *News) fetch() tea.Cmd {
	if m.sess.News == nil {
		return func() tea.Msg {
			return newsLoadedMsg{err: fmt.Errorf("news provider not configured")}
		}
	}
	provider := m.sess.News
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(12*time.Second)
		defer cancel()
		stories, err := provider.TopStories(ctx, newsLimit)
		return newsLoadedMsg{stories: stories, err: err}
	}
}

// extractSelected fires reader.Extract for the cursor-selected story. URL-less
// items (Ask HN, internal discussions) link to the HN item page so the reader
// at least surfaces the comment thread title — though most won't extract a
// useful body.
func (m *News) extractSelected() tea.Cmd {
	if m.cursor >= len(m.stories) {
		return nil
	}
	s := m.stories[m.cursor]
	target := s.URL
	if target == "" {
		target = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", s.ID)
	}
	m.readerLoading = true
	m.readerErr = ""
	m.article = nil
	m.readerScroll = 0
	m.mode = modeNewsReader
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(20*time.Second)
		defer cancel()
		article, err := reader.Extract(ctx, target, 15*time.Second)
		if err != nil {
			return readerLoadedMsg{err: err}
		}
		return readerLoadedMsg{article: &article}
	}
}

func (m *News) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case newsLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.stories = msg.stories
		if m.cursor >= len(m.stories) {
			m.cursor = 0
		}

	case readerLoadedMsg:
		m.readerLoading = false
		if msg.err != nil {
			m.readerErr = msg.err.Error()
			return m, nil
		}
		m.article = msg.article
		m.readerScroll = 0

	case tea.KeyMsg:
		if m.mode == modeNewsReader {
			return m.handleReaderKey(msg)
		}
		return m.handleListKey(msg)
	}
	return m, nil
}

func (m *News) handleListKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.stories)-1 {
			m.cursor++
		}
	case "r":
		if !m.loading {
			m.loading = true
			m.err = ""
			return m, m.fetch()
		}
	case "enter":
		if !m.loading && len(m.stories) > 0 {
			return m, m.extractSelected()
		}
	}
	return m, nil
}

func (m *News) handleReaderKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNewsList
		m.article = nil
		m.readerErr = ""
		m.readerScroll = 0
	case "up", "k":
		if m.readerScroll > 0 {
			m.readerScroll--
		}
	case "down", "j":
		m.readerScroll++ // clamp in View() against rendered line count
	case "pgup":
		m.readerScroll -= 10
		if m.readerScroll < 0 {
			m.readerScroll = 0
		}
	case "pgdown":
		m.readerScroll += 10
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
	readerHeading  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	readerByline   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	readerBody     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	readerQuote    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim)).Italic(true)
	readerCode     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Background(lipgloss.Color(theme.ColorSurfaceAlt))
	readerListItem = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
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

func (m *News) viewList() string {
	var b strings.Builder
	b.WriteString(newsTitleStyle.Render("News") + "  " + newsHint.Render("hacker news top stories"))
	b.WriteString("\n")
	b.WriteString(newsHint.Render("↑/↓ select · Enter open · r refresh · Esc back"))
	b.WriteString("\n\n")

	switch {
	case m.loading:
		b.WriteString(newsHint.Render("fetching from news.ycombinator.com…"))
		return b.String()
	case m.err != "":
		b.WriteString(newsErrStyle.Render("! " + m.err))
		b.WriteString("\n\n")
		b.WriteString(newsHint.Render("press r to retry"))
		return b.String()
	case len(m.stories) == 0:
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
	if m.cursor >= visibleRows {
		start = m.cursor - visibleRows + 1
	}
	end := start + visibleRows
	if end > len(m.stories) {
		end = len(m.stories)
	}

	for i := start; i < end; i++ {
		s := m.stories[i]
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
		if i == m.cursor {
			b.WriteString("▸ " + newsActiveRow.Render(row))
		} else {
			b.WriteString("  " + row)
		}
		b.WriteString("\n")
	}
	if end < len(m.stories) {
		b.WriteString(newsHint.Render(fmt.Sprintf("  … +%d more below", len(m.stories)-end)))
		b.WriteString("\n")
	} else if start > 0 {
		b.WriteString(newsHint.Render(fmt.Sprintf("  %d shown", len(m.stories))))
		b.WriteString("\n")
	}

	if m.cursor < len(m.stories) {
		s := m.stories[m.cursor]
		url := s.URL
		if url == "" {
			url = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", s.ID)
		}
		b.WriteString("\n")
		b.WriteString(newsHint.Render("link: ") + newsHost.Render(url))
	}
	return b.String()
}

func (m *News) viewReader() string {
	var b strings.Builder
	b.WriteString(newsTitleStyle.Render("News › Reader") + "  " + newsHint.Render("Esc back to list · ↑/↓ PgUp/PgDn scroll"))
	b.WriteString("\n\n")

	switch {
	case m.readerLoading:
		b.WriteString(newsHint.Render("fetching + extracting article…"))
		return b.String()
	case m.readerErr != "":
		b.WriteString(newsErrStyle.Render("! " + m.readerErr))
		b.WriteString("\n\n")
		b.WriteString(newsHint.Render("press Esc to return to the list"))
		return b.String()
	case m.article == nil:
		b.WriteString(newsHint.Render("no article loaded"))
		return b.String()
	}

	// Lay out the article as a single line stream: title, byline+host, blank,
	// then paragraphs separated by blank lines. Each paragraph word-wraps to
	// the viewport width.
	width := m.sess.Width - 4
	if width < 40 {
		width = 40
	}
	maxLineW := width
	if maxLineW > 90 {
		// Articles read better narrower; cap at ~90 cells like the .NET
		// RichArticleView does.
		maxLineW = 90
	}

	var lines []string
	for _, line := range wrapToWidth(m.article.Title, maxLineW) {
		lines = append(lines, readerHeading.Render(line))
	}
	meta := ""
	if m.article.Byline != "" {
		meta = m.article.Byline
	}
	if m.article.Host != "" {
		if meta != "" {
			meta += " · "
		}
		meta += m.article.Host
	}
	if meta != "" {
		for _, line := range wrapToWidth(meta, maxLineW) {
			lines = append(lines, readerByline.Render(line))
		}
	}
	lines = append(lines, "")

	for _, block := range m.article.Blocks {
		switch block.Kind {
		case reader.BlockHeading:
			for _, l := range wrapToWidth(block.Text, maxLineW) {
				lines = append(lines, readerHeading.Render(l))
			}
		case reader.BlockQuote:
			// Mirror the .NET project's blockquote prefix — a vertical bar at
			// the left edge so the eye picks the quote out of the surrounding
			// paragraphs at a glance.
			for _, l := range wrapToWidth(block.Text, maxLineW-2) {
				lines = append(lines, readerQuote.Render("│ "+l))
			}
		case reader.BlockCode:
			// Preserve <pre>'s internal newlines verbatim. Long code lines
			// wrap visually (no horizontal scroll) but the wrap goes through
			// wrapToWidth to keep the right margin clean.
			for _, raw := range strings.Split(block.Text, "\n") {
				if raw == "" {
					lines = append(lines, readerCode.Render(strings.Repeat(" ", maxLineW)))
					continue
				}
				for _, l := range wrapToWidth(raw, maxLineW-2) {
					padded := "  " + l
					if extra := maxLineW - len([]rune(padded)); extra > 0 {
						padded += strings.Repeat(" ", extra)
					}
					lines = append(lines, readerCode.Render(padded))
				}
			}
		case reader.BlockList:
			// Each list item came in already prefixed with "• " or "1. " by
			// the HTML walker; split + wrap each one independently so long
			// items keep their bullet hanging indent.
			for _, item := range strings.Split(block.Text, "\n") {
				wrapped := wrapToWidth(item, maxLineW)
				if len(wrapped) == 0 {
					continue
				}
				lines = append(lines, readerListItem.Render(wrapped[0]))
				for _, cont := range wrapped[1:] {
					lines = append(lines, readerListItem.Render("  "+cont))
				}
			}
		case reader.BlockImage:
			// News doesn't fetch images; skip silently so the alt text
			// doesn't leak as a stray paragraph.
			continue
		default: // paragraph + anything unrecognized
			for _, l := range wrapToWidth(block.Text, maxLineW) {
				lines = append(lines, readerBody.Render(l))
			}
		}
		lines = append(lines, "")
	}

	availH := m.sess.Height - 3
	if availH < 1 {
		availH = 1
	}
	maxScroll := len(lines) - availH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.readerScroll > maxScroll {
		m.readerScroll = maxScroll
	}
	end := m.readerScroll + availH
	if end > len(lines) {
		end = len(lines)
	}
	for _, ln := range lines[m.readerScroll:end] {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if end < len(lines) {
		b.WriteString(newsHint.Render(fmt.Sprintf("  … %d more lines below", len(lines)-end)))
	}
	return b.String()
}
