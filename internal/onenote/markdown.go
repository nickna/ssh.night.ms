package onenote

import (
	"fmt"
	"html"
	"strings"
)

// markdown.go is the write-side of the text round-trip: a deliberately small,
// dependency-free Markdown subset → OneNote HTML converter, plus the reverse
// Block → Markdown serializer used to populate an edit buffer.
//
// Supported Markdown: ATX headings (#, ##, ### → h1/h2/h3), unordered lists
// (-, *), ordered lists (1.), blockquotes (>), fenced code (```), and
// paragraphs, with inline **bold**, *italic*, and `code`. Tables, images,
// raw HTML, and nested lists are intentionally NOT supported — OneNote's
// editing model and a terminal edit buffer both make them lossy, and silently
// half-supporting them is worse than not at all. Callers gate non-text
// content via PageContent.HasNonText before offering a full rewrite.

// markdownToHTML converts a Markdown document to a sequence of OneNote body
// block elements (no <html>/<body> wrapper). Used for PATCH command content.
func markdownToHTML(md string) string {
	return strings.Join(markdownBlocks(md), "")
}

// markdownBlocks converts a Markdown document to one HTML string per top-level
// block element. markdownToHTML joins these; the full-rewrite path maps each
// block onto an existing page element for in-place replacement.
func markdownBlocks(md string) []string {
	lines := strings.Split(normalizeNewlines(md), "\n")
	var out []string
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		switch {
		case trimmed == "":
			i++ // blank line: paragraph separator, nothing to emit

		case strings.HasPrefix(trimmed, "```"):
			// Fenced code: gather until the closing fence (or EOF).
			var code []string
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // consume closing fence
			}
			out = append(out, "<pre>"+html.EscapeString(strings.Join(code, "\n"))+"</pre>")

		case headingLevel(trimmed) > 0:
			level := headingLevel(trimmed)
			text := strings.TrimSpace(trimmed[level:])
			out = append(out, fmt.Sprintf("<h%d>%s</h%d>", level, inlineToHTML(text), level))
			i++

		case strings.HasPrefix(trimmed, ">"):
			// Blockquote: collapse a run of > lines into one quote.
			var quote []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				q := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"))
				quote = append(quote, q)
				i++
			}
			out = append(out, "<blockquote>"+inlineToHTML(strings.Join(quote, " "))+"</blockquote>")

		case isUnorderedItem(trimmed):
			var b strings.Builder
			b.WriteString("<ul>")
			for i < len(lines) && isUnorderedItem(strings.TrimSpace(lines[i])) {
				fmt.Fprintf(&b, "<li>%s</li>", inlineToHTML(itemText(strings.TrimSpace(lines[i]))))
				i++
			}
			b.WriteString("</ul>")
			out = append(out, b.String())

		case isOrderedItem(trimmed):
			var b strings.Builder
			b.WriteString("<ol>")
			for i < len(lines) && isOrderedItem(strings.TrimSpace(lines[i])) {
				fmt.Fprintf(&b, "<li>%s</li>", inlineToHTML(orderedItemText(strings.TrimSpace(lines[i]))))
				i++
			}
			b.WriteString("</ol>")
			out = append(out, b.String())

		default:
			// Paragraph: gather consecutive non-blank, non-structural lines.
			var para []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || headingLevel(t) > 0 || strings.HasPrefix(t, ">") ||
					strings.HasPrefix(t, "```") || isUnorderedItem(t) || isOrderedItem(t) {
					break
				}
				para = append(para, t)
				i++
			}
			out = append(out, "<p>"+inlineToHTML(strings.Join(para, " "))+"</p>")
		}
	}
	return out
}

// markdownToPageHTML wraps markdownToHTML in the full HTML document OneNote's
// create-page endpoint expects (Content-Type: text/html). The title becomes
// the page <title>; an empty body still produces a valid (blank) page.
func markdownToPageHTML(title, md string) string {
	body := markdownToHTML(md)
	if strings.TrimSpace(body) == "" {
		body = "<p></p>"
	}
	return "<!DOCTYPE html><html><head><title>" +
		html.EscapeString(title) +
		"</title></head><body>" + body + "</body></html>"
}

// EditMarkdown renders the page's blocks into a Markdown edit buffer the
// TUI/REST seeds an editor with. Lossy (see blocksToMarkdown); pair it with
// PageContent.HasNonText to warn before a full rewrite.
func (pc PageContent) EditMarkdown() string { return blocksToMarkdown(pc.Blocks) }

// blocksToMarkdown serializes render-ready blocks back into a Markdown edit
// buffer. Lossy by design: image/table blocks become placeholder lines a user
// shouldn't edit (and which ReplaceBody will drop). Used to seed the TUI/REST
// edit view from a fetched page.
func blocksToMarkdown(blocks []Block) string {
	var parts []string
	for _, blk := range blocks {
		switch blk.Kind {
		case BlockHeading:
			parts = append(parts, "## "+blk.Text)
		case BlockQuote:
			parts = append(parts, "> "+blk.Text)
		case BlockCode:
			parts = append(parts, "```\n"+blk.Text+"\n```")
		case BlockList:
			// Block.Text already holds "• item" / "1. item" lines; normalize
			// the bullet glyph back to Markdown's "-".
			lines := strings.Split(blk.Text, "\n")
			for i, ln := range lines {
				lines[i] = strings.Replace(ln, "• ", "- ", 1)
			}
			parts = append(parts, strings.Join(lines, "\n"))
		case BlockImage:
			parts = append(parts, "[image: "+blk.Text+"]")
		case BlockTable:
			parts = append(parts, "[table]\n"+blk.Text)
		default:
			parts = append(parts, blk.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// --- small helpers -------------------------------------------------------

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// headingLevel returns 1..3 for an ATX heading prefix, else 0. Caps at h3
// because deeper levels add no value in the terminal render.
func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 3 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

func isUnorderedItem(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
}

func itemText(line string) string {
	return strings.TrimSpace(line[2:])
}

// isOrderedItem matches "<digits>. text".
func isOrderedItem(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func orderedItemText(line string) string {
	i := strings.Index(line, ". ")
	if i < 0 {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// inlineToHTML applies inline Markdown (**bold**, *italic*, `code`) after
// HTML-escaping the raw text, so the emitted fragment is always well-formed.
// Order matters: escape first, then wrap the (now-safe) spans. Code spans win
// over emphasis so backticked **literals** stay literal.
func inlineToHTML(s string) string {
	s = html.EscapeString(s)
	s = wrapSpan(s, "`", "<code>", "</code>")
	s = wrapSpan(s, "**", "<b>", "</b>")
	s = wrapSpan(s, "*", "<i>", "</i>")
	return s
}

// wrapSpan replaces balanced pairs of marker with open/close tags. Unbalanced
// trailing markers are left as literal text. Operates on already-escaped input.
func wrapSpan(s, marker, open, close string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, marker)
		if start < 0 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start+len(marker):], marker)
		if end < 0 {
			// No closing marker — emit the rest verbatim.
			b.WriteString(s)
			break
		}
		end += start + len(marker)
		b.WriteString(s[:start])
		b.WriteString(open)
		b.WriteString(s[start+len(marker) : end])
		b.WriteString(close)
		s = s[end+len(marker):]
	}
	return b.String()
}
