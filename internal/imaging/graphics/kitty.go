package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strconv"
)

// kittyChunkSize is the per-APC-payload byte budget. The Kitty protocol
// recommends ≤4096 base64 chars; staying under that keeps wide compatibility
// with terminals that copy chunks through fixed buffers.
const kittyChunkSize = 4096

// kittyCellPixels is the assumed pixel-per-cell ratio used when sizing the
// output. Most terminals run roughly 8×16 or 10×20; the protocol's c=cols
// and r=rows attributes do the final scaling, so this is just a hint that
// keeps the encoded PNG from being needlessly oversized for narrow widths.
const kittyCellPixels = 16

// encodeKitty emits a Kitty graphics protocol image directly to the terminal.
// The full payload is built into a single returned row[0]; the rest of the
// returned slice is empty rows so the caller's vertical-layout accounting
// matches the on-screen cell footprint.
func encodeKitty(img image.Image, cellCols int) []string {
	if img == nil || cellCols <= 0 {
		return nil
	}
	rows := approxRowsForCols(img, cellCols)
	if rows <= 0 {
		rows = 1
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	if encoded == "" {
		return nil
	}

	var out bytes.Buffer
	// First chunk carries the format + size hints; subsequent chunks
	// continue with m=1 except the last which sets m=0.
	for i := 0; i < len(encoded); i += kittyChunkSize {
		end := i + kittyChunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		chunk := encoded[i:end]
		more := end < len(encoded)
		out.WriteString("\x1b_G")
		if i == 0 {
			out.WriteString("a=T,f=100,c=")
			out.WriteString(strconv.Itoa(cellCols))
			out.WriteString(",r=")
			out.WriteString(strconv.Itoa(rows))
		}
		if more {
			if i > 0 {
				out.WriteString("m=1")
			} else {
				out.WriteString(",m=1")
			}
		} else if i > 0 {
			out.WriteString("m=0")
		}
		out.WriteByte(';')
		out.WriteString(chunk)
		out.WriteString("\x1b\\")
	}

	lines := make([]string, rows)
	lines[0] = out.String()
	for i := 1; i < rows; i++ {
		lines[i] = ""
	}
	return lines
}

// approxRowsForCols returns how many terminal rows we expect the scaled
// image to occupy, used both to pad the returned slice and to advise the
// Kitty/iTerm2 protocol on cell extents. Aspect ratio is preserved from the
// source image, assuming kittyCellPixels for height.
func approxRowsForCols(img image.Image, cellCols int) int {
	if img == nil {
		return 0
	}
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	if w == 0 || h == 0 {
		return 0
	}
	// Pixel width per cell — we don't know exactly, so assume 8.
	const cellPxWidth = 8
	targetPxW := cellCols * cellPxWidth
	scaledH := h * targetPxW / w
	rows := scaledH / kittyCellPixels
	if rows < 1 {
		rows = 1
	}
	return rows
}
