package screens

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Gallery is the .ans browser. Lists every *.ans file under
// sess.GalleryProvider.Dir; arrow keys / hjkl flip between pieces; Enter
// re-lists (so a sysop dropping a new file mid-session sees it without a
// restart). Esc returns to the lobby.
type Gallery struct {
	sess    *session.Session
	entries []art.GalleryEntry
	cursor  int
	grid    *art.CellGrid // currently loaded piece
	err     string
}

func NewGallery(sess *session.Session) tea.Model {
	g := &Gallery{sess: sess}
	g.reload()
	g.loadCurrent()
	return g
}

func (m *Gallery) reload() {
	if m.sess.GalleryProvider == nil {
		m.entries = nil
		m.err = "gallery directory not configured (set NIGHTMS_ART_DIR)"
		return
	}
	entries, err := m.sess.GalleryProvider.List()
	if err != nil {
		m.err = err.Error()
		return
	}
	m.entries = entries
	m.cursor = clampIndex(m.cursor, len(m.entries))
	m.err = ""
}

func (m *Gallery) loadCurrent() {
	m.grid = nil
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return
	}
	g, err := art.LoadFile(m.entries[m.cursor].Path)
	if err != nil {
		m.err = err.Error()
		return
	}
	m.grid = g
}

func (m *Gallery) Init() tea.Cmd { return nil }

func (m *Gallery) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, nav.Navigate(nav.DestLobby)
		case "left", "h":
			if len(m.entries) > 0 {
				m.cursor = (m.cursor - 1 + len(m.entries)) % len(m.entries)
				m.loadCurrent()
			}
		case "right", "l":
			if len(m.entries) > 0 {
				m.cursor = (m.cursor + 1) % len(m.entries)
				m.loadCurrent()
			}
		case "enter":
			// Re-enumerate so a freshly-dropped file appears.
			m.reload()
			m.loadCurrent()
		}
		// Digit 1-9 jumps directly.
		if len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if r >= '1' && r <= '9' {
				idx := int(r - '1')
				if idx < len(m.entries) {
					m.cursor = idx
					m.loadCurrent()
				}
			}
		}
	}
	return m, nil
}

var (
	galleryTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	galleryHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	galleryName  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	galleryErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *Gallery) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var b strings.Builder
	b.WriteString(galleryTitle.Render("Gallery") + "  " + galleryHint.Render("←/→ flip · 1-9 jump · Enter re-list · Esc back"))
	b.WriteString("\n")
	switch {
	case m.err != "" && len(m.entries) == 0:
		b.WriteString(galleryErr.Render("! " + m.err))
		return b.String()
	case len(m.entries) == 0:
		b.WriteString(galleryHint.Render("no .ans files in the gallery directory yet"))
		return b.String()
	}
	cur := m.entries[m.cursor]
	b.WriteString(galleryName.Render(cur.Title) + "  " + galleryHint.Render(positionLabel(m.cursor+1, len(m.entries))))
	b.WriteString("\n\n")
	if m.grid != nil {
		b.WriteString(components.RenderCellGrid(m.grid))
	} else if m.err != "" {
		b.WriteString(galleryErr.Render("! " + m.err))
	}
	return b.String()
}

func positionLabel(i, total int) string {
	return strings.Replace("(? of ?)", "(?", "("+formatInt(i), 1) + " of " + formatInt(total) + ")"
}

func formatInt(i int) string {
	// One-line int->string without bringing in fmt for a hot path. Gallery
	// position labels are typically single-digit so a small buffer suffices.
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [16]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
