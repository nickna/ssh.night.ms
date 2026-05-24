// Package components holds reusable Bubble Tea sub-models used across
// screens — Carousel, ChatLog, ANSI art view, sparkline, modal, card art,
// slots cabinet, and so on.
package components

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// CarouselItem is one slot in the carousel. Hotkey is a single rune that
// jumps directly to the item; Destination is what Enter dispatches; Icon is
// the small ANSI-art glyph painted inside the card (may be nil for cards
// that opt out of an icon).
type CarouselItem struct {
	Title       string
	Hotkey      rune
	Destination nav.Destination
	Icon        *art.CellGrid
}

// Geometry constants mirror src/Night.Ms.SshServer/Tui/Views/LobbyCarouselView.cs.
// The selected card is bigger and bottom-aligned with its neighbours; widths
// are tuned so 5 visible cards fit on an 80-col terminal.
const (
	carSelectedWidth     = 20
	carSelectedHeight    = 7
	carUnselectedWidth   = 14
	carUnselectedHeight  = 5
	carGap               = 2
	carRowHeight         = 7
	carSelectedBorderMin = 17 // width at which we flip to the double border / bold weight
	carMaxSlot           = 5
	carAnimDuration      = 200 * time.Millisecond
	carFrameInterval     = 33 * time.Millisecond
)

// Per-slot RGB alpha used to dim neighbour cards by distance. slot 0 = selected.
// Same values as the .NET SlotAlpha table.
var carSlotAlpha = [6]float64{1.00, 0.72, 0.48, 0.30, 0.18, 0.12}

// cardGeom is one card's per-frame placement + alpha. Pixel-space coords
// against the carousel's current viewport width.
type cardGeom struct {
	LeftX, TopY, Width, Height, Alpha float64
}

// Carousel is a horizontal "spotlight" navigator. The selected card sits in
// the center with a bright double border + icon glyph; neighbours fan out to
// either side with progressively dimmer single borders. Selection changes
// drive a 200ms slide+scale tween between the previous and new layouts;
// rapid arrow input retargets from the current interpolated state.
//
// Mouse: the carousel hit-tests left-click releases against the on-screen
// card layout and translates them into the same move/activate actions the
// keyboard binds. Wheel events scroll the selection by one card. Mouse
// support is part of the component contract so callers don't have to
// re-implement coordinate math for every screen that embeds a carousel.
type Carousel struct {
	Items []CarouselItem

	selected int

	// Animation state. animActive flips to true while a tween is in flight;
	// View consults it (along with the elapsed time) to lerp geometry.
	animActive bool
	animStart  time.Time
	animFrom   map[int]cardGeom
	animTo     map[int]cardGeom
	lastWidth  int // viewport width captured at last View; reused by Update

	// Viewport origin on the terminal grid — the (X, Y) coordinate of the
	// carousel's top-left cell after the parent screen has applied any
	// centering / padding. Defaults to (0, 0); parents that center the
	// carousel (Lobby, Doors) call SetViewport every frame so MouseMsg
	// coordinates resolve to the right card. Mouse events that arrive
	// before View has run are ignored (lastWidth == 0).
	viewportX int
	viewportY int
}

// NewCarousel builds a fresh carousel. The first item is selected by default.
func NewCarousel(items []CarouselItem) *Carousel {
	return &Carousel{
		Items:    items,
		animFrom: map[int]cardGeom{},
		animTo:   map[int]cardGeom{},
	}
}

// Selected returns the currently focused item (zero-value if no items).
func (c *Carousel) Selected() CarouselItem {
	if len(c.Items) == 0 {
		return CarouselItem{}
	}
	return c.Items[c.selected]
}

// SetViewport tells the carousel where its top-left corner sits on the
// terminal grid after the parent's centering / padding has been applied.
// Parents call this once per render pass (typically immediately before
// returning from View); subsequent mouse events translate their absolute
// coordinates by subtracting (viewportX, viewportY). Callers that draw the
// carousel flush against the top-left can skip this — the (0, 0) default
// is correct in that case.
func (c *Carousel) SetViewport(x, y int) {
	c.viewportX = x
	c.viewportY = y
}

