package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/auth/usertoken"
	"github.com/nickna/ssh.night.ms/internal/onenote"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// OneNote is the two-pane OneNote screen: a lazily-expanded tree of
// notebooks ▸ sections ▸ pages on the left, the selected page rendered with
// markdown styling + inline images on the right. Editing (quick append, full
// markdown rewrite, create, delete) overlays on top. All reads/writes go
// through the in-process sess.OneNote service (which owns auth + caching);
// this screen owns only presentation + interaction state.
type OneNote struct {
	sess *session.Session
	mode onenoteMode

	// Tree — a flattened list of currently-visible nodes. Expanding a
	// notebook/section splices its children in right after it; collapsing
	// removes the contiguous deeper-depth block.
	tree   []onenoteNode
	cursor int

	// Reader — the open page + its scroll offset + a per-URL inline-image
	// cache (rendered half-block lines; nil entry = fetch failed → placeholder).
	curPage  *onenote.PageContent
	scroll   int
	imgCache map[string][]string

	// Recent overlay.
	recent       []onenote.RecentPage
	recentCursor int

	// Edit buffers (built lazily on mode entry).
	input       textinput.Model // quick-append + create title
	area        textarea.Model  // full-edit + create body
	createFocus int             // 0 = title, 1 = body
	editPageID  string          // page being full-edited
	editSection string          // section for a pending create
	pendingMD   string          // edited markdown awaiting a non-text confirm

	// Confirm modal.
	confirm     *components.Confirm
	confirmKind string // "deletePage" | "editConfirm"

	// Link/scope/reauth/unavailable call-to-action.
	cta ctaKind

	// Transient status line.
	notice     string
	noticeKind string // "ok" | "err"
	loading    bool

	header *art.CellGrid // optional banner art
}

type onenoteMode int

const (
	onModeBrowse      onenoteMode = iota // tree focused
	onModeReader                         // reader focused (scroll keys)
	onModeRecent                         // recent-pages list
	onModeQuickAppend                    // single-line append input
	onModeEdit                           // full-body textarea
	onModeCreate                         // title + body
	onModeConfirm                        // confirm modal over the base view
	onModeLinkCTA                        // not-linked / scope / reauth / unavailable
)

type ctaKind int

const (
	ctaNone ctaKind = iota
	ctaUnavailable
	ctaNoLink
	ctaScope
	ctaReauth
)

type nodeKind int

const (
	nodeNotebook nodeKind = iota
	nodeSection
	nodePage
)

// onenoteNode is one visible row in the flattened tree.
type onenoteNode struct {
	kind     nodeKind
	id       string // Graph id
	name     string
	colorHex string // notebook accent color; sections/pages inherit the parent's
	depth    int    // 0 notebook, 1 section, 2 page
	expanded bool
	loading  bool
	parentID string // section id for a page; notebook id for a section
}

// notebookPalette colors notebooks that Graph returns without a color, cycled
// by notebook ordinal so the tree still scans by hue.
var notebookPalette = []string{
	theme.ColorAccent, theme.ColorCyan, theme.ColorGreen, theme.ColorYellow, theme.ColorAccentDim,
}

func NewOneNote(sess *session.Session) tea.Model {
	m := &OneNote{
		sess:     sess,
		imgCache: map[string][]string{},
		header:   loadOneNoteHeader(),
	}
	if sess.OneNote == nil {
		m.mode = onModeLinkCTA
		m.cta = ctaUnavailable
	}
	return m
}

// loadOneNoteHeader best-effort loads operator-droppable banner art. Absent →
// nil (the screen falls back to a text title). No config knob: the file is
// looked up at a conventional path relative to the working dir, matching the
// "drop art without a rebuild" convention used by the gallery/lobby icons.
func loadOneNoteHeader() *art.CellGrid {
	g, err := art.LoadFile("data/art/onenote/header.ans")
	if err != nil {
		return nil
	}
	return g
}

func (m *OneNote) Init() tea.Cmd {
	if m.sess.OneNote == nil {
		return nil
	}
	m.loading = true
	return m.loadNotebooks()
}

// --- async message envelopes ---------------------------------------------

