package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/providers/maptile"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// MapScreen renders an OpenStreetMap raster tile centered on the user's
// configured weather coordinate. The tile is binarized (mean-luminance
// threshold) and plotted on a BrailleCanvas; sparse foreground pixels
// produce a recognizable continent outline.
//
// Falls back to a synthetic-geometry demo when the provider is unavailable
// (offline / OSM down / no MapTiles configured), so the screen always
// paints something.
type MapScreen struct {
	sess *session.Session

	zoom int // OSM zoom level, 2..6 (city → continent)
	tile *maptile.Tile
	err  string
}

func NewMapScreen(sess *session.Session) tea.Model {
	return &MapScreen{sess: sess, zoom: 3}
}

type mapTileLoadedMsg struct {
	tile *maptile.Tile
	err  error
}

func (m *MapScreen) Init() tea.Cmd { return m.fetchTile() }

func (m *MapScreen) fetchTile() tea.Cmd {
	provider := m.sess.MapTiles
	lat, lon, _ := m.sess.WeatherCoords()
	z := m.zoom
	return func() tea.Msg {
		if provider == nil {
			return mapTileLoadedMsg{err: fmt.Errorf("tile provider unavailable")}
		}
		ctx, cancel := m.sess.CtxWithTimeout(10*time.Second)
		defer cancel()
		xf, yf := maptile.LatLonToTile(lat, lon, z)
		t, err := provider.Tile(ctx, z, int(xf), int(yf))
		return mapTileLoadedMsg{tile: t, err: err}
	}
}

func (m *MapScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case mapTileLoadedMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.tile = nil
			return m, nil
		}
		m.err = ""
		m.tile = msg.tile
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestLobby)
		case "+", "=":
			if m.zoom < 6 {
				m.zoom++
				return m, m.fetchTile()
			}
		case "-", "_":
			if m.zoom > 2 {
				m.zoom--
				return m, m.fetchTile()
			}
		case "r":
			return m, m.fetchTile()
		}
	}
	return m, nil
}

var (
	mapTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	mapHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	mapInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	mapErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *MapScreen) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	lat, lon, label := m.sess.WeatherCoords()
	var b strings.Builder
	b.WriteString(mapTitle.Render("Map") + "  " + mapHint.Render("+/- zoom · r refresh · Esc back"))
	b.WriteString("\n")
	b.WriteString(mapInfo.Render(fmt.Sprintf("%s · %.3f, %.3f · z%d", label, lat, lon, m.zoom)))
	b.WriteString("\n\n")

	cols := m.sess.Width - 2
	rows := m.sess.Height - 6
	if cols < 10 {
		cols = 10
	}
	if rows < 5 {
		rows = 5
	}
	canvas := components.NewBrailleCanvas(cols, rows)

	if m.tile != nil && len(m.tile.Pixels) > 0 {
		plotTile(canvas, m.tile)
		// Crosshair through center to mark the user's coordinate.
		pxW, pxH := canvas.PixelDims()
		canvas.Line(pxW/2-3, pxH/2, pxW/2+3, pxH/2)
		canvas.Line(pxW/2, pxH/2-3, pxW/2, pxH/2+3)
	} else {
		// Fallback to synthetic demo if no tile loaded.
		pxW, pxH := canvas.PixelDims()
		cx, cy := pxW/2, pxH/2
		for i := 1; i <= 3; i++ {
			canvas.Circle(cx, cy, i*pxH/8)
		}
		canvas.Line(0, cy, pxW-1, cy)
		canvas.Line(cx, 0, cx, pxH-1)
	}

	b.WriteString(canvas.Render())
	b.WriteString("\n")
	if m.err != "" {
		b.WriteString(mapErr.Render("! " + m.err))
	} else if m.tile != nil {
		b.WriteString(mapHint.Render(fmt.Sprintf("OSM tile %d/%d/%d · © OpenStreetMap contributors",
			m.tile.Z, m.tile.X, m.tile.Y)))
	} else {
		b.WriteString(mapHint.Render("loading tile…"))
	}
	return b.String()
}

// plotTile rescales the tile's foreground pixels onto the canvas via nearest-
// neighbor sampling. The canvas's pixel dims are usually smaller than the
// tile's 256x256 source, so this is a downsample.
func plotTile(canvas *components.BrailleCanvas, t *maptile.Tile) {
	pxW, pxH := canvas.PixelDims()
	if pxW <= 0 || pxH <= 0 || t.Width <= 0 || t.Height <= 0 {
		return
	}
	for y := 0; y < pxH; y++ {
		srcY := y * t.Height / pxH
		for x := 0; x < pxW; x++ {
			srcX := x * t.Width / pxW
			if t.Pixels[srcY*t.Width+srcX] {
				canvas.Set(x, y)
			}
		}
	}
}