// SetSelected snaps to an item without animation. Used to restore lobby
// position when returning from a child screen later.
func (c *Carousel) SetSelected(i int) {
	if len(c.Items) == 0 {
		return
	}
	c.selected = ((i % len(c.Items)) + len(c.Items)) % len(c.Items)
	c.animActive = false
}

// carouselTickMsg drives the animation frame loop. Private to this package
// so other screens' tea.Tick events don't collide.
type carouselTickMsg struct{}

// Update returns a tea.Cmd for animation continuation and a Destination ≠
// DestNone when the user activated a card (Enter, Space, or a left-click on
// the selected card). Callers (the Lobby screen, Doors) emit a NavigateMsg
// in response.
func (c *Carousel) Update(msg tea.Msg) (tea.Cmd, nav.Destination) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h":
			c.move(-1)
			return c.startTick(), nav.DestNone
		case "right", "l":
			c.move(1)
			return c.startTick(), nav.DestNone
		case "enter", " ":
			return nil, c.Selected().Destination
		default:
			if len(msg.Runes) == 1 {
				for i, it := range c.Items {
					if matchHotkey(msg.Runes[0], it.Hotkey) {
						c.jumpTo(i)
						return c.startTick(), nav.DestNone
					}
				}
			}
		}
	case tea.MouseMsg:
		return c.handleMouse(msg)
	case carouselTickMsg:
		if !c.animActive {
			return nil, nav.DestNone
		}
		if time.Since(c.animStart) >= carAnimDuration {
			c.animActive = false
			c.animFrom = map[int]cardGeom{}
			c.animTo = map[int]cardGeom{}
			return nil, nav.DestNone
		}
		return c.scheduleTick(), nav.DestNone
	}
	return nil, nav.DestNone
}

// handleMouse routes pointer events. Left-click release on the selected
// card activates it (same Destination as Enter); left-click release on any
// visible neighbour jumps to that card. Wheel-up / wheel-down move the
// selection by one card per tick — the underlying animation system
// retargets cleanly when wheels fire faster than the tween completes.
// Anything else (drag, right-click, middle-click) is ignored so users
// don't get surprises from stray pointer activity.
func (c *Carousel) handleMouse(msg tea.MouseMsg) (tea.Cmd, nav.Destination) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		c.move(-1)
		return c.startTick(), nav.DestNone
	case tea.MouseButtonWheelDown:
		c.move(1)
		return c.startTick(), nav.DestNone
	case tea.MouseButtonLeft:
		// Acting on Release (not Press) matches the chat sidebar — it
		// avoids triggering mid-drag and lets users abort a click by
		// moving off the card before releasing.
		if msg.Action != tea.MouseActionRelease {
			return nil, nav.DestNone
		}
		return c.handleClick(msg.X, msg.Y)
	}
	return nil, nav.DestNone
}

// handleClick hit-tests an absolute (x, y) click against the carousel's
// current card layout. Returns the selected card's Destination if the
// click landed on it, schedules a jump-to animation if a neighbour was
// hit, and is a no-op otherwise. Uses snapshotGeom so mid-tween clicks
// resolve against where the cards visually are right now, not where they
// will be after the animation settles.
func (c *Carousel) handleClick(x, y int) (tea.Cmd, nav.Destination) {
	if c.lastWidth <= 0 || len(c.Items) == 0 {
		return nil, nav.DestNone
	}
	relX := x - c.viewportX
	relY := y - c.viewportY
	if relY < 0 || relY >= carRowHeight {
		return nil, nav.DestNone
	}
	rx, ry := float64(relX), float64(relY)
	geom := c.snapshotGeom(c.lastWidth)
	for idx, g := range geom {
		if rx < g.LeftX || rx >= g.LeftX+g.Width {
			continue
		}
		if ry < g.TopY || ry >= g.TopY+g.Height {
			continue
		}
		if idx == c.selected {
			return nil, c.Items[idx].Destination
		}
		c.jumpTo(idx)
		return c.startTick(), nav.DestNone
	}
	return nil, nav.DestNone
}

