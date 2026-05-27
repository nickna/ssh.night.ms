// Command ansiconvert turns a PNG/JPEG into a .ans file using half-block
// rendering: each output cell covers two source pixels vertically (foreground
// = top pixel, background = bottom pixel, glyph = U+2580 "upper half block").
//
// Usage:
//
//	ansiconvert -width 80 -depth truecolor <input.png>
//	ansiconvert -depth 256 -dither floyd <input.jpg> >art/welcome.ans
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

type depthMode int

const (
	depthTruecolor depthMode = iota
	depth256
	depth16
)

type ditherMode int

const (
	ditherNone ditherMode = iota
	ditherFloyd
)

func main() {
	var (
		widthFlag  = flag.Int("width", 80, "output width in cells")
		depthFlag  = flag.String("depth", "truecolor", "color depth: truecolor | 256 | 16")
		ditherFlag = flag.String("dither", "", "dither: none | floyd (default: none for truecolor, floyd for quantized)")
		outFlag    = flag.String("out", "", "output file (default: stdout)")
	)
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ansiconvert [-width N] [-depth truecolor|256|16] [-dither none|floyd] [-out PATH] <input>")
		os.Exit(2)
	}
	inPath := flag.Arg(0)

	depth, err := parseDepth(*depthFlag)
	check(err)
	dither, err := parseDither(*ditherFlag, depth)
	check(err)

	src, err := loadImage(inPath)
	check(err)

	scaled := scale(src, *widthFlag)
	if dither == ditherFloyd && depth != depthTruecolor {
		scaled = floydSteinberg(scaled, depth)
	}

	w := os.Stdout
	if *outFlag != "" {
		f, err := os.Create(*outFlag)
		check(err)
		defer f.Close()
		w = f
	}
	check(emit(w, scaled, depth))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ansiconvert:", err)
		os.Exit(1)
	}
}

func parseDepth(s string) (depthMode, error) {
	switch strings.ToLower(s) {
	case "truecolor", "true", "24", "rgb":
		return depthTruecolor, nil
	case "256", "xterm256", "xterm-256":
		return depth256, nil
	case "16", "ansi16":
		return depth16, nil
	}
	return 0, fmt.Errorf("invalid depth %q", s)
}

func parseDither(s string, d depthMode) (ditherMode, error) {
	if s == "" {
		if d == depthTruecolor {
			return ditherNone, nil
		}
		return ditherFloyd, nil
	}
	switch strings.ToLower(s) {
	case "none", "off":
		return ditherNone, nil
	case "floyd", "floyd-steinberg", "fs":
		return ditherFloyd, nil
	}
	return 0, fmt.Errorf("invalid dither %q", s)
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// scale resizes the image so its width matches the requested cell width,
// preserving aspect ratio. Each output cell is 2 pixels tall, so the target
// pixel height is 2 × (aspect-preserved row count). CatmullRom keeps edges
// sharp without ringing.
func scale(src image.Image, cellWidth int) *image.NRGBA {
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if srcW == 0 || srcH == 0 {
		return image.NewNRGBA(image.Rect(0, 0, cellWidth, 2))
	}
	cellRows := int(float64(srcH) * float64(cellWidth) / float64(srcW) / 2.0)
	if cellRows < 1 {
		cellRows = 1
	}
	dstW := cellWidth
	dstH := cellRows * 2
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// floydSteinberg applies error-diffusion dithering against the target palette
// (256-color xterm or 16-color ANSI). The image is quantized in-place; the
// caller emits using the same palette during SGR generation.
func floydSteinberg(img *image.NRGBA, d depthMode) *image.NRGBA {
	out := image.NewNRGBA(img.Rect)
	copy(out.Pix, img.Pix)
	w := out.Rect.Dx()
	h := out.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			oldR, oldG, oldB, _ := out.At(x, y).RGBA()
			or, og, ob := uint8(oldR>>8), uint8(oldG>>8), uint8(oldB>>8)
			var newR, newG, newB uint8
			switch d {
			case depth256:
				idx := nearest256(or, og, ob)
				newR, newG, newB = palette256[idx][0], palette256[idx][1], palette256[idx][2]
			case depth16:
				idx := nearest16(or, og, ob)
				newR, newG, newB = palette16[idx][0], palette16[idx][1], palette16[idx][2]
			default:
				newR, newG, newB = or, og, ob
			}
			off := out.PixOffset(x, y)
			out.Pix[off] = newR
			out.Pix[off+1] = newG
			out.Pix[off+2] = newB
			out.Pix[off+3] = 0xFF
			errR := int(or) - int(newR)
			errG := int(og) - int(newG)
			errB := int(ob) - int(newB)
			diffuse(out, x+1, y, errR, errG, errB, 7)
			diffuse(out, x-1, y+1, errR, errG, errB, 3)
			diffuse(out, x, y+1, errR, errG, errB, 5)
			diffuse(out, x+1, y+1, errR, errG, errB, 1)
		}
	}
	return out
}

func diffuse(img *image.NRGBA, x, y, er, eg, eb, weight int) {
	if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() {
		return
	}
	off := img.PixOffset(x, y)
	img.Pix[off] = clip(int(img.Pix[off]) + er*weight/16)
	img.Pix[off+1] = clip(int(img.Pix[off+1]) + eg*weight/16)
	img.Pix[off+2] = clip(int(img.Pix[off+2]) + eb*weight/16)
}

func clip(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 0xFF {
		return 0xFF
	}
	return uint8(v)
}

