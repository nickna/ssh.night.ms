package screens

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sync/errgroup"

	"github.com/nickna/ssh.night.ms/internal/imaging"
	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/providers/maptile"
	"github.com/nickna/ssh.night.ms/internal/providers/routing"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// MapScreen renders a multi-tile OpenStreetMap mosaic as a half-block color
// image. Tiles are fetched in parallel and composited into an RGBA buffer
// before half-block conversion, so the same render path can layer overlays
// (route polyline, markers) on top of the map without fighting the cell
// granularity. Arrow keys pan; +/- zoom from continent (z=2) down to street
// level (z=18); r refreshes; Esc returns to the lobby.
type MapScreen struct {
	sess *session.Session

	centerLat, centerLon float64
	zoom                 int

	mosaicLines []string
	loading     bool
	err         string

	viewW, viewH int

	// fetchSeq discards stale fetch results when the user pans/zooms before
	// the previous mosaic finishes composing.
	fetchSeq int

	// Sub-mode routing — subBrowse handles the normal map; subSearch hides
	// the map behind a geocoder modal until the user picks a result or
	// dismisses with Esc.
	sub mapSubMode

	// Geocoder search modal state. searchInput owns the text cursor;
	// searchResults populates the numbered picker shown beneath it.
	searchInput   textinput.Model
	searchResults []geocoding.Result
	searching     bool
	searchErr     string

	// Saved-location cycle: `l` advances savedLocIdx through savedLocs (in
	// sort_order from LocationService.List). Empty list raises a toast.
	savedLocs   []realtime.SavedLocation
	savedLocIdx int

	// Toast surfaces transient confirmations ("→ Tokyo, Japan") for two
	// seconds after a jump. toastExpires guards against an older tea.Tick
	// clearing a newer toast.
	toast        string
	toastExpires time.Time

	// Directions modal state (sub == subDirections). destInput owns the
	// query field; destSavedPick toggles between "type a place" and "pick
	// a saved location" while the modal is open.
	destInput     textinput.Model
	destResults   []geocoding.Result
	destSearching bool
	destErr       string
	destSavedPick bool

	// Active travel mode + last computed route. mode cycles via `m`.
	// route is set on routing.Provider.Route success and used by refresh()
	// to draw the polyline + endpoint markers into the mosaic.
	mode     routing.Mode
	route    *routing.Route
	routeErr error

	// Steps overlay (only meaningful when route != nil). showSteps toggles
	// the side panel; stepsScroll tracks the first visible step.
	showSteps   bool
	stepsScroll int
}

type mapSubMode int

const (
	subBrowse mapSubMode = iota
	subSearch
	subDirections
)

func NewMapScreen(sess *session.Session) tea.Model {
	// Use the user's saved location as the initial center; sessions without
	// one open on a neutral world-ish view (slightly north of the equator,
	// prime meridian) and let the user search/pan from there.
	lat, lon, _, ok := sess.WeatherCoords()
	if !ok {
		lat, lon = 20, 0
	}
	ti := textinput.New()
	ti.Placeholder = "city, address, or place"
	ti.CharLimit = 96
	ti.Width = 40
	dti := textinput.New()
	dti.Placeholder = "destination"
	dti.CharLimit = 96
	dti.Width = 40
	return &MapScreen{
		sess:        sess,
		centerLat:   lat,
		centerLon:   lon,
		zoom:        3,
		searchInput: ti,
		destInput:   dti,
		mode:        routing.ModeDriving,
	}
}

const (
	mapMinZoom = 2
	mapMaxZoom = 18
	mapPanStep = 64

	mapMaxLat = 85.0511

	// Steps panel geometry — width of the panel content plus its border;
	// computeViewportDims subtracts this from the available map width when
	// the panel is visible. Tuned to fit a "  1. Turn left onto…" line at
	// reasonable truncation.
	stepsPanelWidth = 30
	stepsPanelTotal = stepsPanelWidth + 2 // border + 1 cell gap
)

type mapMosaicLoadedMsg struct {
	seq   int
	lines []string
	err   error
}

type mapSavedLoadedMsg struct {
	locs []realtime.SavedLocation
	err  error
}

type mapToastExpireMsg struct {
	at time.Time
}