func (c *Carousel) move(delta int) {
	if len(c.Items) == 0 {
		return
	}
	target := ((c.selected+delta)%len(c.Items) + len(c.Items)) % len(c.Items)
	c.retargetTo(target, sign(delta))
}

// jumpTo picks the shorter wrap direction so the slide is the shortest
// visible motion. Ties prefer rightward, same as the .NET path.
func (c *Carousel) jumpTo(target int) {
	if target == c.selected || len(c.Items) == 0 {
		return
	}
	n := len(c.Items)
	rightDist := (target - c.selected + n) % n
	leftDist := (c.selected - target + n) % n
	dir := +1
	if rightDist > leftDist {
		dir = -1
	}
	c.retargetTo(target, dir)
}

func (c *Carousel) retargetTo(newIndex, direction int) {
	if newIndex == c.selected && !c.animActive {
		return
	}
	width := c.lastWidth
	// snapshot current geometry as new tween origin
	from := c.snapshotGeom(width)
	c.selected = newIndex
	to := c.buildTargetGeom(width)

	// indices that leave the visible set slide off in `direction`
	for k, fg := range from {
		if _, kept := to[k]; !kept {
			to[k] = offscreenGeom(fg, direction)
		}
	}
	// indices that enter from off-screen come in from -direction
	for k, tg := range to {
		if _, had := from[k]; !had {
			from[k] = offscreenGeom(tg, -direction)
		}
	}

	c.animFrom = from
	c.animTo = to
	c.animStart = time.Now()
	c.animActive = width > 0 // skip animation if we haven't been rendered yet
}

func (c *Carousel) startTick() tea.Cmd {
	if !c.animActive {
		return nil
	}
	return c.scheduleTick()
}

func (c *Carousel) scheduleTick() tea.Cmd {
	return tea.Tick(carFrameInterval, func(time.Time) tea.Msg { return carouselTickMsg{} })
}

// snapshotGeom returns the visible cards' current geometry. When idle the
// snapshot is the static target layout for the selected index; mid-tween it's
// the lerp between animFrom/animTo at the current elapsed time.
func (c *Carousel) snapshotGeom(width int) map[int]cardGeom {
	if !c.animActive {
		return c.buildTargetGeom(width)
	}
	p := c.progress()
	out := map[int]cardGeom{}
	for k, f := range c.animFrom {
		t, ok := c.animTo[k]
		if !ok {
			t = f
		}
		out[k] = lerpGeom(f, t, p)
	}
	for k, t := range c.animTo {
		if _, dup := out[k]; dup {
			continue
		}
		out[k] = lerpGeom(t, t, p)
	}
	return out
}

func (c *Carousel) progress() float64 {
	if !c.animActive {
		return 1
	}
	t := float64(time.Since(c.animStart)) / float64(carAnimDuration)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	// ease-out cubic
	inv := 1 - t
	return 1 - inv*inv*inv
}

// buildTargetGeom walks slots 0, +1, -1, +2, -2 ... around the viewport
// center and assigns each a card geometry. Slots past the viewport edge are
// dropped.
func (c *Carousel) buildTargetGeom(width int) map[int]cardGeom {
	out := map[int]cardGeom{}
	if width <= 0 || len(c.Items) == 0 {
		return out
	}
	cx := float64(width) / 2
	sel := cardGeom{
		LeftX:  cx - float64(carSelectedWidth)/2,
		TopY:   0,
		Width:  float64(carSelectedWidth),
		Height: float64(carSelectedHeight),
		Alpha:  carSlotAlpha[0],
	}
	out[c.selected] = sel

	rightEdge := sel.LeftX + float64(carSelectedWidth) + carGap
	for slot := 1; slot <= carMaxSlot && slot < len(c.Items); slot++ {
		if rightEdge >= float64(width) {
			break
		}
		idx := (c.selected + slot) % len(c.Items)
		if _, dup := out[idx]; dup {
			break
		}
		out[idx] = cardGeom{
			LeftX:  rightEdge,
			TopY:   float64(carRowHeight - carUnselectedHeight),
			Width:  float64(carUnselectedWidth),
			Height: float64(carUnselectedHeight),
			Alpha:  carSlotAlpha[min(slot, len(carSlotAlpha)-1)],
		}
		rightEdge += float64(carUnselectedWidth + carGap)
	}

	leftEdge := sel.LeftX - carGap - float64(carUnselectedWidth)
	for slot := 1; slot <= carMaxSlot && slot < len(c.Items); slot++ {
		if leftEdge+float64(carUnselectedWidth) <= 0 {
			break
		}
		idx := (c.selected - slot + len(c.Items)) % len(c.Items)
		if _, dup := out[idx]; dup {
			break
		}
		out[idx] = cardGeom{
			LeftX:  leftEdge,
			TopY:   float64(carRowHeight - carUnselectedHeight),
			Width:  float64(carUnselectedWidth),
			Height: float64(carUnselectedHeight),
			Alpha:  carSlotAlpha[min(slot, len(carSlotAlpha)-1)],
		}
		leftEdge -= float64(carUnselectedWidth + carGap)
	}
	return out
}

