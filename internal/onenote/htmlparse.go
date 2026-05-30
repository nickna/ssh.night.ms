package onenote

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// htmlparse.go is the read-side of the round-trip: it walks the HTML returned
// by GET /me/onenote/pages/{id}/content?includeIDs=true and produces both the
// render-ready []Block and the []EditableElement map.
//
// This is a dedicated walker rather than a reuse of internal/reader's
// go-readability path: readability is built to strip web-page chrome from an
// article, whereas OneNote HTML is already pure content with its own quirks
// (absolutely-positioned <div> wrappers, generated id attributes from
// includeIDs, inline position styles). We share the Block *vocabulary* with
// reader, not its extraction logic.
//
// With includeIDs=true, editable elements carry an `id` attribute; a PATCH
// command targets them as "#<id>". We capture that id verbatim.

// parsePageHTML walks a OneNote page content document and returns the ordered
// blocks plus the editable-element map. Returns empty slices on a parse
// failure rather than erroring — a page that won't parse is still safe to
// surface as "no readable content".
func parsePageHTML(content string) (blocks []Block, elements []EditableElement) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}
	root, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil, nil
	}
	body := findBody(root)
	if body == nil {
		body = root
	}
	walkContent(body, &blocks, &elements)
	return blocks, elements
}

// walkContent descends the DOM, emitting one Block per recognized block-level
// element (and an EditableElement when that element carries an id). Container
// elements (div, the body) are descended into; recognized blocks are NOT
// descended into further, so nested inline markup is flattened into the
// block's text rather than double-emitted.
func walkContent(n *html.Node, blocks *[]Block, elements *[]EditableElement) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		switch strings.ToLower(n.Data) {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			emit(blocks, elements, n, Block{Kind: BlockHeading, Text: collapse(textOf(n))})
			return
		case "p":
			text := collapse(textOf(n))
			if text != "" {
				emit(blocks, elements, n, Block{Kind: BlockParagraph, Text: text})
			}
			return
		case "blockquote":
			text := collapse(textOf(n))
			if text != "" {
				emit(blocks, elements, n, Block{Kind: BlockQuote, Text: text})
			}
			return
		case "pre":
			text := strings.TrimRight(textOfPreserve(n), "\n")
			if text != "" {
				emit(blocks, elements, n, Block{Kind: BlockCode, Text: text})
			}
			return
		case "ul", "ol":
			ordered := strings.ToLower(n.Data) == "ol"
			var lines []string
			idx := 1
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.ElementNode || strings.ToLower(c.Data) != "li" {
					continue
				}
				body := strings.TrimSpace(collapse(textOf(c)))
				if body == "" {
					continue
				}
				if ordered {
					lines = append(lines, fmt.Sprintf("%d. %s", idx, body))
					idx++
				} else {
					lines = append(lines, "• "+body)
				}
			}
			if len(lines) > 0 {
				emit(blocks, elements, n, Block{Kind: BlockList, Text: strings.Join(lines, "\n")})
			}
			return
		case "img":
			emit(blocks, elements, n, Block{Kind: BlockImage, Text: attr(n, "alt"), URL: attr(n, "src")})
			return
		case "table":
			// Tables are read-only in v1: flatten to tab-separated rows so the
			// terminal can show the data, but mark them non-text so an edit
			// can't silently destroy them.
			emit(blocks, elements, n, Block{Kind: BlockTable, Text: flattenTable(n)})
			return
		case "object":
			// Embedded files / attachments. Surface a placeholder; not editable.
			name := attr(n, "data-attachment")
			if name == "" {
				name = "attachment"
			}
			emit(blocks, elements, n, Block{Kind: BlockImage, Text: "attachment: " + name})
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkContent(c, blocks, elements)
	}
}

// emit appends the block and, when the element carries an id (includeIDs),
// the matching editable-element entry.
func emit(blocks *[]Block, elements *[]EditableElement, n *html.Node, b Block) {
	*blocks = append(*blocks, b)
	if id := attr(n, "id"); id != "" {
		*elements = append(*elements, EditableElement{ID: id, Kind: b.Kind, Text: b.Text})
	}
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "body" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// flattenTable renders a <table> as tab-separated rows for read-only display.
func flattenTable(n *html.Node) string {
	var rows []string
	var walkRows func(*html.Node)
	walkRows = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.ToLower(node.Data) == "tr" {
			var cells []string
			for c := node.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					switch strings.ToLower(c.Data) {
					case "td", "th":
						cells = append(cells, strings.TrimSpace(collapse(textOf(c))))
					}
				}
			}
			rows = append(rows, strings.Join(cells, "\t"))
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walkRows(c)
		}
	}
	walkRows(n)
	return strings.Join(rows, "\n")
}

func attr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// textOf concatenates a subtree's text, inserting a space at <br> boundaries.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		switch node.Type {
		case html.TextNode:
			b.WriteString(node.Data)
		case html.ElementNode:
			if strings.ToLower(node.Data) == "br" {
				b.WriteByte(' ')
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// textOfPreserve is textOf without whitespace collapsing — for <pre>.
func textOfPreserve(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// collapse squeezes internal whitespace runs to single spaces and trims ends.
func collapse(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if inSpace {
				continue
			}
			inSpace = true
			b.WriteByte(' ')
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