type onNotebooksLoadedMsg struct {
	notebooks []onenote.Notebook
	err       error
}
type onSectionsLoadedMsg struct {
	notebookID string
	colorHex   string
	sections   []onenote.Section
	err        error
}
type onPagesLoadedMsg struct {
	sectionID string
	colorHex  string
	pages     []onenote.Page
	err       error
}
type onPageLoadedMsg struct {
	page onenote.PageContent
	err  error
}
type onRecentLoadedMsg struct {
	pages []onenote.RecentPage
	err   error
}
type onImageRenderedMsg struct {
	pageID string
	url    string
	lines  []string
}
type onWriteResultMsg struct {
	kind string // "append" | "replace" | "create" | "delete"
	page onenote.Page
	err  error
}

// --- loads ----------------------------------------------------------------

func (m *OneNote) loadNotebooks() tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(12 * time.Second)
		defer cancel()
		nbs, err := svc.ListNotebooks(ctx, uid)
		return onNotebooksLoadedMsg{notebooks: nbs, err: err}
	}
}

func (m *OneNote) loadChildren(node onenoteNode) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	switch node.kind {
	case nodeNotebook:
		nbID, color := node.id, node.colorHex
		return func() tea.Msg {
			ctx, cancel := m.sess.CtxWithTimeout(12 * time.Second)
			defer cancel()
			secs, err := svc.ListSections(ctx, uid, nbID)
			return onSectionsLoadedMsg{notebookID: nbID, colorHex: color, sections: secs, err: err}
		}
	case nodeSection:
		secID, color := node.id, node.colorHex
		return func() tea.Msg {
			ctx, cancel := m.sess.CtxWithTimeout(12 * time.Second)
			defer cancel()
			pages, err := svc.ListPages(ctx, uid, secID)
			return onPagesLoadedMsg{sectionID: secID, colorHex: color, pages: pages, err: err}
		}
	}
	return nil
}

func (m *OneNote) openPage(pageID string) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	m.loading = true
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(20 * time.Second)
		defer cancel()
		pc, err := svc.GetPage(ctx, uid, pageID)
		return onPageLoadedMsg{page: pc, err: err}
	}
}

func (m *OneNote) loadRecent() tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(8 * time.Second)
		defer cancel()
		rows, err := svc.ListRecentViewed(ctx, uid)
		return onRecentLoadedMsg{pages: rows, err: err}
	}
}

// classifyErr maps a service error to a CTA, returning ctaNone for ordinary
// (transient/Graph) errors that should surface as a status line instead.
func classifyErr(err error) ctaKind {
	switch {
	case errors.Is(err, usertoken.ErrNoLink):
		return ctaNoLink
	case errors.Is(err, usertoken.ErrMissingScope):
		return ctaScope
	case errors.Is(err, usertoken.ErrNeedsReauth):
		return ctaReauth
	}
	return ctaNone
}

// handleServiceErr routes a service error: typed auth errors flip to the CTA
// screen; everything else becomes a transient notice. Returns true when it
// consumed the error (caller should stop).
func (m *OneNote) handleServiceErr(err error) bool {
	if err == nil {
		return false
	}
	if k := classifyErr(err); k != ctaNone {
		m.cta = k
		m.mode = onModeLinkCTA
		return true
	}
	m.notice = err.Error()
	m.noticeKind = "err"
	return true
}

// --- tree mutation --------------------------------------------------------

func (m *OneNote) indexOf(id string) int {
	for i := range m.tree {
		if m.tree[i].id == id {
			return i
		}
	}
	return -1
}

// collapseAt collapses the node at idx, dropping the contiguous block of
// deeper-depth descendants that follow it.
func (m *OneNote) collapseAt(idx int) {
	depth := m.tree[idx].depth
	j := idx + 1
	for j < len(m.tree) && m.tree[j].depth > depth {
		j++
	}
	m.tree = append(m.tree[:idx+1], m.tree[j:]...)
	m.tree[idx].expanded = false
}

// insertChildren splices children directly after the node identified by
// parentID and marks it expanded.
func (m *OneNote) insertChildren(parentID string, children []onenoteNode) {
	idx := m.indexOf(parentID)
	if idx < 0 {
		return
	}
	m.tree[idx].expanded = true
	m.tree[idx].loading = false
	next := make([]onenoteNode, 0, len(m.tree)+len(children))
	next = append(next, m.tree[:idx+1]...)
	next = append(next, children...)
	next = append(next, m.tree[idx+1:]...)
	m.tree = next
}

func (m *OneNote) clampCursor() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.tree) {
		m.cursor = len(m.tree) - 1
	}
}

// currentSectionID returns the section the cursor is "in" (for create), or
// false when the cursor is on a notebook (no section chosen yet).
func (m *OneNote) currentSectionID() (string, bool) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return "", false
	}
	n := m.tree[m.cursor]
	switch n.kind {
	case nodeSection:
		return n.id, true
	case nodePage:
		return n.parentID, true
	}
	return "", false
}