// mapRouteLoadedMsg carries the result of routing.Provider.Route. We don't
// sequence-number these (unlike mosaic fetches) because routing only fires
// from explicit user actions — pan/zoom doesn't churn them.
type mapRouteLoadedMsg struct {
	route *routing.Route
	err   error
}

func (m *MapScreen) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.loadSavedLocations())
}

// loadSavedLocations pre-fetches the user's saved-location list so the `l`
// cycle has something to walk through on first press. Empty / unauthenticated
// users land with savedLocs nil and `l` shows a "no saved locations" toast.
func (m *MapScreen) loadSavedLocations() tea.Cmd {
	svc := m.sess.Locations
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		if svc == nil || userID == 0 {
			return mapSavedLoadedMsg{}
		}
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		locs, err := svc.List(ctx, userID)
		return mapSavedLoadedMsg{locs: locs, err: err}
	}
}

// setToast publishes a transient confirmation line and returns the Cmd that
// will clear it ~2 s later. Callers that already return Cmds should batch
// this in.
func (m *MapScreen) setToast(text string) tea.Cmd {
	m.toast = text
	m.toastExpires = time.Now().Add(2 * time.Second)
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return mapToastExpireMsg{at: t}
	})
}

// jumpTo recenters the map on (lat, lon) and queues a refresh. Used by
// search-result pick and saved-location cycle.
func (m *MapScreen) jumpTo(lat, lon float64) tea.Cmd {
	m.centerLat = lat
	m.centerLon = lon
	return m.refresh()
}

// cycleSavedLocation advances to the next saved location and re-centers.
// First press lands on index 0 (★ primary); subsequent presses walk the
// list. Empty list → toast only.
func (m *MapScreen) cycleSavedLocation() (tea.Model, tea.Cmd) {
	if len(m.savedLocs) == 0 {
		return m, m.setToast("no saved locations")
	}
	m.savedLocIdx = (m.savedLocIdx + 1) % len(m.savedLocs)
	loc := m.savedLocs[m.savedLocIdx]
	cmds := []tea.Cmd{m.jumpTo(loc.Latitude, loc.Longitude), m.setToast("→ " + loc.Label)}
	return m, tea.Batch(cmds...)
}

// computeViewportDims sizes the mosaic to the current PTY, leaving room for
// the title/info header and the OSM-attribution footer. viewH is the cell
// row count doubled because half-block packs two source pixels per row.
// When the steps overlay is visible, the panel eats stepsPanelTotal cols
// from the map's width — the tile cache makes the refetch effectively free.
func (m *MapScreen) computeViewportDims() {
	cols := m.sess.Width - 2
	if m.showSteps && m.route != nil {
		cols -= stepsPanelTotal
	}
	rows := m.sess.Height - 6
	if cols < 20 {
		cols = 20
	}
	if rows < 6 {
		rows = 6
	}
	m.viewW = cols
	m.viewH = rows * 2
}

