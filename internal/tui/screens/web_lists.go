package screens

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
)

// Reload Cmds for the two lists. Both run with a short timeout — the screen
// stays usable even if Postgres briefly stalls.

func (m *Web) reloadBookmarks() tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		rows, err := queries.ListWebBookmarks(ctx, uid)
		return bookmarksLoadedMsg{rows: rows, err: err}
	}
}

func (m *Web) reloadHistory() tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		rows, err := queries.RecentWebHistory(ctx, gen.RecentWebHistoryParams{
			UserID: uid,
			Limit:  recentHistoryLimit,
		})
		return historyLoadedMsg{rows: rows, err: err}
	}
}

// ---------------------------------------------------------------------------
// Bookmarks region
// ---------------------------------------------------------------------------

func (m *Web) handleBookmarksKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		if m.bmCursor > 0 {
			m.bmCursor--
		}
		return m, nil
	case "down", "j":
		if m.bmCursor < len(m.bookmarks)-1 {
			m.bmCursor++
		} else {
			// Step off the bottom into the next region.
			if len(m.history) > 0 {
				m.focus = focusHistory
				m.hsCursor = 0
			}
		}
		return m, nil
	case "enter":
		if m.bmCursor < len(m.bookmarks) {
			return m, m.launch(m.bookmarks[m.bmCursor].Url)
		}
		return m, nil
	case "a":
		// Add a brand-new bookmark sourced from the URL textinput.
		target := strings.TrimSpace(m.input.Value())
		if target == "" {
			m.status = "type a URL above to bookmark (or visit one first)"
			return m, nil
		}
		m.openEditorForAdd(ensureScheme(target))
		return m, nil
	case "e":
		if m.bmCursor < len(m.bookmarks) {
			b := m.bookmarks[m.bmCursor]
			m.openEditorForRename(b.ID, b.Url, b.Title)
		}
		return m, nil
	case "d":
		if m.bmCursor < len(m.bookmarks) {
			b := m.bookmarks[m.bmCursor]
			title := b.Title
			if title == "" {
				title = defaultBookmarkTitle(b.Url)
			}
			m.confirm = components.NewConfirm(
				"remove bookmark",
				fmt.Sprintf("remove %q? this cannot be undone.", title),
			)
			m.confirmKind = fmt.Sprintf("deleteBookmark:%d", b.ID)
		}
		return m, nil
	}
	return m, nil
}

func (m *Web) renderBookmarks() string {
	var b strings.Builder
	arrow := "  "
	if m.focus == focusBookmarks {
		arrow = webNote.Render("▸ ")
	}
	b.WriteString(arrow)
	b.WriteString(webHead.Render("★ Bookmarks"))
	b.WriteString("  ")
	if m.focus == focusBookmarks {
		b.WriteString(webHint.Render("Enter load · a add · e rename · d delete"))
	} else {
		b.WriteString(webHint.Render(fmt.Sprintf("(%d)", len(m.bookmarks))))
	}
	b.WriteString("\n")

	if m.bookmarks == nil {
		b.WriteString("  ")
		b.WriteString(webDim.Render("loading…"))
		b.WriteString("\n")
		return b.String()
	}
	if len(m.bookmarks) == 0 {
		b.WriteString("  ")
		b.WriteString(webDim.Render("(empty — Ctrl+B to bookmark the URL above, or visit a site and Ctrl+B after)"))
		b.WriteString("\n")
		return b.String()
	}

	titleW, urlW := m.bookmarkColumnWidths()
	for i, row := range m.bookmarks {
		title := row.Title
		if title == "" {
			title = defaultBookmarkTitle(row.Url)
		}
		title = runewidth.Truncate(title, titleW, "…")
		title = runewidth.FillRight(title, titleW)
		urlDisplay := compactURL(row.Url)
		urlDisplay = runewidth.Truncate(urlDisplay, urlW, "…")

		line := "  " + title + "  " + webDim.Render(urlDisplay)
		if m.focus == focusBookmarks && i == m.bmCursor {
			// Build the highlighted line at the same column positions, but
			// inside the highlight style so background spans both columns.
			plain := "▸ " + runewidth.Truncate(title, titleW, "…") + "  " + urlDisplay
			line = webRowOn.Render(plain)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// bookmarkColumnWidths reserves a stable layout: title column ~28 cols, URL
// column = whatever's left up to a sane cap. Recompute each render so terminal
// resizes flow through cleanly.
func (m *Web) bookmarkColumnWidths() (titleW, urlW int) {
	inner := m.sess.Width - 4 // leading "  " + arrow gutter
	if inner < 40 {
		inner = 40
	}
	titleW = 28
	urlW = inner - titleW - 2
	if urlW < 20 {
		urlW = 20
	}
	if urlW > 80 {
		urlW = 80
	}
	return titleW, urlW
}

// compactURL strips the scheme + leading www. for display. The full URL is
// what we launch; this is purely for screen density.
func compactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.TrimPrefix(u.Host, "www.")
	out := host + u.Path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + u.Fragment
	}
	if out == "" {
		return raw
	}
	return out
}

func (m *Web) deleteBookmark(id int64) tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		err := queries.DeleteWebBookmark(ctx, gen.DeleteWebBookmarkParams{UserID: uid, ID: id})
		return bookmarkDeletedMsg{err: err}
	}
}

