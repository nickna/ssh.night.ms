// Package theme is the single source of lipgloss styles + named colors used
// across every screen. Centralizing here makes a palette change (or a "dark"
// vs "high-contrast" theme later) a one-file edit.
package theme

import "github.com/charmbracelet/lipgloss"

// Named colors. Hex literals are kept here so screens never spell raw colors.
const (
	ColorBackground = "#0E0B16"
	ColorSurface    = "#1A1426"
	ColorSurfaceAlt = "#241D33"
	ColorMuted      = "#3F3F46"
	ColorDim        = "#6E6A7F"
	ColorText       = "#E6E0F2"
	ColorAccent     = "#FF7DB0" // titles, prompts
	ColorAccentDim  = "#9C8AA5"
	ColorCyan       = "#5EE7DF"
	ColorYellow     = "#FFD166"
	ColorGreen      = "#5EE39C"
	ColorRed        = "#FF6B7A"

	// Card-game tokens — used by the cardart + cabinet widgets so every door
	// game shares one palette. Suit colors map hearts/diamonds → red ink and
	// spades/clubs → text ink, matching real-world playing cards. Held/winning
	// reuses gold (Yellow) so a glance reads as "lit up." Felt accents pick a
	// distinct hue per cabinet so each game still feels its own.
	ColorSuitRed  = ColorRed
	ColorSuitInk  = ColorText
	ColorCardHeld = ColorYellow
	ColorFeltBJ        = "#2E7D5B"
	ColorFeltVP        = "#3A8FB7"
	ColorFeltHE        = "#9E2B3C"
	ColorFeltRoulette  = "#7A1F2E" // dark wine red — distinct from Hold'em's brighter burgundy
)

var (
	Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(ColorAccent)).
		Padding(0, 1)

	Sub = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorAccentDim))

	// Header is the bold-accent style used for the lobby welcome and other
	// top-of-screen prompts.
	Header = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorAccent)).
		Bold(true)

	Body = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorText))

	Hint = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorMuted)).
		Italic(true)

	Notice = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorCyan)).
		Bold(true)

	SysopBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(ColorYellow)).
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color(ColorYellow)).
			Padding(0, 1)

	// SysopNotice is the flat-text variant shown below the lobby carousel
	// when the user has the sysop bit.
	SysopNotice = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorGreen)).
			Bold(true)

	// Carousel card styles. The "selected" card is the focused one; "neighbor"
	// is rendered at a smaller size. The fade is applied inline (per-distance
	// alpha blend) so we don't pre-compute N styles.
	CardSelected = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color(ColorAccent)).
			Foreground(lipgloss.Color(ColorText)).
			Bold(true).
			Padding(1, 2)

	CardNeighbor = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color(ColorDim)).
			Foreground(lipgloss.Color(ColorDim)).
			Padding(0, 1)

	// Boards-screen chrome. Header lives at the top of every mode; StatusBar
	// is the full-width footer with hints/counts. BreadcrumbSep is the dim
	// glyph between crumb segments ("Boards › #dev › title"). The bar style
	// carries no padding — callers compose their own row at exact width
	// (see boards.chromeStatus) so they own the left/right layout.
	StatusBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorDim)).
			Background(lipgloss.Color(ColorSurface))

	BreadcrumbSep = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorDim))

	// PostCard wraps each post in the thread view. Rounded border keeps the
	// surface feeling less harsh than the carousel's double border. The
	// AccentDim color is intentional — bright accent borders next to each
	// other would compete with the OP/SYSOP chips inside.
	PostCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorAccentDim)).
			Foreground(lipgloss.Color(ColorText)).
			Padding(0, 1)

	// ModalFrame is the compose overlay's bordered container. Double border
	// + accent color so it pops against the dimmed underlying scene.
	ModalFrame = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color(ColorAccent)).
			Background(lipgloss.Color(ColorSurface)).
			Foreground(lipgloss.Color(ColorText)).
			Padding(1, 2)

	// Chips render as inline pills in the post-card header. Background-color
	// chips read as "labels" rather than as text — exactly what we want for
	// OP / SYSOP / unread badges.
	OPChip = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(ColorBackground)).
		Background(lipgloss.Color(ColorAccent)).
		Padding(0, 1)

	SysopChip = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(ColorBackground)).
			Background(lipgloss.Color(ColorYellow)).
			Padding(0, 1)

	EditedChip = lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.Color(ColorDim))

	UnreadBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(ColorBackground)).
			Background(lipgloss.Color(ColorGreen)).
			Padding(0, 1)

	// Per-row unread/read markers used in the forum + topic lists.
	UnreadDot = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorGreen)).
			Bold(true)

	ReadDot = lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorDim))

	// RowHighlight is the selection background reused across screens (Boards
	// forum/topic rows, Profile buttons, etc.). Centralizing avoids each
	// screen re-deriving the same yellow-on-surface-alt combo. Bold so the
	// active row also reads as emphasized text.
	RowHighlight = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color(ColorSurfaceAlt)).
			Foreground(lipgloss.Color(ColorYellow))

	// AccentBar is the colored 1-cell-wide gutter painted on the left edge
	// of a selected forum block (replaces the ▸ cursor glyph). Renders as a
	// solid colored space so it reads as a continuous bar across all rows
	// of the multi-line block.
	AccentBar = lipgloss.NewStyle().
			Background(lipgloss.Color(ColorAccent))

	// AuthorChip styles a "[@handle]" tag for the topic-list rows. Uses the
	// accent-dim background so it reads as a soft pill rather than a hard
	// label. Sysop authors get SysopChip instead.
	AuthorChip = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorBackground)).
			Background(lipgloss.Color(ColorAccentDim)).
			Padding(0, 1)
)

// KeyChip renders a hint key glyph (e.g. "↑/↓", "Enter") as a small chip.
// Used by the boards chrome header to make the keybinding row scan as a
// row of buttons rather than one long italic sentence.
func KeyChip(s string) string {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(ColorYellow)).
		Background(lipgloss.Color(ColorSurfaceAlt)).
		Padding(0, 1).Render(s)
}

// BlendHex linearly interpolates two hex-form colors. t=0 → a, t=1 → b. Used
// by the carousel to fade neighbor cards by distance from center.
func BlendHex(aHex, bHex string, t float64) string {
	ar, ag, ab := parseHex(aHex)
	br, bg, bb := parseHex(bHex)
	r := int(float64(ar)*(1-t) + float64(br)*t)
	g := int(float64(ag)*(1-t) + float64(bg)*t)
	b := int(float64(ab)*(1-t) + float64(bb)*t)
	return rgbHex(r, g, b)
}

func parseHex(h string) (r, g, b int) {
	if len(h) != 7 || h[0] != '#' {
		return 0, 0, 0
	}
	r = (hex(h[1])<<4 | hex(h[2]))
	g = (hex(h[3])<<4 | hex(h[4]))
	b = (hex(h[5])<<4 | hex(h[6]))
	return
}

func hex(c byte) int {
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

func rgbHex(r, g, b int) string {
	r = clamp8(r)
	g = clamp8(g)
	b = clamp8(b)
	return "#" + nibble(r>>4) + nibble(r&0xF) + nibble(g>>4) + nibble(g&0xF) + nibble(b>>4) + nibble(b&0xF)
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func nibble(n int) string {
	const digits = "0123456789ABCDEF"
	return string(digits[n&0xF])
}