// --- Update ---------------------------------------------------------------

func (m *OneNote) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case onNotebooksLoadedMsg:
		m.loading = false
		if m.handleServiceErr(msg.err) {
			return m, nil
		}
		m.tree = m.tree[:0]
		for i, nb := range msg.notebooks {
			color := nb.Color
			if color == "" {
				color = notebookPalette[i%len(notebookPalette)]
			}
			m.tree = append(m.tree, onenoteNode{
				kind: nodeNotebook, id: nb.ID, name: nb.Name, colorHex: color, depth: 0,
			})
		}
		m.clampCursor()
		return m, nil

	case onSectionsLoadedMsg:
		if msg.err != nil {
			if idx := m.indexOf(msg.notebookID); idx >= 0 {
				m.tree[idx].loading = false
			}
			m.handleServiceErr(msg.err)
			return m, nil
		}
		children := make([]onenoteNode, 0, len(msg.sections))
		for _, s := range msg.sections {
			children = append(children, onenoteNode{
				kind: nodeSection, id: s.ID, name: s.Name, colorHex: msg.colorHex, depth: 1, parentID: msg.notebookID,
			})
		}
		m.insertChildren(msg.notebookID, children)
		m.clampCursor()
		return m, nil

	case onPagesLoadedMsg:
		if msg.err != nil {
			if idx := m.indexOf(msg.sectionID); idx >= 0 {
				m.tree[idx].loading = false
			}
			m.handleServiceErr(msg.err)
			return m, nil
		}
		children := make([]onenoteNode, 0, len(msg.pages))
		for _, p := range msg.pages {
			title := p.Title
			if strings.TrimSpace(title) == "" {
				title = "(untitled)"
			}
			children = append(children, onenoteNode{
				kind: nodePage, id: p.ID, name: title, colorHex: msg.colorHex, depth: 2, parentID: msg.sectionID,
			})
		}
		m.insertChildren(msg.sectionID, children)
		m.clampCursor()
		return m, nil

	case onPageLoadedMsg:
		m.loading = false
		if m.handleServiceErr(msg.err) {
			return m, nil
		}
		pc := msg.page
		m.curPage = &pc
		m.scroll = 0
		m.imgCache = map[string][]string{}
		m.mode = onModeReader
		return m, m.scheduleImages(&pc)

	case onRecentLoadedMsg:
		if m.handleServiceErr(msg.err) {
			return m, nil
		}
		m.recent = msg.pages
		if m.recentCursor >= len(m.recent) {
			m.recentCursor = 0
		}
		return m, nil

	case onImageRenderedMsg:
		if m.curPage != nil && m.curPage.ID == msg.pageID {
			m.imgCache[msg.url] = msg.lines
		}
		return m, nil

	case onWriteResultMsg:
		return m.handleWriteResult(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *OneNote) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case onModeLinkCTA:
		return m.handleCTAKey(k)
	case onModeRecent:
		return m.handleRecentKey(k)
	case onModeQuickAppend:
		return m.handleAppendKey(k)
	case onModeEdit:
		return m.handleEditKey(k)
	case onModeCreate:
		return m.handleCreateKey(k)
	case onModeConfirm:
		return m.handleConfirmKey(k)
	case onModeReader:
		return m.handleReaderKey(k)
	default:
		return m.handleBrowseKey(k)
	}
}

func (m *OneNote) handleBrowseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.tree)-1 {
			m.cursor++
		}
	case "right", "l", "enter":
		return m.activateNode()
	case "left", "h":
		return m.collapseOrParent()
	case "tab":
		if m.curPage != nil {
			m.mode = onModeReader
		}
	case "r":
		m.mode = onModeRecent
		return m, m.loadRecent()
	case "n":
		return m.startCreate()
	case "d":
		return m.startDeleteFromCursor()
	case "g":
		// refresh notebooks
		m.loading = true
		return m, m.loadNotebooks()
	}
	return m, nil
}

// activateNode expands/collapses a notebook/section or opens a page.
func (m *OneNote) activateNode() (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return m, nil
	}
	node := m.tree[m.cursor]
	switch node.kind {
	case nodeNotebook, nodeSection:
		if node.expanded {
			m.collapseAt(m.cursor)
			return m, nil
		}
		if node.loading {
			return m, nil
		}
		m.tree[m.cursor].loading = true
		return m, m.loadChildren(m.tree[m.cursor])
	case nodePage:
		return m, m.openPage(node.id)
	}
	return m, nil
}