// refresh kicks off a fresh tile-fetch + compose round. Returns nil when the
// session has no PTY size yet — Init() will land before WindowSize on a cold
// boot, and the parent route() that constructed us has already seen
// WindowSize by the time we navigate here from the lobby.
func (m *MapScreen) refresh() tea.Cmd {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return nil
	}
	m.computeViewportDims()
	m.loading = true
	m.fetchSeq++
	seq := m.fetchSeq

	provider := m.sess.MapTiles
	centerLat, centerLon := m.centerLat, m.centerLon
	z, viewW, viewH := m.zoom, m.viewW, m.viewH
	route := m.route
	mode := m.mode

	return func() tea.Msg {
		if provider == nil {
			return mapMosaicLoadedMsg{seq: seq, err: errors.New("tile provider unavailable")}
		}
		ctx, cancel := m.sess.CtxWithTimeout(10 * time.Second)
		defer cancel()

		keys, originX, originY := maptile.ViewportTiles(centerLat, centerLon, z, viewW, viewH)
		composed := image.NewRGBA(image.Rect(0, 0, viewW, viewH))
		// Dark background — missing tiles paint as a hole rather than white,
		// which would clash with the BBS palette.
		draw.Draw(composed, composed.Bounds(), &image.Uniform{C: bgFill}, image.Point{}, draw.Src)

		fetched := make([]image.Image, len(keys))
		g, gctx := errgroup.WithContext(ctx)
		for i, k := range keys {
			i, k := i, k
			g.Go(func() error {
				tile, err := provider.Tile(gctx, k.Z, k.X, k.Y)
				if err == nil && tile != nil {
					fetched[i] = tile.Image
				}
				// Swallow per-tile errors — a partial mosaic is acceptable.
				return nil
			})
		}
		_ = g.Wait()

		for i, img := range fetched {
			if img == nil {
				continue
			}
			k := keys[i]
			dst := image.Rect(k.DrawX, k.DrawY, k.DrawX+256, k.DrawY+256)
			draw.Draw(composed, dst, img, image.Point{}, draw.Src)
		}

		// Route + endpoint markers go on top of the tiles but underneath the
		// crosshair so the center indicator remains visible even when the
		// polyline passes directly through the viewport center.
		if route != nil && len(route.Coordinates) > 1 {
			pts := make([]image.Point, 0, len(route.Coordinates))
			for _, c := range route.Coordinates {
				gx, gy := maptile.LatLonToPixel(c.Lat, c.Lon, z)
				pts = append(pts, image.Point{X: int(gx) - originX, Y: int(gy) - originY})
			}
			imaging.DrawPolyline(composed, pts, 1, routeColorFor(mode))
			imaging.DrawMarker(composed, pts[0].X, pts[0].Y, 4, markerStart)
			imaging.DrawMarker(composed, pts[len(pts)-1].X, pts[len(pts)-1].Y, 4, markerEnd)
		}

		imaging.DrawCrosshair(composed, viewW/2, viewH/2, 3, crosshairColor)

		lines := imaging.RenderToANSILines(composed, viewW)
		return mapMosaicLoadedMsg{seq: seq, lines: lines}
	}
}

// pan shifts the center by (dxPx, dyPx) global pixels at the current zoom,
// clamping latitude to the slippy-map valid range so we never request tiles
// with y < 0 or y >= 2^z.
func (m *MapScreen) pan(dxPx, dyPx int) {
	px, py := maptile.LatLonToPixel(m.centerLat, m.centerLon, m.zoom)
	px += float64(dxPx)
	py += float64(dyPx)
	lat, lon := maptile.PixelToLatLon(px, py, m.zoom)
	if lat > mapMaxLat {
		lat = mapMaxLat
	} else if lat < -mapMaxLat {
		lat = -mapMaxLat
	}
	// Wrap longitude to [-180, 180) so the displayed coords stay sane after
	// crossing the antimeridian.
	for lon < -180 {
		lon += 360
	}
	for lon >= 180 {
		lon -= 360
	}
	m.centerLat, m.centerLon = lat, lon
}

func (m *MapScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case mapMosaicLoadedMsg:
		if msg.seq != m.fetchSeq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			m.mosaicLines = nil
			return m, nil
		}
		m.err = ""
		m.mosaicLines = msg.lines
		return m, nil

	case mapSavedLoadedMsg:
		if msg.err == nil {
			m.savedLocs = msg.locs
		}
		return m, nil

	case mapSearchMsg:
		return m.handleSearchResult(msg)

	case mapDestSearchMsg:
		return m.handleDestSearchResult(msg)

	case mapRouteLoadedMsg:
		return m.handleRouteLoaded(msg)

	case mapToastExpireMsg:
		if !msg.at.Before(m.toastExpires) {
			m.toast = ""
		}
		return m, nil

	case tea.WindowSizeMsg:
		// Root has already mutated sess.Width/Height; refetch at the new
		// viewport dims. computeViewportDims handles the actual sizing.
		return m, m.refresh()

	case tea.KeyMsg:
		switch m.sub {
		case subSearch:
			return m.handleSearchKey(msg)
		case subDirections:
			return m.handleDirectionsKey(msg)
		default:
			return m.handleBrowseKey(msg)
		}
	}
	return m, nil
}

