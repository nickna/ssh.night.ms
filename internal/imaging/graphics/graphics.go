// Package graphics dispatches inline-image rendering to whichever terminal
// graphics protocol the connected SSH client supports. Halfblock is the
// universal fallback and reuses the existing imaging.RenderToANSILines.
// Kitty + iTerm2 + Sixel layer on for clients that advertise them via
// $TERM / $TERM_PROGRAM at PTY allocation time.
package graphics

import (
	"image"
	"strings"

	"github.com/nickna/ssh.night.ms/internal/imaging"
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

// Encode renders img to a slice of terminal rows for the given protocol.
// cellCols is the desired cell-width; the protocol may scale to honor or
// approximate it. Returns nil on unsupported protocols or render failure
// so the caller can fall through to the halfblock encoder.
func Encode(p Protocol, img image.Image, cellCols int) []string {
	if img == nil || cellCols <= 0 {
		return nil
	}
	switch p {
	case None:
		return nil
	case Kitty:
		return encodeKitty(img, cellCols)
	case Iterm2:
		return encodeIterm2(img, cellCols)
	case Sixel:
		return encodeSixel(img, cellCols)
	default:
		return imaging.RenderToANSILines(img, cellCols)
	}
}

// EncodeWithFallback runs Encode and, when the result is empty (a protocol
// stub returning nil, or a failure), falls back to halfblock so the caller
// always gets *something* paintable. Use this from screens that don't want
// to write their own retry.
func EncodeWithFallback(p Protocol, img image.Image, cellCols int) []string {
	out := Encode(p, img, cellCols)
	if len(out) > 0 {
		return out
	}
	return imaging.RenderToANSILines(img, cellCols)
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
