package art

import (
	"image/color"
	"strconv"
	"strings"
)

// ParseANSI scans an SGR-laden byte stream into a CellGrid. Supports:
//   - 16-color palette via 30-37 / 40-47 / 90-97 / 100-107
//   - 256-color via "38;5;N" / "48;5;N"
//   - Truecolor via "38;2;R;G;B" / "48;2;R;G;B"
//   - Bold (1), reset (0), default-fg (39), default-bg (49)
//   - CR (\r), LF (\n) — LF advances row, CR resets col to 0
//   - Tab → 8-cell stops
//
// Unknown escape sequences are silently skipped so a malformed file paints
// what it can rather than failing. The canvas grows to fit the content:
// width is the longest line, height is the line count.
func ParseANSI(data []byte) *CellGrid {
	// Two-pass: first pass discovers the canvas size by walking newlines +
	// recording max column reached per row. Second pass paints into the
	// pre-sized canvas. Simpler than dynamic growth and we only do this on
	// file load.
	w, h := measureANSI(data)
	if w == 0 {
		w = 1
	}
	if h == 0 {
		h = 1
	}
	grid := NewCellGrid(w, h)

	st := state{fg: nil, bg: nil}
	row, col := 0, 0
	i := 0
	for i < len(data) {
		b := data[i]
		switch b {
		case 0x1b: // ESC
			n := consumeEscape(data, i, &st)
			i += n
			continue
		case '\r':
			col = 0
			i++
			continue
		case '\n':
			row++
			col = 0
			i++
			continue
		case '\t':
			col = ((col / 8) + 1) * 8
			i++
			continue
		}
		// UTF-8 decode
		r, size := decodeRune(data[i:])
		if size == 0 {
			i++
			continue
		}
		i += size
		if c := grid.At(row, col); c != nil {
			c.Rune = r
			if st.fg != nil {
				fg := *st.fg
				c.Fg = &fg
			}
			if st.bg != nil {
				bg := *st.bg
				c.Bg = &bg
			}
			c.Bold = st.bold
		}
		col++
	}
	return grid
}

// state is the current SGR attributes while the parser walks the stream.
type state struct {
	fg, bg *color.NRGBA
	bold   bool
}

// consumeEscape handles ONE escape sequence starting at data[start] (which
// must be 0x1B). Returns the number of bytes consumed (including the ESC).
// Mutates `st` for SGR. Unknown CSI sequences are skipped without effect.
func consumeEscape(data []byte, start int, st *state) int {
	if start+1 >= len(data) {
		return 1
	}
	if data[start+1] != '[' {
		// ESC followed by something else (e.g. OSC). Skip until ST/BEL.
		return skipNonCSI(data, start)
	}
	// CSI: ESC [ params [intermediate] final
	i := start + 2
	paramsStart := i
	for i < len(data) {
		b := data[i]
		if (b >= '0' && b <= '9') || b == ';' || b == '?' || b == ' ' {
			i++
			continue
		}
		break
	}
	if i >= len(data) {
		return i - start
	}
	final := data[i]
	params := string(data[paramsStart:i])
	consumed := (i - start) + 1
	if final == 'm' {
		applySGR(params, st)
	}
	// Other CSI finals (H/J/K/...) we drop; we don't implement cursor
	// positioning yet because ParseANSI uses CR/LF + glyph order.
	return consumed
}

func skipNonCSI(data []byte, start int) int {
	for i := start + 1; i < len(data); i++ {
		// OSC terminates with BEL (0x07) or ESC \
		if data[i] == 0x07 {
			return i - start + 1
		}
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i - start + 2
		}
	}
	return len(data) - start
}

// applySGR walks the ";"-separated parameter list and updates the state.
// Handles 38;5;N / 38;2;R;G;B / 48;5;N / 48;2;R;G;B inline (they consume
// extra params from the list).
func applySGR(params string, st *state) {
	if params == "" {
		// CSI m alone == CSI 0 m == reset.
		st.fg, st.bg, st.bold = nil, nil, false
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		p, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch {
		case p == 0:
			st.fg, st.bg, st.bold = nil, nil, false
		case p == 1:
			st.bold = true
		case p == 22:
			st.bold = false
		case p == 39:
			st.fg = nil
		case p == 49:
			st.bg = nil
		case p >= 30 && p <= 37:
			c := palette16[p-30]
			st.fg = &c
		case p >= 90 && p <= 97:
			c := palette16[p-90+8]
			st.fg = &c
		case p >= 40 && p <= 47:
			c := palette16[p-40]
			st.bg = &c
		case p >= 100 && p <= 107:
			c := palette16[p-100+8]
			st.bg = &c
		case p == 38 || p == 48:
			// Extended color. Next param: 5 = 256-color, 2 = truecolor.
			if i+1 >= len(parts) {
				return
			}
			kind, _ := strconv.Atoi(parts[i+1])
			switch kind {
			case 5:
				if i+2 < len(parts) {
					n, _ := strconv.Atoi(parts[i+2])
					c := paletteAt256(n)
					if p == 38 {
						st.fg = &c
					} else {
						st.bg = &c
					}
					i += 2
				}
			case 2:
				if i+4 < len(parts) {
					r, _ := strconv.Atoi(parts[i+2])
					g, _ := strconv.Atoi(parts[i+3])
					bl, _ := strconv.Atoi(parts[i+4])
					c := color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(bl), A: 0xFF}
					if p == 38 {
						st.fg = &c
					} else {
						st.bg = &c
					}
					i += 4
				}
			}
		}
	}
}