func (m *MapScreen) handleBrowseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, nav.Navigate(nav.DestLobby)
	case "+", "=":
		if m.zoom < mapMaxZoom {
			m.zoom++
			return m, m.refresh()
		}
	case "-", "_":
		if m.zoom > mapMinZoom {
			m.zoom--
			return m, m.refresh()
		}
	case "up":
		m.pan(0, -mapPanStep)
		return m, m.refresh()
	case "down":
		m.pan(0, mapPanStep)
		return m, m.refresh()
	case "left":
		m.pan(-mapPanStep, 0)
		return m, m.refresh()
	case "right":
		m.pan(mapPanStep, 0)
		return m, m.refresh()
	case "r":
		return m, m.refresh()
	case "/":
		return m, m.openSearch()
	case "l":
		return m.cycleSavedLocation()
	case "d":
		return m, m.openDirections()
	case "m":
		return m.cycleMode()
	case "c":
		if m.route != nil {
			m.route = nil
			m.routeErr = nil
			// Closing the side panel too — there's nothing to show without
			// a route. The next `i` press will toast "no route" instead.
			m.showSteps = false
			m.stepsScroll = 0
			return m, tea.Batch(m.refresh(), m.setToast("route cleared"))
		}
	case "i":
		if m.route == nil {
			return m, m.setToast("no route — press d for directions")
		}
		m.showSteps = !m.showSteps
		if !m.showSteps {
			m.stepsScroll = 0
		}
		return m, m.refresh()
	case "j", "pgdown":
		if m.showSteps && m.route != nil {
			m.scrollSteps(+1)
		}
	case "k", "pgup":
		if m.showSteps && m.route != nil {
			m.scrollSteps(-1)
		}
	}
	return m, nil
}

// scrollSteps moves stepsScroll by delta, clamped to keep the visible
// window inside the steps list. Step rows shown is derived in the panel
// renderer; we conservatively allow scrolling by 1 step at a time.
func (m *MapScreen) scrollSteps(delta int) {
	if m.route == nil {
		return
	}
	total := len(m.route.Steps)
	visible := m.visibleStepRows()
	maxScroll := total - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.stepsScroll += delta
	if m.stepsScroll < 0 {
		m.stepsScroll = 0
	}
	if m.stepsScroll > maxScroll {
		m.stepsScroll = maxScroll
	}
}

var (
	mapTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	mapHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	mapInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	mapErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	mapToast = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorCyan))

	// crosshairColor is theme.ColorAccent (#FF7DB0) as RGBA. Defined here as
	// a raw color.RGBA because the half-block compositor works in pixel
	// space, not lipgloss styles.
	crosshairColor = color.RGBA{R: 255, G: 125, B: 176, A: 255}
	bgFill         = color.RGBA{R: 14, G: 11, B: 22, A: 255}

	// Route polyline colors — picked from the theme palette so the line
	// reads against the OSM tile background. driving=cyan, walking=yellow,
	// cycling=soft purple (different enough from the pink crosshair).
	routeDrive = color.RGBA{R: 94, G: 231, B: 223, A: 255}  // #5EE7DF
	routeWalk  = color.RGBA{R: 255, G: 209, B: 102, A: 255} // #FFD166
	routeCycle = color.RGBA{R: 197, G: 128, B: 224, A: 255} // soft purple

	// Endpoint markers — bright green for origin, bright red for destination.
	markerStart = color.RGBA{R: 40, G: 230, B: 100, A: 255}
	markerEnd   = color.RGBA{R: 230, G: 60, B: 80, A: 255}
)

func routeColorFor(mode routing.Mode) color.RGBA {
	switch mode {
	case routing.ModeWalking:
		return routeWalk
	case routing.ModeCycling:
		return routeCycle
	default:
		return routeDrive
	}
}

// cycleMode advances to the next travel mode. If a route is already loaded,
// the new mode triggers a fresh fetch so the polyline + summary reflect the
// chosen profile. Without a route, just toasts the new mode for feedback.
func (m *MapScreen) cycleMode() (tea.Model, tea.Cmd) {
	prev := m.mode
	switch m.mode {
	case routing.ModeDriving:
		m.mode = routing.ModeWalking
	case routing.ModeWalking:
		m.mode = routing.ModeCycling
	default:
		m.mode = routing.ModeDriving
	}
	if m.route == nil {
		return m, m.setToast(fmt.Sprintf("mode: %s", m.mode.Label()))
	}
	dest := m.route.Coordinates[len(m.route.Coordinates)-1]
	origin := m.route.Coordinates[0]
	cmds := []tea.Cmd{
		m.requestRoute(routing.LatLon{Lat: origin.Lat, Lon: origin.Lon},
			routing.LatLon{Lat: dest.Lat, Lon: dest.Lon}, m.mode),
		m.setToast(fmt.Sprintf("%s → %s · routing…", prev.Label(), m.mode.Label())),
	}
	return m, tea.Batch(cmds...)
}

