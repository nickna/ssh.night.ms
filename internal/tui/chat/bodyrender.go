// Package chat: rendering helpers for chat message bodies. These mirror the
// .NET MessageRenderer.AppendBodyRuns logic — `*bold*`, `_italic_`,
// `` `code` ``, `@mention` (self vs. other), `:emoji:` — so the Go TUI paints
// chat with the same visual vocabulary as the .NET original.
package chat

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Color constants for the body palette. Match ChatPalette.cs:
//   - MentionOther — soft sky blue
//   - MentionSelf  — bright yellow, bold (also flagged via SelfMentioned)
//   - BoldFg       — pure white
//   - CodeFg       — pale green so inline code reads as a separate channel
//   - Italic       — default fg + italic
//   - Plain        — default fg
const (
	ColorMentionOther = "#6CC0FF"
	ColorMentionSelf  = "#FFD700"
	ColorBoldFg       = "#FFFFFF"
	ColorCodeFg       = "#9FE59F"
)

// BodyKind discriminates the style of a single body token.
type BodyKind int

const (
	BodyPlain BodyKind = iota
	BodyBold
	BodyItalic
	BodyCode
	BodyMentionOther
	BodyMentionSelf
)

// BodyToken is one contiguous run of body text + its style. Wrapping operates
// on these so a single styled span can flow across multiple display lines
// without losing its color.
type BodyToken struct {
	Kind BodyKind
	Text string
}

// TokenizeBody parses a message body into typed runs. selfHandle is matched
// case-insensitively against @mentions to decide BodyMentionSelf vs
// BodyMentionOther. Returns the run list and whether selfHandle was mentioned.
//
// Mirrors the .NET MessageRenderer Inline regex but as a hand-written scan to
// avoid Go regexp's lack of lookbehind. Rules:
//   - `*foo*`   bold      — non-space immediately inside both stars, single line
//   - `_foo_`   italic    — same shape with underscores
//   - `` `foo` `` code    — anything except backtick/newline inside
//   - `@name`   mention   — name = [A-Za-z0-9][A-Za-z0-9_-]{0,31}; preceded by start or non-alnum/_
func TokenizeBody(body, selfHandle string) ([]BodyToken, bool) {
	text := SubstituteEmoji(body)
	if text == "" {
		return nil, false
	}
	var out []BodyToken
	selfMentioned := false
	plain := strings.Builder{}
	flush := func() {
		if plain.Len() == 0 {
			return
		}
		out = append(out, BodyToken{Kind: BodyPlain, Text: plain.String()})
		plain.Reset()
	}

	i := 0
	for i < len(text) {
		c := text[i]
		switch c {
		case '@':
			if end, ok := matchMention(text, i); ok {
				name := text[i+1 : end]
				flush()
				if strings.EqualFold(name, selfHandle) {
					out = append(out, BodyToken{Kind: BodyMentionSelf, Text: text[i:end]})
					selfMentioned = true
				} else {
					out = append(out, BodyToken{Kind: BodyMentionOther, Text: text[i:end]})
				}
				i = end
				continue
			}
		case '*':
			if end, ok := matchPaired(text, i, '*'); ok {
				flush()
				out = append(out, BodyToken{Kind: BodyBold, Text: text[i+1 : end-1]})
				i = end
				continue
			}
		case '_':
			if end, ok := matchPaired(text, i, '_'); ok {
				flush()
				out = append(out, BodyToken{Kind: BodyItalic, Text: text[i+1 : end-1]})
				i = end
				continue
			}
		case '`':
			if end, ok := matchCode(text, i); ok {
				flush()
				out = append(out, BodyToken{Kind: BodyCode, Text: text[i+1 : end-1]})
				i = end
				continue
			}
		}
		plain.WriteByte(c)
		i++
	}
	flush()
	return out, selfMentioned
}

// matchMention returns (oneAfterEnd, true) if text[i] starts a valid @mention.
// Requires a non-alnum/underscore byte (or start-of-text) on the left so
// "x@host" isn't treated as a mention.
func matchMention(text string, i int) (int, bool) {
	if text[i] != '@' {
		return 0, false
	}
	if i > 0 {
		prev := text[i-1]
		if isIdentByte(prev) {
			return 0, false
		}
	}
	// First name byte must be alnum.
	j := i + 1
	if j >= len(text) || !isAlnum(text[j]) {
		return 0, false
	}
	j++
	limit := i + 1 + 32 // name capped at 32 bytes
	if limit > len(text) {
		limit = len(text)
	}
	for j < limit && isHandleByte(text[j]) {
		j++
	}
	return j, true
}

// matchPaired matches `marker` + (non-marker, no-newline)+ + `marker` with
// non-space directly inside both markers, and no other instance of the same
// marker in between. Returns the index one past the closing marker.
func matchPaired(text string, i int, marker byte) (int, bool) {
	if text[i] != marker {
		return 0, false
	}
	if i+1 >= len(text) {
		return 0, false
	}
	if isSpace(text[i+1]) || text[i+1] == marker {
		return 0, false
	}
	for j := i + 2; j < len(text); j++ {
		c := text[j]
		if c == '\n' {
			return 0, false
		}
		if c == marker {
			if isSpace(text[j-1]) {
				return 0, false
			}
			return j + 1, true
		}
	}
	return 0, false
}

