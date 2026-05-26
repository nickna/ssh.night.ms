package screens

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// bookmarkEditor is the inline title editor used for both "add bookmark" and
// "rename bookmark". The URL is fixed once the editor opens — the title is
// the only field the user can edit.
//
// bookmarkID == 0 means "this is an Add"; non-zero means "this is a Rename"
// of an existing row.
type bookmarkEditor struct {
	bookmarkID int64
	url        string
	title      textinput.Model
}

func (m *Web) openEditorForAdd(url string) {
	in := textinput.New()
	in.SetValue(defaultBookmarkTitle(url))
	in.CharLimit = 120
	in.Focus()
	m.editor = &bookmarkEditor{bookmarkID: 0, url: url, title: in}
	m.input.Blur()
	m.status = ""
}

func (m *Web) openEditorForRename(id int64, url, currentTitle string) {
	in := textinput.New()
	if currentTitle == "" {
		in.SetValue(defaultBookmarkTitle(url))
	} else {
		in.SetValue(currentTitle)
	}
	in.CharLimit = 120
	in.Focus()
	m.editor = &bookmarkEditor{bookmarkID: id, url: url, title: in}
	m.input.Blur()
	m.status = ""
}

func (m *Web) handleEditorKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.editor = nil
		if m.focus == focusURL {
			m.input.Focus()
		}
		return m, nil
	case "enter":
		title := strings.TrimSpace(m.editor.title.Value())
		if title == "" {
			title = defaultBookmarkTitle(m.editor.url)
		}
		return m, m.saveEditor(title)
	}
	var cmd tea.Cmd
	m.editor.title, cmd = m.editor.title.Update(k)
	return m, cmd
}

func (m *Web) saveEditor(title string) tea.Cmd {
	queries := m.sess.Queries
	uid := m.sess.Identity.UserID
	url := m.editor.url
	id := m.editor.bookmarkID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if id == 0 {
			_, err := queries.AddWebBookmark(ctx, gen.AddWebBookmarkParams{
				UserID: uid, Url: url, Title: title,
			})
			return bookmarkSavedMsg{err: err}
		}
		err := queries.RenameWebBookmark(ctx, gen.RenameWebBookmarkParams{
			UserID: uid, ID: id, Title: title,
		})
		return bookmarkSavedMsg{err: err}
	}
}

func (m *Web) renderEditor() string {
	var b strings.Builder
	headLine := "★ "
	if m.editor.bookmarkID == 0 {
		headLine += "Add bookmark"
	} else {
		headLine += "Rename bookmark"
	}
	b.WriteString(webHead.Render(headLine))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(webPrompt.Render("URL  "))
	b.WriteString(webDim.Render(compactURL(m.editor.url)))
	b.WriteString("\n  ")
	b.WriteString(webPrompt.Render("title"))
	b.WriteString("  ")
	b.WriteString(m.editor.title.View())
	b.WriteString("\n\n  ")
	b.WriteString(webHint.Render("Enter save · Esc cancel"))
	b.WriteString("\n")
	return b.String()
}
