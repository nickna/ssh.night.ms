// ArticleReader is the reusable reader-mode widget shared by the News and
// Finance screens. It owns the loaded article, scroll position, and rendering;
// the host screen only decides when to launch a load and where Esc should
// navigate back to.
package components

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/reader"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Timeouts for the extract pipeline. The 20s outer cap is what the cmd's
// context lives under; the 15s inner cap is what reader.Extract gives the
// per-fetch HTTP call. The 5s gap lets a slow HTML parse still finish.
const (
	articleReaderFetchTimeout = 15 * time.Second
	articleReaderCmdTimeout   = 20 * time.Second
)

// ArticleReaderLoadedMsg is the bubbletea message published by a Load cmd.
// Host screens forward it back to (*ArticleReader).Loaded from their Update.
type ArticleReaderLoadedMsg struct {
	Article *reader.Article
	Err     error
}

// ArticleReader holds per-screen reader state: the loaded article, the
// vertical scroll offset, and loading / error indicators.
type ArticleReader struct {
	article *reader.Article
	scroll  int
	loading bool
	err     string
}

func NewArticleReader() *ArticleReader { return &ArticleReader{} }

// Load clears any prior state, marks the reader as loading, and returns a
// tea.Cmd that fires reader.Extract under a derived timeout from parentCtx
// (so the cmd cancels if the session disconnects). Returns nil for an empty
// URL — caller can ignore the return without a nil check.
func (a *ArticleReader) Load(parentCtx context.Context, url string) tea.Cmd {
	if url == "" {
		return nil
	}
	a.article = nil
	a.err = ""
	a.scroll = 0
	a.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, articleReaderCmdTimeout)
		defer cancel()
		art, err := reader.Extract(ctx, url, articleReaderFetchTimeout)
		if err != nil {
			return ArticleReaderLoadedMsg{Err: err}
		}
		return ArticleReaderLoadedMsg{Article: &art}
	}
}

// Loaded records the result of a Load cmd. Stale results (e.g. a delayed
// response that arrives after the user opened a different article) overwrite
// the current article — same behavior as the previous in-screen implementation.
func (a *ArticleReader) Loaded(msg ArticleReaderLoadedMsg) {
	a.loading = false
	if msg.Err != nil {
		a.err = msg.Err.Error()
		return
	}
	a.article = msg.Article
	a.scroll = 0
}

// Clear wipes loaded state. Call when the user closes the reader so the next
// Load starts from a clean View.
func (a *ArticleReader) Clear() {
	a.article = nil
	a.err = ""
	a.scroll = 0
	a.loading = false
}

// Update consumes scroll keys (up/k, down/j, pgup, pgdown). Returns true when
// the key was a scroll key (and so the host screen should not process it
// further). Esc/q are deliberately NOT consumed — the host decides where to
// navigate back to.
func (a *ArticleReader) Update(k tea.KeyMsg) (consumed bool) {
	switch k.String() {
	case "up", "k":
		if a.scroll > 0 {
			a.scroll--
		}
		return true
	case "down", "j":
		a.scroll++ // clamped in View
		return true
	case "pgup":
		a.scroll -= 10
		if a.scroll < 0 {
			a.scroll = 0
		}
		return true
	case "pgdown":
		a.scroll += 10
		return true
	}
	return false
}