// matchCode matches a backtick-delimited inline-code span: any non-backtick,
// non-newline character inside, at least one byte.
func matchCode(text string, i int) (int, bool) {
	if text[i] != '`' {
		return 0, false
	}
	for j := i + 1; j < len(text); j++ {
		c := text[j]
		if c == '\n' {
			return 0, false
		}
		if c == '`' {
			if j == i+1 {
				return 0, false
			}
			return j + 1, true
		}
	}
	return 0, false
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isHandleByte(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-'
}

func isIdentByte(c byte) bool {
	return isAlnum(c) || c == '_'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// styleFor returns the lipgloss style for a body kind. Italic and bold are
// applied here so callers get a single Render() per token.
func styleFor(kind BodyKind) lipgloss.Style {
	switch kind {
	case BodyBold:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorBoldFg))
	case BodyItalic:
		return lipgloss.NewStyle().Italic(true)
	case BodyCode:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorCodeFg))
	case BodyMentionOther:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorMentionOther))
	case BodyMentionSelf:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorMentionSelf))
	}
	return lipgloss.NewStyle()
}

// RenderTokens concatenates the styled rendering of every token.
func RenderTokens(tokens []BodyToken) string {
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(styleFor(t.Kind).Render(t.Text))
	}
	return b.String()
}

// WrapBodyLines tokenizes `body`, then word-wraps the token stream to `width`
// columns. Each output line is the lipgloss-rendered concatenation of its
// constituent (possibly split) tokens, so multi-word styled spans keep their
// color across wrap points. Returns the wrapped lines + whether selfHandle
// was mentioned anywhere in the input.
//
// Width semantics are "display cells" — go-runewidth handles wide glyphs
// (CJK, emoji). A token containing a soft newline is split into multiple
// paragraphs at the newline; empty paragraphs render as blank lines.
func WrapBodyLines(body, selfHandle string, width int) ([]string, bool) {
	tokens, mentioned := TokenizeBody(body, selfHandle)
	if len(tokens) == 0 {
		return []string{""}, mentioned
	}
	if width <= 0 {
		// No budget; just dump styled content on one line.
		return []string{RenderTokens(tokens)}, mentioned
	}

	// Split each token on '\n' first; each piece becomes a "paragraph atom"
	// inside a single logical paragraph. The carrier knows whether the next
	// atom begins a new line.
	type atom struct {
		kind  BodyKind
		text  string
		newln bool // true => emit hard line break BEFORE this atom
	}
	var atoms []atom
	for _, t := range tokens {
		parts := strings.Split(t.Text, "\n")
		for pi, p := range parts {
			atoms = append(atoms, atom{kind: t.Kind, text: p, newln: pi > 0})
		}
	}

	// Wrap atoms into lines greedily by words. A "word" is a maximal
	// non-space run inside an atom. Atoms preserve adjacency so a styled
	// "world" coming after a plain "hello " concatenates without space.
	type lineRun struct {
		kind BodyKind
		text string
	}
	var lines [][]lineRun
	var current []lineRun
	currentWidth := 0
	pushLine := func() {
		lines = append(lines, current)
		current = nil
		currentWidth = 0
	}
	appendRun := func(kind BodyKind, text string) {
		if text == "" {
			return
		}
		// Merge with the trailing run if same kind so adjacent same-style
		// fragments share a single Render call.
		if n := len(current); n > 0 && current[n-1].kind == kind {
			current[n-1].text += text
			currentWidth += runewidth.StringWidth(text)
			return
		}
		current = append(current, lineRun{kind: kind, text: text})
		currentWidth += runewidth.StringWidth(text)
	}

	for _, a := range atoms {
		if a.newln {
			pushLine()
		}
		if a.text == "" {
			continue
		}
		// Tokenize the atom into (word, trailing-space) chunks so we can
		// soft-break at spaces.
		s := a.text
		j := 0
		// Special case: if this atom starts with leading spaces and we're at
		// line start, drop them. Otherwise carry them so "@alice waves" keeps
		// its space between mention and "waves".
		if currentWidth == 0 {
			for j < len(s) && s[j] == ' ' {
				j++
			}
		}
		for j < len(s) {
			// Read one word.
			wordStart := j
			for j < len(s) && s[j] != ' ' {
				j++
			}
			word := s[wordStart:j]
			ww := runewidth.StringWidth(word)
			// Read trailing spaces (could be zero).
			spaceStart := j
			for j < len(s) && s[j] == ' ' {
				j++
			}
			spaces := s[spaceStart:j]
			sw := runewidth.StringWidth(spaces)

			if word != "" {
				if currentWidth+ww > width && currentWidth > 0 {
					pushLine()
					// If word alone is wider than budget, just place it and
					// move on — hard-truncation is not the chat UX we want.
				}
				appendRun(a.kind, word)
			}
			if spaces != "" {
				// Trailing whitespace at line end gets dropped on wrap.
				if currentWidth+sw > width {
					// Drop the trailing spaces; the wrap point is here.
					pushLine()
				} else {
					appendRun(BodyPlain, spaces)
				}
			}
		}
	}
	pushLine()

	// Render each line.
	rendered := make([]string, 0, len(lines))
	for _, ln := range lines {
		if len(ln) == 0 {
			rendered = append(rendered, "")
			continue
		}
		var b strings.Builder
		for _, r := range ln {
			b.WriteString(styleFor(r.kind).Render(r.text))
		}
		rendered = append(rendered, b.String())
	}
	if len(rendered) == 0 {
		rendered = []string{""}
	}
	return rendered, mentioned
}

