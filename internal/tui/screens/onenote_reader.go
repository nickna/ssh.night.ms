package screens

import (
	"bytes"
	"image"
	_ "image/gif"  // register decoders for FetchResource-rendered page images
	_ "image/jpeg" // (we decode bytes ourselves — the shared sess.Images pool
	_ "image/png"  //  can't fetch auth-protected Graph resource URLs)
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/imaging"
	"github.com/nickna/ssh.night.ms/internal/onenote"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// onenoteImageCols caps the half-block width of an inline page image so a big
// picture doesn't overflow the reader column.
const onenoteImageCols = 56

// imageCols is the half-block width to render an inline image at: capped to
// the live right-pane width so it can't overrun the column on a narrow term.
func (m *OneNote) imageCols() int {
	rightW := m.sess.Width - m.sess.Width/3 - 3
	if rightW > onenoteImageCols {
		rightW = onenoteImageCols
	}
	if rightW < 16 {
		rightW = 16
	}
	return rightW
}

func (m *OneNote) handleReaderKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	switch k.String() {
	case "esc", "tab":
		m.mode = onModeBrowse
	case "up", "k":
		if m.scroll > 0 {
			m.scroll--
		}
	case "down", "j":
		m.scroll++
	case "pgup":
		m.scroll -= 10
		if m.scroll < 0 {
			m.scroll = 0
		}
	case "pgdown", " ":
		m.scroll += 10
	case "home", "g":
		m.scroll = 0
	case "a":
		return m.startAppend()
	case "e":
		return m.startEdit()
	case "d":
		return m.startDeleteCurrent()
	}
	return m, nil
}

// scheduleImages fires one fetch Cmd per not-yet-cached image block on the
// page. Each result lands as onImageRenderedMsg and repaints in place.
func (m *OneNote) scheduleImages(pc *onenote.PageContent) tea.Cmd {
	var cmds []tea.Cmd
	for _, b := range pc.Blocks {
		if b.Kind != onenote.BlockImage || b.URL == "" {
			continue
		}
		if _, ok := m.imgCache[b.URL]; ok {
			continue
		}
		cmds = append(cmds, m.fetchImage(pc.ID, b.URL))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// fetchImage downloads a page resource through the authenticated service path
// and renders it to half-block lines. A failure caches nil → the block falls
// back to its "[image: …]" placeholder without retrying.
func (m *OneNote) fetchImage(pageID, url string) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	cols := m.imageCols()
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(15 * time.Second)
		defer cancel()
		data, err := svc.FetchResource(ctx, uid, url)
		if err != nil {
			return onImageRenderedMsg{pageID: pageID, url: url, lines: nil}
		}
		img, _, derr := image.Decode(bytes.NewReader(data))
		if derr != nil {
			return onImageRenderedMsg{pageID: pageID, url: url, lines: nil}
		}
		return onImageRenderedMsg{pageID: pageID, url: url, lines: imaging.RenderToANSILines(img, cols)}
	}
}

// renderReader builds the right pane: a breadcrumb header + the page body
// rendered with markdown styling, windowed to height h and padded to width w.
func (m *OneNote) renderReader(w, h int) string {
	if m.curPage == nil {
		hint := "select a page on the left and press Enter"
		if m.loading {
			hint = "loading page…"
		}
		return padPaneLines([]string{"", theme.Hint.Render("  " + hint)}, w, h)
	}

	head := m.readerHeader(w)
	headH := len(head)
	bodyH := h - headH
	if bodyH < 1 {
		bodyH = 1
	}

	content := m.renderBlocks(w)
	// Clamp scroll so we never page past the end.
	maxScroll := len(content) - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	start := m.scroll
	end := start + bodyH
	if end > len(content) {
		end = len(content)
	}

	lines := append([]string{}, head...)
	lines = append(lines, content[start:end]...)
	return padPaneLines(lines, w, h)
}