// View renders the loading state, error message, or the article itself into
// the given viewport. The title is rendered in the top bar (e.g. "News ›
// Reader"). The host's only render obligation is to call this once per frame
// while it's in reader mode.
func (a *ArticleReader) View(width, height int, title string) string {
	var b strings.Builder
	b.WriteString(articleReaderTitle.Render(title))
	b.WriteString("  ")
	b.WriteString(articleReaderHint.Render("Esc back · ↑/↓ PgUp/PgDn scroll"))
	b.WriteString("\n\n")

	switch {
	case a.loading:
		b.WriteString(articleReaderHint.Render("fetching + extracting article…"))
		return b.String()
	case a.err != "":
		b.WriteString(articleReaderErr.Render("! " + a.err))
		b.WriteString("\n\n")
		b.WriteString(articleReaderHint.Render("press Esc to return"))
		return b.String()
	case a.article == nil:
		b.WriteString(articleReaderHint.Render("no article loaded"))
		return b.String()
	}

	// Articles read better narrower; cap at ~90 cells. Subtract 4 from the
	// host width for padding.
	maxLineW := width - 4
	if maxLineW < 40 {
		maxLineW = 40
	}
	if maxLineW > 90 {
		maxLineW = 90
	}

	lines := layoutArticle(a.article, maxLineW)

	availH := height - 3
	if availH < 1 {
		availH = 1
	}
	maxScroll := len(lines) - availH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.scroll > maxScroll {
		a.scroll = maxScroll
	}
	end := a.scroll + availH
	if end > len(lines) {
		end = len(lines)
	}
	for _, ln := range lines[a.scroll:end] {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if end < len(lines) {
		b.WriteString(articleReaderHint.Render(fmt.Sprintf("  … %d more lines below", len(lines)-end)))
	}
	return b.String()
}

// layoutArticle turns an extracted article into a flat slice of pre-styled,
// width-wrapped lines suitable for vertical slicing by the scroll viewport.
// Title + byline lead, then blocks separated by blank lines.
func layoutArticle(art *reader.Article, maxLineW int) []string {
	var lines []string
	for _, line := range articleWrap(art.Title, maxLineW) {
		lines = append(lines, articleReaderHeading.Render(line))
	}
	meta := ""
	if art.Byline != "" {
		meta = art.Byline
	}
	if art.Host != "" {
		if meta != "" {
			meta += " · "
		}
		meta += art.Host
	}
	if meta != "" {
		for _, line := range articleWrap(meta, maxLineW) {
			lines = append(lines, articleReaderByline.Render(line))
		}
	}
	lines = append(lines, "")

	for _, block := range art.Blocks {
		switch block.Kind {
		case reader.BlockHeading:
			for _, l := range articleWrap(block.Text, maxLineW) {
				lines = append(lines, articleReaderHeading.Render(l))
			}
		case reader.BlockQuote:
			// Vertical bar prefix so the eye picks the quote out of the
			// surrounding paragraphs at a glance.
			for _, l := range articleWrap(block.Text, maxLineW-2) {
				lines = append(lines, articleReaderQuote.Render("│ "+l))
			}
		case reader.BlockCode:
			// Preserve <pre>'s internal newlines verbatim. Long code lines
			// wrap visually so there's no horizontal scroll.
			for _, raw := range strings.Split(block.Text, "\n") {
				if raw == "" {
					lines = append(lines, articleReaderCode.Render(strings.Repeat(" ", maxLineW)))
					continue
				}
				for _, l := range articleWrap(raw, maxLineW-2) {
					padded := "  " + l
					if extra := maxLineW - len([]rune(padded)); extra > 0 {
						padded += strings.Repeat(" ", extra)
					}
					lines = append(lines, articleReaderCode.Render(padded))
				}
			}
		case reader.BlockList:
			// Items came in already prefixed with "• " or "1. " by the HTML
			// walker; wrap each independently so long items keep their
			// bullet hanging indent.
			for _, item := range strings.Split(block.Text, "\n") {
				wrapped := articleWrap(item, maxLineW)
				if len(wrapped) == 0 {
					continue
				}
				lines = append(lines, articleReaderListItem.Render(wrapped[0]))
				for _, cont := range wrapped[1:] {
					lines = append(lines, articleReaderListItem.Render("  "+cont))
				}
			}
		case reader.BlockImage:
			// Reader doesn't fetch images; skip silently so the alt text
			// doesn't leak as a stray paragraph.
			continue
		default: // paragraph + anything unrecognized
			for _, l := range articleWrap(block.Text, maxLineW) {
				lines = append(lines, articleReaderBody.Render(l))
			}
		}
		lines = append(lines, "")
	}
	return lines
}

// articleWrap is a tiny word-wrap for plain-text article blocks. Counts
// visible width via runewidth so wide glyphs land in the right column.
func articleWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		line := ""
		lineW := 0
		for _, word := range strings.Fields(paragraph) {
			ww := runewidth.StringWidth(word)
			switch {
			case line == "":
				line, lineW = word, ww
			case lineW+1+ww <= width:
				line += " " + word
				lineW += 1 + ww
			default:
				out = append(out, line)
				line, lineW = word, ww
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

var (
	articleReaderTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	articleReaderHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	articleReaderErr      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	articleReaderHeading  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	articleReaderByline   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	articleReaderBody     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	articleReaderQuote    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim)).Italic(true)
	articleReaderCode     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Background(lipgloss.Color(theme.ColorSurfaceAlt))
	articleReaderListItem = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
)