func offscreenGeom(ref cardGeom, direction int) cardGeom {
	step := float64((carUnselectedWidth + carGap) * direction)
	return cardGeom{
		LeftX:  ref.LeftX + step,
		TopY:   ref.TopY,
		Width:  float64(carUnselectedWidth),
		Height: float64(carUnselectedHeight),
		Alpha:  0,
	}
}

func lerpGeom(a, b cardGeom, t float64) cardGeom {
	return cardGeom{
		LeftX:  a.LeftX + (b.LeftX-a.LeftX)*t,
		TopY:   a.TopY + (b.TopY-a.TopY)*t,
		Width:  a.Width + (b.Width-a.Width)*t,
		Height: a.Height + (b.Height-a.Height)*t,
		Alpha:  a.Alpha + (b.Alpha-a.Alpha)*t,
	}
}

// paintCell is one cell in the carousel framebuffer. Set=false leaves the
// renderer free to skip the cell (and emit a plain space without escapes).
type paintCell struct {
	Rune      rune
	Fg, Bg    color.NRGBA
	HasFg     bool
	HasBg     bool
	Bold      bool
	Underline bool
	Set       bool
}

// View paints the carousel + a hotkey hint line. width sets the row's
// horizontal budget; the cards are centered within it. Output is two
// "blocks" joined vertically: the 7-row carousel and a hint legend.
func (c *Carousel) View(width int) string {
	c.lastWidth = width
	if width <= 0 || len(c.Items) == 0 {
		return strings.Repeat(" ", max(width, 0))
	}

	geom := c.snapshotGeom(width)

	// Build a width × rowHeight cell buffer initialised to "blank".
	buf := make([][]paintCell, carRowHeight)
	for y := range buf {
		buf[y] = make([]paintCell, width)
	}

	// Paint farthest-from-center first so the selected card overdraws any
	// neighbour seams. Mid-tween overlaps would otherwise reveal the unset
	// border characters of the receding card.
	type kv struct {
		idx int
		g   cardGeom
	}
	items := make([]kv, 0, len(geom))
	cx := float64(width) / 2
	for k, g := range geom {
		items = append(items, kv{idx: k, g: g})
	}
	// insertion sort by descending distance from cx
	for i := 1; i < len(items); i++ {
		cur := items[i]
		curD := math.Abs(cur.g.LeftX + cur.g.Width/2 - cx)
		j := i - 1
		for j >= 0 {
			prevD := math.Abs(items[j].g.LeftX + items[j].g.Width/2 - cx)
			if prevD >= curD {
				break
			}
			items[j+1] = items[j]
			j--
		}
		items[j+1] = cur
	}
	for _, it := range items {
		c.paintCard(buf, it.g, c.Items[it.idx])
	}

	// Emit buffer to a string. Then append the hotkey hint legend.
	carouselStr := emitBuffer(buf)

	hints := make([]string, 0, len(c.Items))
	for _, it := range c.Items {
		hints = append(hints, theme.Hint.Render(string(it.Hotkey)+"·"+it.Title))
	}
	hintLine := strings.Join(hints, "  ")
	hintLine = centerLine(hintLine, width)

	return carouselStr + "\n\n" + hintLine
}

