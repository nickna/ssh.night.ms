package components

import (
	"math"
	"strings"
	"testing"
)

func TestSparklineEmpty(t *testing.T) {
	if got := Sparkline(nil, 10); got != "" {
		t.Errorf("nil → %q, want empty", got)
	}
	if got := Sparkline([]float64{1, 2, 3}, 0); got != "" {
		t.Errorf("width 0 → %q, want empty", got)
	}
	if got := Sparkline([]float64{math.NaN(), math.Inf(1)}, 10); got != "" {
		t.Errorf("all NaN/Inf → %q, want empty", got)
	}
}

func TestSparklineMonotonic(t *testing.T) {
	got := Sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8}, 8)
	want := "▁▂▃▄▅▆▇█"
	if got != want {
		t.Errorf("monotonic 1..8 → %q, want %q", got, want)
	}
}

func TestSparklineConstant(t *testing.T) {
	got := Sparkline([]float64{5, 5, 5, 5}, 4)
	if got != "▁▁▁▁" {
		t.Errorf("constant → %q, want flat row of lowest glyph", got)
	}
}

func TestSparklineShortSeriesDoesNotStretch(t *testing.T) {
	got := Sparkline([]float64{1, 5, 9}, 10)
	if len([]rune(got)) != 3 {
		t.Errorf("len(%q) = %d, want 3 (short series unchanged)", got, len([]rune(got)))
	}
}

func TestSparklineBuckets(t *testing.T) {
	series := []float64{0, 0, 0, 0, 9, 9, 9, 9}
	got := Sparkline(series, 2)
	if len([]rune(got)) != 2 {
		t.Errorf("len(%q) = %d, want 2", got, len([]rune(got)))
	}
	runes := []rune(got)
	if runes[0] != '▁' || runes[1] != '█' {
		t.Errorf("got %q, want ▁█ (two buckets, min then max)", got)
	}
}

func TestBigChartShape(t *testing.T) {
	got := BigChart([]float64{1, 2, 3, 4, 5}, 12, 10)
	if len(got) != 10 {
		t.Errorf("got %d rows, want 10", len(got))
	}
	for i, row := range got {
		if len([]rune(row)) != 12 {
			t.Errorf("row %d width = %d, want 12", i, len([]rune(row)))
		}
	}
	// Last row should have at least one filled cell (every column gets ≥1 sub-row).
	if !strings.ContainsAny(got[len(got)-1], "▁▂▃▄▅▆▇█") {
		t.Errorf("bottom row has no fill: %q", got[len(got)-1])
	}
}

func TestBigChartEmpty(t *testing.T) {
	got := BigChart(nil, 12, 10)
	if len(got) != 10 {
		t.Errorf("got %d rows, want 10", len(got))
	}
}