// requestRoute dispatches a routing.Provider.Route call. The result lands
// as a mapRouteLoadedMsg which handleRouteLoaded translates into either a
// new route (with a refreshed mosaic) or an error toast.
func (m *MapScreen) requestRoute(origin, dest routing.LatLon, mode routing.Mode) tea.Cmd {
	svc := m.sess.Routing
	return func() tea.Msg {
		if svc == nil {
			return mapRouteLoadedMsg{err: routing.ErrRoutingDisabled}
		}
		ctx, cancel := m.sess.CtxWithTimeout(12 * time.Second)
		defer cancel()
		route, err := svc.Route(ctx, origin, dest, mode)
		return mapRouteLoadedMsg{route: route, err: err}
	}
}

// handleRouteLoaded installs the route on success, surfaces an error toast
// on failure, then refreshes the mosaic so the new polyline lands.
func (m *MapScreen) handleRouteLoaded(msg mapRouteLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.routeErr = msg.err
		if errors.Is(msg.err, routing.ErrRoutingDisabled) {
			return m, m.setToast("routing disabled — see operator")
		}
		return m, m.setToast("route failed: " + msg.err.Error())
	}
	m.route = msg.route
	m.routeErr = nil
	return m, tea.Batch(m.refresh(), m.setToast("route ready"))
}

// formatRouteSummary renders "drive · 12.3 km · 18 min" for the header.
// Units pick between m and km / s and min based on magnitude.
func formatRouteSummary(r *routing.Route) string {
	if r == nil {
		return ""
	}
	var distStr string
	if r.DistanceMeters >= 1000 {
		distStr = fmt.Sprintf("%.1f km", r.DistanceMeters/1000)
	} else {
		distStr = fmt.Sprintf("%.0f m", r.DistanceMeters)
	}
	var durStr string
	mins := int(r.DurationSeconds / 60)
	switch {
	case mins >= 60:
		durStr = fmt.Sprintf("%dh%02dm", mins/60, mins%60)
	case mins >= 1:
		durStr = fmt.Sprintf("%d min", mins)
	default:
		durStr = fmt.Sprintf("%.0fs", r.DurationSeconds)
	}
	return r.Mode.Label() + " · " + distStr + " · " + durStr
}

func (m *MapScreen) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	switch m.sub {
	case subSearch:
		return m.renderSearchView()
	case subDirections:
		return m.renderDirectionsView()
	}
	return m.renderBrowseView()
}

func (m *MapScreen) renderBrowseView() string {
	var b strings.Builder
	hint := "arrows pan · +/- zoom · / search · l saved · d directions · m mode · r refresh · Esc back"
	if m.route != nil {
		hint = "arrows pan · +/- zoom · / search · l saved · d directions · m mode · i steps · c clear · r refresh · Esc back"
	}
	b.WriteString(mapTitle.Render("Map") + "  " + mapHint.Render(hint))
	b.WriteString("\n")
	info := fmt.Sprintf("%.4f, %.4f · z%d", m.centerLat, m.centerLon, m.zoom)
	if m.route != nil {
		info += "  ·  " + formatRouteSummary(m.route)
	} else {
		info += "  ·  " + m.mode.Label()
	}
	b.WriteString(mapInfo.Render(info))
	if m.toast != "" {
		b.WriteString("   " + mapToast.Render(m.toast))
	}
	b.WriteString("\n\n")

	var mapBlock string
	if len(m.mosaicLines) > 0 {
		mapBlock = strings.Join(m.mosaicLines, "\n")
	} else {
		mapBlock = mapHint.Render("loading tiles…")
	}
	if m.showSteps && m.route != nil {
		panel := m.renderStepsPanel()
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, mapBlock, " ", panel))
	} else {
		b.WriteString(mapBlock)
	}

	b.WriteString("\n")
	if m.err != "" {
		b.WriteString(mapErr.Render("! " + m.err))
	} else {
		b.WriteString(mapHint.Render("© OpenStreetMap contributors"))
	}
	return b.String()
}
