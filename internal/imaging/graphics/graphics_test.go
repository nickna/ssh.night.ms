package graphics

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		term string
		env  []string
		want Protocol
	}{
		{"kitty via term", "xterm-kitty", nil, Kitty},
		{"wezterm via TERM_PROGRAM", "xterm-256color", []string{"TERM_PROGRAM=WezTerm"}, Kitty},
		{"iterm via TERM_PROGRAM", "xterm-256color", []string{"TERM_PROGRAM=iTerm.app"}, Iterm2},
		{"iterm via ITERM_SESSION_ID", "xterm-256color", []string{"ITERM_SESSION_ID=abc"}, Iterm2},
		{"vscode via TERM_PROGRAM", "xterm-256color", []string{"TERM_PROGRAM=vscode"}, Iterm2},
		{"sixel opt-in", "xterm-256color", []string{"NIGHTMS_SIXEL=1"}, Sixel},
		{"default halfblock", "xterm-256color", nil, Halfblock},
		{"empty term", "", nil, Halfblock},
		{"override forces halfblock", "xterm-kitty", []string{"NIGHTMS_BROWSER_GRAPHICS=halfblock"}, Halfblock},
		{"override forces none", "xterm-kitty", []string{"NIGHTMS_BROWSER_GRAPHICS=none"}, None},
		{"override forces iterm2", "xterm-256color", []string{"NIGHTMS_BROWSER_GRAPHICS=iterm2"}, Iterm2},
		{"override garbage falls through to detection", "xterm-kitty", []string{"NIGHTMS_BROWSER_GRAPHICS=bogus"}, Kitty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.term, tc.env); got != tc.want {
				t.Errorf("Detect(%q,%v) = %v, want %v", tc.term, tc.env, got, tc.want)
			}
		})
	}
}

func TestProtocolName(t *testing.T) {
	cases := []struct {
		p    Protocol
		want string
	}{
		{Halfblock, "halfblock"},
		{Kitty, "kitty"},
		{Iterm2, "iterm2"},
		{Sixel, "sixel"},
		{None, "none"},
	}
	for _, tc := range cases {
		if got := tc.p.Name(); got != tc.want {
			t.Errorf("%d.Name() = %q, want %q", tc.p, got, tc.want)
		}
	}
}
