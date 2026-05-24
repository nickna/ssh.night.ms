package screens

import "testing"

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{75204, "$75,204.00"},
		{2054.44, "$2,054.44"},
		{108432.18, "$108,432.18"},
		{1500000.5, "$1,500,000.50"},
		{100, "$100.00"},
		{99.99, "$99.99"},
		{83.80, "$83.80"},
		{1, "$1.00"},
		{0.2411, "$0.2411"},
		{0.01, "$0.0100"},
		{0.000123, "$0.000123"},
	}
	for _, c := range cases {
		got := formatPrice(c.in)
		if got != c.want {
			t.Errorf("formatPrice(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanizeWithCommas(t *testing.T) {
	cases := []struct {
		in       float64
		decimals int
		want     string
	}{
		{0, 2, "0.00"},
		{1, 0, "1"},
		{12, 0, "12"},
		{123, 0, "123"},
		{1234, 0, "1,234"},
		{12345, 0, "12,345"},
		{123456, 0, "123,456"},
		{1234567, 0, "1,234,567"},
		{75204, 2, "75,204.00"},
		{1234.5, 2, "1,234.50"},
	}
	for _, c := range cases {
		got := humanizeWithCommas(c.in, c.decimals)
		if got != c.want {
			t.Errorf("humanizeWithCommas(%v, %d) = %q, want %q", c.in, c.decimals, got, c.want)
		}
	}
}
