package screens

import (
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/onenote"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// --- quick append ---------------------------------------------------------

func (m *OneNote) startAppend() (tea.Model, tea.Cmd) {
	if m.curPage == nil {
		return m, nil
	}
	in := textinput.New()
	in.Placeholder = "a line to append… (markdown ok)"
	in.CharLimit = 4000
	in.Width = min(m.sess.Width-8, 80)
	in.Focus()
	m.input = in
	m.mode = onModeQuickAppend
	return m, textinput.Blink
}

func (m *OneNote) handleAppendKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = onModeReader
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			m.mode = onModeReader
			return m, nil
		}
		pageID := m.curPage.ID
		m.loading = true
		m.mode = onModeReader
		return m, m.appendBlockCmd(pageID, text)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *OneNote) renderAppendModal() string {
	inner := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent)).Render("Append to page") +
		"\n\n" + m.input.View() + "\n\n" +
		lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color(theme.ColorDim)).Render("Enter append · Esc cancel")
	return theme.ModalFrame.Width(min(m.sess.Width-6, 70)).Render(inner)
}

// --- full edit ------------------------------------------------------------

func (m *OneNote) startEdit() (tea.Model, tea.Cmd) {
	if m.curPage == nil {
		return m, nil
	}
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(min(m.sess.Width-4, 100))
	ta.SetHeight(max(m.sess.Height-6, 6))
	ta.SetValue(m.curPage.EditMarkdown())
	ta.Focus()
	m.area = ta
	m.editPageID = m.curPage.ID
	m.mode = onModeEdit
	return m, textarea.Blink
}

func (m *OneNote) handleEditKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = onModeReader
		return m, nil
	case "ctrl+s":
		md := m.area.Value()
		m.pendingMD = md
		m.loading = true
		m.mode = onModeReader
		return m, m.replaceBodyCmd(m.editPageID, md, false)
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(k)
	return m, cmd
}

// --- create ---------------------------------------------------------------

func (m *OneNote) startCreate() (tea.Model, tea.Cmd) {
	secID, ok := m.currentSectionID()
	if !ok {
		m.notice = "expand a notebook and select a section to create a page in"
		m.noticeKind = "err"
		return m, nil
	}
	title := textinput.New()
	title.Placeholder = "page title"
	title.CharLimit = 200
	title.Width = min(m.sess.Width-8, 60)
	title.Focus()
	m.input = title

	body := textarea.New()
	body.ShowLineNumbers = false
	body.Placeholder = "page body — markdown"
	body.SetWidth(min(m.sess.Width-4, 100))
	body.SetHeight(max(m.sess.Height-8, 5))
	m.area = body

	m.createFocus = 0
	m.editSection = secID
	m.mode = onModeCreate
	return m, textinput.Blink
}

func (m *OneNote) handleCreateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = onModeBrowse
		return m, nil
	case "tab":
		m.createFocus = (m.createFocus + 1) % 2
		if m.createFocus == 0 {
			m.area.Blur()
			m.input.Focus()
		} else {
			m.input.Blur()
			m.area.Focus()
		}
		return m, nil
	case "ctrl+s":
		title := strings.TrimSpace(m.input.Value())
		if title == "" {
			m.notice = "a title is required"
			m.noticeKind = "err"
			return m, nil
		}
		m.loading = true
		m.mode = onModeBrowse
		return m, m.createPageCmd(m.editSection, title, m.area.Value())
	}
	var cmd tea.Cmd
	if m.createFocus == 0 {
		m.input, cmd = m.input.Update(k)
	} else {
		m.area, cmd = m.area.Update(k)
	}
	return m, cmd
}

func (m *OneNote) viewEditor() string {
	var b strings.Builder
	if m.mode == onModeCreate {
		b.WriteString(theme.Title.Render("New page"))
		b.WriteString("\n")
		b.WriteString(theme.Hint.Render("Tab switch field · Ctrl+S create · Esc cancel"))
		b.WriteString("\n\n")
		label := func(s string, on bool) string {
			st := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
			if on {
				st = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
			}
			return st.Render(s)
		}
		b.WriteString(label("Title", m.createFocus == 0) + "\n")
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(label("Body", m.createFocus == 1) + "\n")
		b.WriteString(m.area.View())
		return b.String()
	}
	// onModeEdit
	b.WriteString(theme.Title.Render("Edit page"))
	b.WriteString("\n")
	b.WriteString(theme.Hint.Render("Ctrl+S save · Esc cancel"))
	b.WriteString("\n\n")
	b.WriteString(m.area.View())
	return b.String()
}

// --- delete ---------------------------------------------------------------

func (m *OneNote) startDeleteCurrent() (tea.Model, tea.Cmd) {
	if m.curPage == nil {
		return m, nil
	}
	return m.openDeleteConfirm(m.curPage.ID, m.curPage.SectionID, m.curPage.Title)
}

func (m *OneNote) startDeleteFromCursor() (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return m, nil
	}
	n := m.tree[m.cursor]
	if n.kind != nodePage {
		m.notice = "select a page to delete"
		m.noticeKind = "err"
		return m, nil
	}
	return m.openDeleteConfirm(n.id, n.parentID, n.name)
}

