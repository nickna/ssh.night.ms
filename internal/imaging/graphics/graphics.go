// Package graphics detects which terminal graphics protocol the connected
// SSH client supports via $TERM / $TERM_PROGRAM at PTY allocation time.
// Halfblock (imaging.RenderToANSILines) is the universal fallback and
// currently the only renderer; per-protocol encoders would layer on here.
package graphics

import (
	"strings"
)

// Protocol identifies the inline-image transport. Halfblock is the default
// when nothing more specific is detected (or detection is suppressed via
// NIGHTMS_BROWSER_GRAPHICS=halfblock).
type Protocol int

const (
	None Protocol = iota
	Halfblock
	Kitty
	Iterm2
	Sixel
)

// Name returns a stable lowercase identifier used in logs + the env override.
func (p Protocol) Name() string {
	switch p {
	case Halfblock:
		return "halfblock"
	case Kitty:
		return "kitty"
	case Iterm2:
		return "iterm2"
	case Sixel:
		return "sixel"
	case None:
		return "none"
	}
	return "unknown"
}

// Detect picks a Protocol from the client's PTY $TERM value and the SSH
// channel's environ slice (each entry "KEY=value"). Order matters: the env
// override is consulted first so a user can force a protocol when the
// auto-detection is wrong (e.g. inside a tmux that strips graphics).
func Detect(term string, environ []string) Protocol {
	envMap := parseEnviron(environ)
	if forced := envMap["NIGHTMS_BROWSER_GRAPHICS"]; forced != "" {
		if p := parseProtocol(forced); p != None {
			return p
		}
		// "none" explicitly disables inline images.
		if strings.EqualFold(forced, "none") {
			return None
		}
	}
	termLow := strings.ToLower(strings.TrimSpace(term))
	progLow := strings.ToLower(strings.TrimSpace(envMap["TERM_PROGRAM"]))
	if strings.Contains(termLow, "kitty") {
		return Kitty
	}
	if progLow == "wezterm" {
		return Kitty
	}
	if progLow == "iterm.app" || envMap["ITERM_SESSION_ID"] != "" {
		return Iterm2
	}
	if progLow == "vscode" {
		return Iterm2
	}
	if envMap["NIGHTMS_SIXEL"] == "1" {
		return Sixel
	}
	return Halfblock
}

func parseEnviron(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		m[kv[:eq]] = kv[eq+1:]
	}
	return m
}

func parseProtocol(s string) Protocol {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "halfblock":
		return Halfblock
	case "kitty":
		return Kitty
	case "iterm2", "iterm":
		return Iterm2
	case "sixel":
		return Sixel
	}
	return None
}