// readerHeader is the breadcrumb + edited-time + non-text badge strip.
func (m *OneNote) readerHeader(w int) []string {
	pc := m.curPage
	crumb := m.breadcrumb(pc)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).
		Render(runewidth.Truncate(firstNonEmpty(pc.Title, "(untitled)"), w-2, "…"))

	meta := theme.Sub.Render(relTime(pc.ModifiedAt))
	if pc.HasNonText {
		meta += "  " + theme.EditedChip.Render("contains images/tables")
	}
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Render(strings.Repeat("─", w))
	out := []string{}
	if crumb != "" {
		out = append(out, theme.BreadcrumbSep.Render(runewidth.Truncate(crumb, w, "…")))
	}
	out = append(out, title, meta, sep)
	return out
}

// breadcrumb resolves "Notebook ▸ Section ▸ Title" from the tree when the
// page's section is present; otherwise returns just the section glyph trail it
// can find. Best-effort — a page opened from the recent list may not be in the
// tree yet.
func (m *OneNote) breadcrumb(pc *onenote.PageContent) string {
	secIdx := m.indexOf(pc.SectionID)
	if secIdx < 0 {
		return ""
	}
	sec := m.tree[secIdx]
	parts := []string{sec.name}
	if nbIdx := m.indexOf(sec.parentID); nbIdx >= 0 {
		parts = append([]string{m.tree[nbIdx].name}, parts...)
	}
	return strings.Join(parts, " ▸ ")
}

// renderBlocks turns the page's parsed blocks into styled, width-wrapped
// lines, splicing in any rendered inline images.
func (m *OneNote) renderBlocks(w int) []string {
	var lines []string
	for _, b := range m.curPage.Blocks {
		switch b.Kind {
		case onenote.BlockHeading:
			lines = append(lines, lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).Render(b.Text))
			lines = append(lines, "")
		case onenote.BlockQuote:
			bar := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
			for _, ln := range wrapLines(b.Text, w-2) {
				lines = append(lines, bar.Render("│ ")+lipgloss.NewStyle().
					Foreground(lipgloss.Color(theme.ColorAccentDim)).Render(ln))
			}
			lines = append(lines, "")
		case onenote.BlockCode:
			box := lipgloss.NewStyle().Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Foreground(lipgloss.Color(theme.ColorText))
			for _, ln := range strings.Split(b.Text, "\n") {
				lines = append(lines, box.Render(padTo("  "+ln, w)))
			}
			lines = append(lines, "")
		case onenote.BlockList:
			for _, ln := range strings.Split(b.Text, "\n") {
				for _, wl := range wrapLines(ln, w) {
					lines = append(lines, theme.Body.Render(wl))
				}
			}
			lines = append(lines, "")
		case onenote.BlockImage:
			lines = append(lines, m.renderImageBlock(b, w)...)
			lines = append(lines, "")
		case onenote.BlockTable:
			lines = append(lines, theme.EditedChip.Render("table — read only:"))
			box := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
			for _, ln := range strings.Split(b.Text, "\n") {
				lines = append(lines, box.Render(runewidth.Truncate(ln, w, "…")))
			}
			lines = append(lines, "")
		default: // paragraph
			for _, wl := range wrapLines(b.Text, w) {
				lines = append(lines, theme.Body.Render(wl))
			}
			lines = append(lines, "")
		}
	}
	if len(lines) == 0 {
		lines = append(lines, theme.Hint.Render("(empty page)"))
	}
	return lines
}

func (m *OneNote) renderImageBlock(b onenote.Block, w int) []string {
	if img, ok := m.imgCache[b.URL]; ok && len(img) > 0 {
		return img
	}
	alt := b.Text
	if strings.TrimSpace(alt) == "" {
		alt = "image"
	}
	return []string{lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim)).
		Render(runewidth.Truncate("[image: "+alt+"]", w, "…"))}
}

// wrapLines word-wraps text to width w, returning at least one (possibly
// empty) line.
func wrapLines(text string, w int) []string {
	if w < 1 {
		w = 1
	}
	text = strings.TrimRight(text, " ")
	if text == "" {
		return []string{""}
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(text)
	return strings.Split(wrapped, "\n")
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
