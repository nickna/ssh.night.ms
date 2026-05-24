// Package components — block-glyph chart utilities used by the finance screens.
//
// Sparkline is the one-line inline chart (each output character represents one
// bucket from the input series, scaled into the 8-step block alphabet).
//
// BigChart is the multi-row detail chart (each column shows the bucketed value
// as filled rows from the bottom, with the topmost row using a partial-block
// glyph for sub-row resolution).
//
// Ported from ssh.night.ms/.../Providers/Finance/Sparkline.cs.
package components

import (
	"math"
	"strings"
)

// blocks is U+2581 .. U+2588 — eight ascending lower-block glyphs.
var blocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline renders a series of numbers as a single line of block glyphs.
// Returns empty string when series is empty / width <= 0 / all values are
// NaN+Inf. A constant series renders as a flat row of the lowest glyph.
func Sparkline(series []float64, width int) string {
	if len(series) == 0 || width <= 0 {
		return ""
	}
	buckets := bucketize(series, width, false)
	if len(buckets) == 0 {
		return ""
	}
	min, max, ok := minMaxFinite(buckets)
	if !ok {
		return ""
	}
	rang := max - min
	out := make([]rune, len(buckets))
	for i, v := range buckets {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			out[i] = ' '
			continue
		}
		t := 0.0
		if rang > 0 {
			t = (v - min) / rang
		}
		idx := int(math.Round(t * float64(len(blocks)-1)))
		if idx < 0 {
			idx = 0
		} else if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		out[i] = blocks[idx]
	}
	return string(out)
}

// BigChart renders a series as a height-row, width-column block chart. Returns
// `height` strings (top row first). Short series are stretched to fill width
// so a small chart still reads as a chart, not a stub.
func BigChart(series []float64, width, height int) []string {
	if len(series) == 0 || width <= 0 || height <= 0 {
		out := make([]string, 0, height)
		for i := 0; i < height; i++ {
			out = append(out, "")
		}
		return out
	}
	buckets := bucketize(series, width, true)
	blank := strings.Repeat(" ", width)
	if len(buckets) == 0 {
		out := make([]string, height)
		for i := range out {
			out[i] = blank
		}
		return out
	}
	min, max, ok := minMaxFinite(buckets)
	if !ok {
		out := make([]string, height)
		for i := range out {
			out[i] = blank
		}
		return out
	}
	rang := max - min
	rows := make([][]rune, height)
	for r := range rows {
		rows[r] = []rune(blank)
	}
	for c, v := range buckets {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		t := 0.5
		if rang > 0 {
			t = (v - min) / rang
		}
		subRows := int(math.Round(t * float64(height) * 8))
		if subRows < 1 {
			subRows = 1
		}
		if subRows > height*8 {
			subRows = height * 8
		}
		fullRows := subRows / 8
		partial := subRows % 8
		for r := 0; r < fullRows; r++ {
			rows[height-1-r][c] = '█'
		}
		if partial > 0 && fullRows < height {
			rows[height-1-fullRows][c] = blocks[partial-1]
		}
	}
	out := make([]string, height)
	for r := range out {
		out[r] = string(rows[r])
	}
	return out
}

// bucketize partitions a series into `width` buckets averaging values per
// bucket. NaN/Inf inputs are dropped before bucketing. When the series is
// shorter than width: Sparkline (stretch=false) returns the cleaned values
// unchanged; BigChart (stretch=true) repeats values so columns fill the box.
func bucketize(series []float64, width int, stretch bool) []float64 {
	clean := make([]float64, 0, len(series))
	for _, v := range series {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	if len(clean) <= width {
		if !stretch {
			return clean
		}
		out := make([]float64, width)
		for i := 0; i < width; i++ {
			src := int(math.Floor(float64(i) * float64(len(clean)) / float64(width)))
			if src >= len(clean) {
				src = len(clean) - 1
			}
			out[i] = clean[src]
		}
		return out
	}
	out := make([]float64, width)
	step := float64(len(clean)) / float64(width)
	for i := 0; i < width; i++ {
		from := int(math.Floor(float64(i) * step))
		to := int(math.Floor(float64(i+1) * step))
		if to <= from {
			to = from + 1
		}
		if to > len(clean) {
			to = len(clean)
		}
		sum, n := 0.0, 0
		for j := from; j < to; j++ {
			sum += clean[j]
			n++
		}
		if n > 0 {
			out[i] = sum / float64(n)
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}

func minMaxFinite(vs []float64) (min, max float64, ok bool) {
	min, max = math.Inf(1), math.Inf(-1)
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		ok = true
	}
	return
}