// palette16 is the standard ANSI 16-color palette (low 8 + bright 8). Picked
// to look reasonable on dark terminals — close to xterm defaults.
var palette16 = [16]color.NRGBA{
	{R: 0x00, G: 0x00, B: 0x00, A: 0xFF}, // 0 black
	{R: 0xCC, G: 0x33, B: 0x33, A: 0xFF}, // 1 red
	{R: 0x33, G: 0xAA, B: 0x55, A: 0xFF}, // 2 green
	{R: 0xCC, G: 0x99, B: 0x33, A: 0xFF}, // 3 yellow
	{R: 0x33, G: 0x66, B: 0xCC, A: 0xFF}, // 4 blue
	{R: 0xAA, G: 0x55, B: 0xCC, A: 0xFF}, // 5 magenta
	{R: 0x33, G: 0xAA, B: 0xCC, A: 0xFF}, // 6 cyan
	{R: 0xCC, G: 0xCC, B: 0xCC, A: 0xFF}, // 7 white
	{R: 0x66, G: 0x66, B: 0x66, A: 0xFF}, // 8 bright black
	{R: 0xFF, G: 0x55, B: 0x55, A: 0xFF}, // 9 bright red
	{R: 0x55, G: 0xFF, B: 0x88, A: 0xFF}, // 10 bright green
	{R: 0xFF, G: 0xDD, B: 0x55, A: 0xFF}, // 11 bright yellow
	{R: 0x55, G: 0x99, B: 0xFF, A: 0xFF}, // 12 bright blue
	{R: 0xDD, G: 0x88, B: 0xFF, A: 0xFF}, // 13 bright magenta
	{R: 0x55, G: 0xDD, B: 0xFF, A: 0xFF}, // 14 bright cyan
	{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}, // 15 bright white
}

// paletteAt256 returns the 256-color palette entry for n. 0-15 are the
// standard 16; 16-231 are the 6×6×6 RGB cube; 232-255 are grayscale.
func paletteAt256(n int) color.NRGBA {
	switch {
	case n < 0 || n > 255:
		return color.NRGBA{A: 0xFF}
	case n < 16:
		return palette16[n]
	case n < 232:
		// 6×6×6 cube: index = 16 + 36r + 6g + b, each component 0-5.
		idx := n - 16
		r := (idx / 36) % 6
		g := (idx / 6) % 6
		b := idx % 6
		conv := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + v*40)
		}
		return color.NRGBA{R: conv(r), G: conv(g), B: conv(b), A: 0xFF}
	default:
		v := uint8(8 + (n-232)*10)
		return color.NRGBA{R: v, G: v, B: v, A: 0xFF}
	}
}

// measureANSI walks data once to discover canvas dimensions. Same loop
// shape as the main parser but only counts; keeps the second pass simple.
func measureANSI(data []byte) (int, int) {
	maxCol, col, rows := 0, 0, 0
	i := 0
	for i < len(data) {
		b := data[i]
		switch b {
		case 0x1b:
			n := consumeEscape(data, i, &state{})
			i += n
			continue
		case '\r':
			col = 0
			i++
			continue
		case '\n':
			rows++
			if col > maxCol {
				maxCol = col
			}
			col = 0
			i++
			continue
		case '\t':
			col = ((col / 8) + 1) * 8
			i++
			continue
		}
		_, sz := decodeRune(data[i:])
		if sz == 0 {
			i++
			continue
		}
		i += sz
		col++
		if col > maxCol {
			maxCol = col
		}
	}
	if col > 0 {
		rows++ // last line without trailing \n
	}
	return maxCol, rows
}

// decodeRune is a tiny UTF-8 decoder so we don't pull utf8 just for this.
func decodeRune(p []byte) (rune, int) {
	if len(p) == 0 {
		return 0, 0
	}
	b := p[0]
	switch {
	case b < 0x80:
		return rune(b), 1
	case b < 0xC0:
		return 0xFFFD, 1 // stray continuation
	case b < 0xE0:
		if len(p) < 2 {
			return 0xFFFD, 1
		}
		return (rune(b&0x1F) << 6) | rune(p[1]&0x3F), 2
	case b < 0xF0:
		if len(p) < 3 {
			return 0xFFFD, 1
		}
		return (rune(b&0x0F) << 12) | (rune(p[1]&0x3F) << 6) | rune(p[2]&0x3F), 3
	default:
		if len(p) < 4 {
			return 0xFFFD, 1
		}
		return (rune(b&0x07) << 18) | (rune(p[1]&0x3F) << 12) | (rune(p[2]&0x3F) << 6) | rune(p[3]&0x3F), 4
	}
}
