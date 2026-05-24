package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strconv"
)

// encodeIterm2 emits the iTerm2 inline-image OSC sequence (OSC 1337 File=...).
// Width is given in cells via the width= attribute. Like Kitty, the whole
// payload sits in row[0] and the remaining returned rows are blank so the
// caller's line accounting matches the on-screen footprint.
func encodeIterm2(img image.Image, cellCols int) []string {
	if img == nil || cellCols <= 0 {
		return nil
	}
	rows := approxRowsForCols(img, cellCols)
	if rows <= 0 {
		rows = 1
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	if encoded == "" {
		return nil
	}

	var out bytes.Buffer
	out.WriteString("\x1b]1337;File=inline=1;width=")
	out.WriteString(strconv.Itoa(cellCols))
	out.WriteString(";height=")
	out.WriteString(strconv.Itoa(rows))
	out.WriteString(";preserveAspectRatio=1:")
	out.WriteString(encoded)
	out.WriteString("\x07")

	lines := make([]string, rows)
	lines[0] = out.String()
	for i := 1; i < rows; i++ {
		lines[i] = ""
	}
	return lines
}