func (m *OneNote) openDeleteConfirm(pageID, sectionID, title string) (tea.Model, tea.Cmd) {
	m.editPageID = pageID
	m.editSection = sectionID
	m.confirm = components.NewConfirm("delete page", "delete \""+title+"\"? this cannot be undone.")
	m.confirmKind = "deletePage"
	m.mode = onModeConfirm
	return m, nil
}

// --- confirm dispatch -----------------------------------------------------

func (m *OneNote) handleConfirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm == nil {
		m.mode = onModeBrowse
		return m, nil
	}
	m.confirm.Update(k)
	if m.confirm.Cancelled {
		m.confirm = nil
		m.restoreFromConfirm()
		return m, nil
	}
	if !m.confirm.Submitted {
		return m, nil
	}
	yes := m.confirm.Choice == 1
	kind := m.confirmKind
	m.confirm = nil
	if !yes {
		m.restoreFromConfirm()
		return m, nil
	}
	switch kind {
	case "deletePage":
		m.loading = true
		m.restoreFromConfirm()
		return m, m.deletePageCmd(m.editSection, m.editPageID)
	case "editConfirm":
		m.loading = true
		m.mode = onModeReader
		return m, m.replaceBodyCmd(m.editPageID, m.pendingMD, true)
	}
	m.restoreFromConfirm()
	return m, nil
}

// restoreFromConfirm returns to the reader when a page is open, else the tree.
func (m *OneNote) restoreFromConfirm() {
	if m.curPage != nil {
		m.mode = onModeReader
	} else {
		m.mode = onModeBrowse
	}
}

// --- write commands -------------------------------------------------------

func (m *OneNote) appendBlockCmd(pageID, md string) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(20 * time.Second)
		defer cancel()
		err := svc.AppendBlock(ctx, uid, pageID, md)
		return onWriteResultMsg{kind: "append", page: onenote.Page{ID: pageID}, err: err}
	}
}

func (m *OneNote) replaceBodyCmd(pageID, md string, confirm bool) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(30 * time.Second)
		defer cancel()
		page, err := svc.ReplaceBody(ctx, uid, pageID, md, confirm)
		return onWriteResultMsg{kind: "replace", page: page, err: err}
	}
}

func (m *OneNote) createPageCmd(sectionID, title, md string) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(30 * time.Second)
		defer cancel()
		page, err := svc.CreatePage(ctx, uid, sectionID, onenote.NewPage{Title: title, Markdown: md})
		return onWriteResultMsg{kind: "create", page: page, err: err}
	}
}

func (m *OneNote) deletePageCmd(sectionID, pageID string) tea.Cmd {
	svc, uid := m.sess.OneNote, m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(20 * time.Second)
		defer cancel()
		err := svc.DeletePage(ctx, uid, sectionID, pageID)
		return onWriteResultMsg{kind: "delete", page: onenote.Page{ID: pageID}, err: err}
	}
}

// --- write result handling ------------------------------------------------

func (m *OneNote) handleWriteResult(msg onWriteResultMsg) (tea.Model, tea.Cmd) {
	m.loading = false

	// The non-text rewrite gate comes back as a typed error: open the confirm.
	if msg.kind == "replace" && errors.Is(msg.err, onenote.ErrConfirmRequired) {
		m.confirm = components.NewConfirm("overwrite page",
			"this page has images or tables that a full rewrite will drop. continue?")
		m.confirmKind = "editConfirm"
		m.mode = onModeConfirm
		return m, nil
	}
	if m.handleServiceErr(msg.err) {
		return m, nil
	}

	switch msg.kind {
	case "append":
		m.notice, m.noticeKind = "appended.", "ok"
		return m, m.openPage(msg.page.ID)

	case "replace":
		m.notice, m.noticeKind = "saved.", "ok"
		// Strategy B yields a new page id — re-point the tree node + reopen.
		if msg.page.ID != "" && m.curPage != nil && msg.page.ID != m.curPage.ID {
			if idx := m.indexOf(m.curPage.ID); idx >= 0 {
				m.tree[idx].id = msg.page.ID
				if strings.TrimSpace(msg.page.Title) != "" {
					m.tree[idx].name = msg.page.Title
				}
			}
		}
		target := msg.page.ID
		if target == "" && m.curPage != nil {
			target = m.curPage.ID
		}
		return m, m.openPage(target)

	case "create":
		m.notice, m.noticeKind = "page created.", "ok"
		var cmds []tea.Cmd
		// Refresh the section's children so the new page appears in the tree.
		if idx := m.indexOf(m.editSection); idx >= 0 && m.tree[idx].expanded {
			m.collapseAt(idx)
			m.tree[idx].loading = true
			cmds = append(cmds, m.loadChildren(m.tree[idx]))
		}
		cmds = append(cmds, m.openPage(msg.page.ID))
		return m, tea.Batch(cmds...)

	case "delete":
		m.notice, m.noticeKind = "page deleted.", "ok"
		if idx := m.indexOf(msg.page.ID); idx >= 0 {
			m.tree = append(m.tree[:idx], m.tree[idx+1:]...)
			m.clampCursor()
		}
		if m.curPage != nil && m.curPage.ID == msg.page.ID {
			m.curPage = nil
			m.mode = onModeBrowse
		}
	}
	return m, nil
}