func (c *Carousel) paintCard(buf [][]paintCell, g cardGeom, it CarouselItem) {
	if g.Alpha <= 0 {
		return
	}
	leftX := int(math.Round(g.LeftX))
	topY := int(math.Round(g.TopY))
	width := int(math.Round(g.Width))
	height := int(math.Round(g.Height))
	if width < 4 || height < 3 {
		return
	}
	isSelected := width >= carSelectedBorderMin
	alpha := g.Alpha

	borderBase := theme.ColorDim
	labelBase := theme.ColorText
	if isSelected {
		borderBase = theme.ColorAccent
		labelBase = theme.ColorText
	}
	borderColor := blendToBg(hexNRGBA(borderBase), alpha)
	labelColor := blendToBg(hexNRGBA(labelBase), alpha)

	tl, tr, bl, br, h, v := '┌', '┐', '└', '┘', '─', '│'
	if isSelected {
		tl, tr, bl, br, h, v = '╔', '╗', '╚', '╝', '═', '║'
	}

	plot := func(x, y int, r rune, fg color.NRGBA, bold, underline bool) {
		if x < 0 || x >= len(buf[0]) || y < 0 || y >= len(buf) {
			return
		}
		buf[y][x] = paintCell{Rune: r, Fg: fg, HasFg: true, Bold: bold, Underline: underline, Set: true}
	}

	plot(leftX, topY, tl, borderColor, isSelected, false)
	plot(leftX+width-1, topY, tr, borderColor, isSelected, false)
	plot(leftX, topY+height-1, bl, borderColor, isSelected, false)
	plot(leftX+width-1, topY+height-1, br, borderColor, isSelected, false)
	for x := 1; x < width-1; x++ {
		plot(leftX+x, topY, h, borderColor, isSelected, false)
		plot(leftX+x, topY+height-1, h, borderColor, isSelected, false)
	}
	for y := 1; y < height-1; y++ {
		plot(leftX, topY+y, v, borderColor, isSelected, false)
		plot(leftX+width-1, topY+y, v, borderColor, isSelected, false)
	}
	// fill interior with blanks so neighbour pixels behind a mid-tween card
	// don't show through (paintCell.Set marks the cell as "owned").
	for y := topY + 1; y < topY+height-1; y++ {
		for x := leftX + 1; x < leftX+width-1; x++ {
			if x < 0 || x >= len(buf[0]) || y < 0 || y >= len(buf) {
				continue
			}
			buf[y][x] = paintCell{Rune: ' ', Set: true}
		}
	}

	innerWidth := width - 2
	innerLeft := leftX + 1

	// icon: center inside the interior area minus the label row
	if it.Icon != nil {
		iconAreaRows := max(0, height-3)
		iconRows := min(iconAreaRows, it.Icon.Height)
		iconStartY := topY + 1 + (iconAreaRows-iconRows)/2
		iconCols := min(it.Icon.Width, innerWidth)
		iconStartX := innerLeft + (innerWidth-iconCols)/2
		for iy := 0; iy < iconRows; iy++ {
			row := it.Icon.Cells[iy]
			for ix := 0; ix < iconCols; ix++ {
				cell := row[ix]
				if cell.Rune == 0 {
					continue
				}
				var fg color.NRGBA
				if cell.Fg != nil {
					fg = blendToBg(*cell.Fg, alpha)
				} else {
					fg = blendToBg(hexNRGBA(theme.ColorText), alpha)
				}
				plot(iconStartX+ix, iconStartY+iy, cell.Rune, fg, cell.Bold, false)
			}
		}
	}

	labelText := it.Title
	if isSelected {
		labelText = "► " + strings.ToUpper(it.Title) + " ◄"
	}
	if len(labelText) > innerWidth {
		labelText = labelText[:innerWidth]
	}
	labelStartX := innerLeft + (innerWidth-len(labelText))/2
	labelY := topY + height - 2

	hkIdx := findHotkeyIndex(labelText, it.Hotkey)
	for i, r := range labelText {
		plot(labelStartX+i, labelY, r, labelColor, isSelected, i == hkIdx)
	}
}