// ---------------------------------------------------------------------------
// History region
// ---------------------------------------------------------------------------

func (m *Web) handleHistoryKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		if m.hsCursor > 0 {
			m.hsCursor--
		} else {
			// Step up into bookmarks (or URL if no bookmarks).
			if len(m.bookmarks) > 0 {
				m.focus = focusBookmarks
				m.bmCursor = len(m.bookmarks) - 1
			} else {
				m.focus = focusURL
				m.input.Focus()
			}
		}
		return m, nil
	case "down", "j":
		if m.hsCursor < len(m.history)-1 {
			m.hsCursor++
		}
		return m, nil
	case "enter":
		if m.hsCursor < len(m.history) {
			return m, m.launch(m.history[m.hsCursor].Url)
		}
		return m, nil
	case "b":
		// Bookmark the selected history row.
		if m.hsCursor < len(m.history) {
			m.openEditorForAdd(m.history[m.hsCursor].Url)
		}
		return m, nil
	case "d":
		if m.hsCursor < len(m.history) {
			row := m.history[m.hsCursor]
			return m, m.deleteHistoryRow(row.ID)
		}
		return m, nil
	case "c":
		if len(m.history) == 0 {
			return m, nil
		}
		m.confirm = components.NewConfirm(
			"clear history",
			fmt.Sprintf("delete all %d entries from your browser history?", len(m.history)),
		)
		m.confirmKind = "clearHistory"
		return m, nil
	}
	return m, nil
}

func (m *Web) renderHistory() string {
	var b strings.Builder
	arrow := "  "
	if m.focus == focusHistory {
		arrow = webNote.Render("▸ ")
	}
	b.WriteString(arrow)
	b.WriteString(webHead.Render("⏱ Recent"))
	b.WriteString("  ")
	if m.focus == focusHistory {
		b.WriteString(webHint.Render("Enter load · b bookmark · d remove · c clear"))
	} else {
		b.WriteString(webHint.Render(fmt.Sprintf("(%d)", len(m.history))))
	}
	b.WriteString("\n")

	if m.history == nil {
		b.WriteString("  ")
		b.WriteString(webDim.Render("loading…"))
		b.WriteString("\n")
		return b.String()
	}
	if len(m.history) == 0 {
		b.WriteString("  ")
		b.WriteString(webDim.Render("(empty — pages you visit will appear here)"))
		b.WriteString("\n")
		return b.String()
	}

	// Show the first ~8 rows inline; the rest are reachable via arrow-down.
	const visible = 8
	start := 0
	if m.hsCursor >= visible {
		// Slide the window so the cursor stays in view.
		start = m.hsCursor - visible + 1
	}
	end := start + visible
	if end > len(m.history) {
		end = len(m.history)
	}

	inner := m.sess.Width - 4
	if inner < 50 {
		inner = 50
	}
	urlW := inner - 16 // reserve ~16 for the right-aligned age
	if urlW < 30 {
		urlW = 30
	}

	for i := start; i < end; i++ {
		row := m.history[i]
		urlDisplay := compactURL(row.Url)
		urlDisplay = runewidth.Truncate(urlDisplay, urlW, "…")
		urlDisplay = runewidth.FillRight(urlDisplay, urlW)
		age := components.FormatRelativeAge(row.LastVisitedAt.Time)
		ageDisplay := runewidth.FillLeft(age, 14)

		line := "  " + urlDisplay + "  " + webDim.Render(ageDisplay)
		if m.focus == focusHistory && i == m.hsCursor {
			plain := "▸ " + runewidth.Truncate(compactURL(row.Url), urlW, "…")
			plain = runewidth.FillRight(plain, urlW+2)
			plain += "  " + ageDisplay
			line = webRowOn.Render(plain)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if len(m.history) > visible {
		b.WriteString("  ")
		b.WriteString(webDim.Render(fmt.Sprintf("(%d of %d — ↑/↓ to scroll)", end-start, len(m.history))))
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Web) deleteHistoryRow(id int64) tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		err := queries.DeleteWebHistoryEntry(ctx, gen.DeleteWebHistoryEntryParams{UserID: uid, ID: id})
		return historyDeletedMsg{err: err}
	}
}

func (m *Web) clearHistory() tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		err := queries.ClearWebHistory(ctx, uid)
		return historyClearedMsg{err: err}
	}
}