// emit walks the image in 2-row strides, emitting one cell per column. Each
// cell is "ESC[<fg>;<bg>m▀" with explicit transitions to suppress redundant
// SGR codes (cheap win on long runs).
func emit(w io.Writer, img *image.NRGBA, d depthMode) error {
	bw := newWriter(w)
	defer bw.Flush()

	cellW := img.Rect.Dx()
	cellH := img.Rect.Dy() / 2

	var lastFg, lastBg = "", ""
	for cy := 0; cy < cellH; cy++ {
		for cx := 0; cx < cellW; cx++ {
			top := img.NRGBAAt(cx, cy*2)
			bot := img.NRGBAAt(cx, cy*2+1)
			fg := sgrColor(true, top, d)
			bg := sgrColor(false, bot, d)
			var sgr string
			switch {
			case fg != lastFg && bg != lastBg:
				sgr = "\x1b[" + fg + ";" + bg + "m"
			case fg != lastFg:
				sgr = "\x1b[" + fg + "m"
			case bg != lastBg:
				sgr = "\x1b[" + bg + "m"
			}
			lastFg, lastBg = fg, bg
			bw.WriteString(sgr)
			bw.WriteString("▀")
		}
		bw.WriteString("\x1b[0m\n")
		lastFg, lastBg = "", ""
	}
	return nil
}

func sgrColor(fg bool, c color.NRGBA, d depthMode) string {
	base := "38"
	if !fg {
		base = "48"
	}
	switch d {
	case depthTruecolor:
		return base + ";2;" + itoa(int(c.R)) + ";" + itoa(int(c.G)) + ";" + itoa(int(c.B))
	case depth256:
		idx := nearest256(c.R, c.G, c.B)
		return base + ";5;" + itoa(idx)
	case depth16:
		idx := nearest16(c.R, c.G, c.B)
		return ansi16Code(fg, idx)
	}
	return ""
}

// ansi16Code returns the legacy 30-37 / 90-97 / 40-47 / 100-107 SGR for a
// 16-color palette entry; that's denser than the 38;5;N form.
func ansi16Code(fg bool, idx int) string {
	if idx < 8 {
		if fg {
			return itoa(30 + idx)
		}
		return itoa(40 + idx)
	}
	if fg {
		return itoa(90 + idx - 8)
	}
	return itoa(100 + idx - 8)
}

func itoa(i int) string { return strconv.Itoa(i) }

// nearest256 finds the closest xterm-256 palette entry by squared distance.
// The 6×6×6 color cube + 24 grayscale ramp is computed once at init.
func nearest256(r, g, b uint8) int {
	best := 0
	bestD := 1 << 30
	for i, p := range palette256 {
		dr := int(r) - int(p[0])
		dg := int(g) - int(p[1])
		db := int(b) - int(p[2])
		d := dr*dr + dg*dg + db*db
		if d < bestD {
			bestD = d
			best = i
		}
	}
	return best
}

func nearest16(r, g, b uint8) int {
	best := 0
	bestD := 1 << 30
	for i, p := range palette16 {
		dr := int(r) - int(p[0])
		dg := int(g) - int(p[1])
		db := int(b) - int(p[2])
		d := dr*dr + dg*dg + db*db
		if d < bestD {
			bestD = d
			best = i
		}
	}
	return best
}

// palette16 is the classic Linux-console 16-color palette. Values chosen to
// match the original Microsoft Windows colors.
var palette16 = [16][3]uint8{
	{0, 0, 0},       // 0  black
	{170, 0, 0},     // 1  red
	{0, 170, 0},     // 2  green
	{170, 85, 0},    // 3  yellow
	{0, 0, 170},     // 4  blue
	{170, 0, 170},   // 5  magenta
	{0, 170, 170},   // 6  cyan
	{170, 170, 170}, // 7  white
	{85, 85, 85},    // 8  bright black
	{255, 85, 85},   // 9  bright red
	{85, 255, 85},   // 10 bright green
	{255, 255, 85},  // 11 bright yellow
	{85, 85, 255},   // 12 bright blue
	{255, 85, 255},  // 13 bright magenta
	{85, 255, 255},  // 14 bright cyan
	{255, 255, 255}, // 15 bright white
}

// palette256 is built on init: indexes 0-15 = ANSI16, 16-231 = 6×6×6 cube,
// 232-255 = 24 grayscale entries.
var palette256 [256][3]uint8

func init() {
	for i, c := range palette16 {
		palette256[i] = c
	}
	cube := [6]uint8{0, 95, 135, 175, 215, 255}
	idx := 16
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				palette256[idx] = [3]uint8{cube[r], cube[g], cube[b]}
				idx++
			}
		}
	}
	for i := 0; i < 24; i++ {
		v := uint8(8 + i*10)
		palette256[232+i] = [3]uint8{v, v, v}
	}
}

// newWriter wraps an io.Writer so emit() can do many small string writes
// without per-call syscalls. Hand-rolled to avoid pulling bufio in just
// for one user.
type bufWriter struct {
	w   io.Writer
	buf []byte
}

func newWriter(w io.Writer) *bufWriter { return &bufWriter{w: w, buf: make([]byte, 0, 4096)} }

func (b *bufWriter) WriteString(s string) {
	if len(b.buf)+len(s) > cap(b.buf) {
		_, _ = b.w.Write(b.buf)
		b.buf = b.buf[:0]
		if len(s) > cap(b.buf) {
			_, _ = b.w.Write([]byte(s))
			return
		}
	}
	b.buf = append(b.buf, s...)
}
func (b *bufWriter) Flush() {
	if len(b.buf) > 0 {
		_, _ = b.w.Write(b.buf)
		b.buf = b.buf[:0]
	}
}