// emitBuffer converts the cell buffer into a string with SGR escapes,
// batching runs of identical-styled cells per row for fewer escapes.
func emitBuffer(buf [][]paintCell) string {
	var b strings.Builder
	for y, row := range buf {
		i := 0
		for i < len(row) {
			j := i + 1
			for j < len(row) && cellsSameStyle(row[i], row[j]) {
				j++
			}
			run := row[i:j]
			b.WriteString(styleForCell(row[i]).Render(runesFromCells(run)))
			i = j
		}
		if y < len(buf)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func cellsSameStyle(a, b paintCell) bool {
	if a.HasFg != b.HasFg || a.HasBg != b.HasBg || a.Bold != b.Bold || a.Underline != b.Underline {
		return false
	}
	if a.HasFg && a.Fg != b.Fg {
		return false
	}
	if a.HasBg && a.Bg != b.Bg {
		return false
	}
	return true
}

func styleForCell(c paintCell) lipgloss.Style {
	s := lipgloss.NewStyle()
	if c.HasFg {
		s = s.Foreground(lipgloss.Color(hexFromNRGBA(c.Fg)))
	}
	if c.HasBg {
		s = s.Background(lipgloss.Color(hexFromNRGBA(c.Bg)))
	}
	if c.Bold {
		s = s.Bold(true)
	}
	if c.Underline {
		s = s.Underline(true)
	}
	return s
}

func runesFromCells(cells []paintCell) string {
	var b strings.Builder
	b.Grow(len(cells))
	for _, c := range cells {
		switch {
		case !c.Set, c.Rune == 0:
			b.WriteByte(' ')
		default:
			b.WriteRune(c.Rune)
		}
	}
	return b.String()
}

func matchHotkey(typed, want rune) bool {
	return foldRune(typed) == foldRune(want)
}

func foldRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

func findHotkeyIndex(label string, hotkey rune) int {
	target := unicode.ToLower(hotkey)
	for i, r := range label {
		if unicode.ToLower(r) == target {
			return i
		}
	}
	return -1
}

// hexNRGBA parses a #RRGGBB string from the theme palette into the same
// color.NRGBA type the icon cells use so the dim path can treat them uniformly.
func hexNRGBA(hex string) color.NRGBA {
	if len(hex) != 7 || hex[0] != '#' {
		return color.NRGBA{A: 0xFF}
	}
	parse := func(h, l byte) uint8 {
		return uint8(nybble(h)<<4 | nybble(l))
	}
	return color.NRGBA{
		R: parse(hex[1], hex[2]),
		G: parse(hex[3], hex[4]),
		B: parse(hex[5], hex[6]),
		A: 0xFF,
	}
}

func nybble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c - 'a' + 10)
	case c >= 'A' && c <= 'F':
		return int(c - 'A' + 10)
	}
	return 0
}

func hexFromNRGBA(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// blendToBg dims a color toward the theme background by (1-alpha). Same
// shape as LobbyCarouselView.Dim. alpha=1.0 returns the input, alpha=0.0
// returns the background.
func blendToBg(in color.NRGBA, alpha float64) color.NRGBA {
	if alpha >= 1 {
		return in
	}
	bg := hexNRGBA(theme.ColorBackground)
	if alpha <= 0 {
		return bg
	}
	return color.NRGBA{
		R: uint8(math.Round(float64(bg.R) + (float64(in.R)-float64(bg.R))*alpha)),
		G: uint8(math.Round(float64(bg.G) + (float64(in.G)-float64(bg.G))*alpha)),
		B: uint8(math.Round(float64(bg.B) + (float64(in.B)-float64(bg.B))*alpha)),
		A: 0xFF,
	}
}

func centerLine(s string, width int) string {
	plain := lipgloss.Width(s)
	if plain >= width {
		return s
	}
	pad := (width - plain) / 2
	return strings.Repeat(" ", pad) + s
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