// collapseOrParent collapses the node if it's an expanded container, otherwise
// jumps the cursor to the parent row.
func (m *OneNote) collapseOrParent() (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return m, nil
	}
	node := m.tree[m.cursor]
	if (node.kind == nodeNotebook || node.kind == nodeSection) && node.expanded {
		m.collapseAt(m.cursor)
		return m, nil
	}
	// Walk upward to the nearest shallower row.
	for i := m.cursor - 1; i >= 0; i-- {
		if m.tree[i].depth < node.depth {
			m.cursor = i
			break
		}
	}
	return m, nil
}

func (m *OneNote) handleRecentKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "r":
		m.mode = onModeBrowse
	case "up", "k":
		if m.recentCursor > 0 {
			m.recentCursor--
		}
	case "down", "j":
		if m.recentCursor < len(m.recent)-1 {
			m.recentCursor++
		}
	case "enter":
		if m.recentCursor < len(m.recent) {
			id := m.recent[m.recentCursor].PageID
			return m, m.openPage(id)
		}
	}
	return m, nil
}

func (m *OneNote) handleCTAKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(k.String()) {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "p":
		if m.cta == ctaNoLink || m.cta == ctaScope || m.cta == ctaReauth {
			return m, nav.Navigate(nav.DestProfile)
		}
	}
	return m, nil
}

// --- View -----------------------------------------------------------------

func (m *OneNote) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	switch m.mode {
	case onModeLinkCTA:
		return m.viewCTA()
	case onModeRecent:
		return m.viewRecent()
	case onModeEdit, onModeCreate:
		return m.viewEditor()
	}

	base := m.viewMain()
	switch m.mode {
	case onModeQuickAppend:
		modal := m.renderAppendModal()
		dim := components.DimSGR(base, theme.ColorDim)
		return components.Overlay(dim, modal, m.sess.Width, m.sess.Height)
	case onModeConfirm:
		if m.confirm != nil {
			modal := m.confirm.View(44)
			dim := components.DimSGR(base, theme.ColorDim)
			return components.Overlay(dim, modal, m.sess.Width, m.sess.Height)
		}
	}
	return base
}

func (m *OneNote) headerView() string {
	if m.header != nil {
		return components.RenderCellGrid(m.header)
	}
	return theme.Title.Render("OneNote")
}

func (m *OneNote) viewMain() string {
	w, h := m.sess.Width, m.sess.Height
	header := m.headerView()
	headerH := lipgloss.Height(header)

	bodyH := h - headerH - 2 // -2 for hint + status line
	if bodyH < 4 {
		bodyH = 4
	}

	leftW := w / 3
	if leftW < 24 {
		leftW = 24
	}
	if leftW > 40 {
		leftW = 40
	}
	rightW := w - leftW - 3
	if rightW < 20 {
		rightW = 20
	}

	left := m.renderTree(leftW, bodyH)
	right := m.renderReader(rightW, bodyH)
	gutterRows := make([]string, bodyH)
	for i := range gutterRows {
		gutterRows[i] = " │ "
	}
	gutter := strings.Join(gutterRows, "\n")

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, gutter, right)

	hint := m.renderHint()
	status := m.renderStatus()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, hint, status)
}

func (m *OneNote) renderHint() string {
	var h string
	if m.mode == onModeReader {
		h = "↑/↓ scroll · a append · e edit · d delete · Tab tree · Esc back"
	} else {
		h = "↑/↓ move · →/Enter open · ← collapse · n new · d delete · r recent · Esc lobby"
	}
	return theme.Hint.Render(h)
}

func (m *OneNote) renderStatus() string {
	if m.loading {
		return theme.Hint.Render("loading…")
	}
	if m.notice == "" {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	if m.noticeKind == "err" {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	}
	return style.Render(m.notice)
}

// renderTree draws the flattened tree windowed to height h, padded to width w.
func (m *OneNote) renderTree(w, h int) string {
	if len(m.tree) == 0 {
		msg := "no notebooks"
		if m.loading {
			msg = "loading notebooks…"
		}
		return padPaneLines([]string{theme.Hint.Render(msg)}, w, h)
	}

	// Window the visible rows around the cursor.
	start := 0
	if m.cursor >= h {
		start = m.cursor - h + 1
	}
	end := start + h
	if end > len(m.tree) {
		end = len(m.tree)
	}

	lines := make([]string, 0, h)
	for i := start; i < end; i++ {
		lines = append(lines, m.renderTreeRow(i, w))
	}
	return padPaneLines(lines, w, h)
}

func (m *OneNote) renderTreeRow(i, w int) string {
	n := m.tree[i]
	indent := strings.Repeat("  ", n.depth)

	var glyph string
	switch n.kind {
	case nodeNotebook, nodeSection:
		if n.loading {
			glyph = "…"
		} else if n.expanded {
			glyph = "▾"
		} else {
			glyph = "▸"
		}
	case nodePage:
		glyph = "·"
	}

	dot := lipgloss.NewStyle().Foreground(lipgloss.Color(n.colorHex)).Render("●")
	if n.kind == nodePage {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim)).Render("◦")
	}

	// Width budget for the name = pane minus prefix(2) indent glyph(1) sp dot(1) sp.
	nameW := w - 2 - lipgloss.Width(indent) - 4
	if nameW < 4 {
		nameW = 4
	}
	name := runewidth.Truncate(n.name, nameW, "…")

	prefix := "  "
	if i == m.cursor && m.focusedOnTree() {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccent)).Render("▸ ")
		name = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorText)).Render(name)
	} else if i == m.cursor {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim)).Render("· ")
	}
	return prefix + indent + glyph + " " + dot + " " + name
}

func (m *OneNote) focusedOnTree() bool {
	return m.mode == onModeBrowse
}

// --- CTA + recent views ---------------------------------------------------

func (m *OneNote) viewCTA() string {
	var title, body string
	switch m.cta {
	case ctaUnavailable:
		title = "OneNote unavailable"
		body = "OneNote isn't enabled on this server.\n\nPress Esc to return to the lobby."
	case ctaNoLink:
		title = "Link Microsoft to use OneNote"
		body = "You haven't linked a Microsoft account yet.\n\nPress P to open Profile → connected accounts, then link Microsoft (granting OneNote access).\n\nEsc returns to the lobby."
	case ctaScope:
		title = "Re-authorize Microsoft"
		body = "Your Microsoft account is linked, but the grant predates OneNote access.\n\nPress P to open Profile → connected accounts and re-link Microsoft to grant it.\n\nEsc returns to the lobby."
	case ctaReauth:
		title = "Microsoft authorization expired"
		body = "Your Microsoft authorization needs renewing.\n\nPress P to open Profile → connected accounts and re-link Microsoft.\n\nEsc returns to the lobby."
	}
	panel := theme.ModalFrame.Width(54).Render(
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).Render(title) +
			"\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText)).Width(50).Render(body))
	return lipgloss.Place(m.sess.Width, m.sess.Height, lipgloss.Center, lipgloss.Center, panel)
}

func (m *OneNote) viewRecent() string {
	w, h := m.sess.Width, m.sess.Height
	var b strings.Builder
	b.WriteString(theme.Title.Render("OneNote — recent"))
	b.WriteString("\n")
	b.WriteString(theme.Hint.Render("↑/↓ select · Enter open · r/Esc back"))
	b.WriteString("\n\n")

	if len(m.recent) == 0 {
		b.WriteString(theme.Hint.Render("no recently-viewed pages yet"))
		return b.String()
	}

	rows := h - 5
	if rows < 3 {
		rows = 3
	}
	start := 0
	if m.recentCursor >= rows {
		start = m.recentCursor - rows + 1
	}
	end := start + rows
	if end > len(m.recent) {
		end = len(m.recent)
	}
	for i := start; i < end; i++ {
		r := m.recent[i]
		title := runewidth.Truncate(r.Title, w/2, "…")
		meta := theme.Sub.Render("  " + relTime(r.ViewedAt))
		row := title + meta
		if i == m.recentCursor {
			b.WriteString("▸ " + lipgloss.NewStyle().Bold(true).Render(row))
		} else {
			b.WriteString("  " + row)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// --- small helpers --------------------------------------------------------

// padPaneLines clips/pads a set of lines to exactly w×h so JoinHorizontal
// aligns the gutter and right pane regardless of content length. Each line is
// ansi-truncated to w (preserving SGR runs) then space-padded.
func padPaneLines(lines []string, w, h int) string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = padTo(ansi.Truncate(lines[i], w, ""), w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return strings.Join(out, "\n")
}

func padTo(s string, w int) string {
	cur := lipgloss.Width(s)
	if cur >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cur)
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
