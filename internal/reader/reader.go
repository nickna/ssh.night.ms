// Package reader extracts the readable body of an arbitrary web page into a
// structured block list the TUI can render with word wrap. Wraps
// go-shiori/go-readability for the extraction itself — that library handles
// the boilerplate of stripping nav/ads/sidebars and surfacing the article.
//
// The HTML walker emits Paragraph / Heading / Quote / Code / List blocks
// from go-readability's cleaned HTML; if HTML parse fails (or content is
// empty), it falls back to paragraphsFromText on article.TextContent.
package reader

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

// Article is the rendered-ready shape the TUI consumes.
type Article struct {
	Title  string
	Byline string
	Host   string  // hostname of the source URL, for the screen header
	Blocks []Block // ordered body content
	URL    string  // source URL, for "open in browser" hints
}

// Block is the sum type for body content. Defined as a concrete struct (not
// an interface) so the TUI's switch is exhaustive against a known set.
// URL is populated only for BlockImage; Text holds the paragraph/heading/
// quote/code/list payload for the text kinds and the image alt text for
// BlockImage.
type Block struct {
	Kind BlockKind
	Text string
	URL  string // image src for BlockImage; empty otherwise
}

// BlockKind is the discriminator. New kinds extend this enum; the renderer
// falls back to BlockParagraph for anything it doesn't recognize.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockQuote
	BlockCode
	BlockList
	BlockImage
)

// Extract fetches the URL and runs go-readability against it, returning a
// structured Article. Failures (network, dead URL, no extractable content)
// surface as plain errors the caller renders as a notice.
//
// The `timeout` covers the whole fetch + parse. 15s is sane for the typical
// page; some news sites with heavy CSPs occasionally bump up against that.
func Extract(ctx context.Context, rawURL string, timeout time.Duration) (Article, error) {
	if rawURL == "" {
		return Article{}, fmt.Errorf("reader: empty url")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Article{}, fmt.Errorf("reader: parse url: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	article, err := readability.FromURL(rawURL, timeout)
	if err != nil {
		return Article{}, fmt.Errorf("reader: fetch %q: %w", rawURL, err)
	}

	// Guard against ctx cancellation that the readability call didn't honor.
	if ctx.Err() != nil {
		return Article{}, ctx.Err()
	}

	// Prefer the structured HTML walker. If it produces zero blocks (parse
	// failed or the cleaned content is empty), fall back to the legacy
	// TextContent splitter so we don't regress in the worst case.
	blocks := blocksFromHTML(article.Content, parsed)
	if len(blocks) == 0 {
		blocks = paragraphsFromText(article.TextContent)
	}
	if len(blocks) == 0 {
		// Some pages return no usable text (paywall, JS-only, etc.). Surface a
		// clear error so the screen can suggest the source URL instead.
		return Article{}, fmt.Errorf("reader: no extractable text in %q", rawURL)
	}

	return Article{
		Title:  strings.TrimSpace(article.Title),
		Byline: strings.TrimSpace(article.Byline),
		Host:   parsed.Hostname(),
		Blocks: blocks,
		URL:    rawURL,
	}, nil
}

// paragraphsFromText splits raw extracted text into paragraph blocks. Blank
// lines (one or more consecutive \n) are the paragraph separator. Trims
// leading/trailing whitespace so the renderer doesn't paint empty rows.
func paragraphsFromText(text string) []Block {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	// Normalize Windows / Mac line endings.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out []Block
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		p := strings.TrimSpace(cur.String())
		if p != "" {
			out = append(out, Block{Kind: BlockParagraph, Text: collapseSpaces(p)})
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(trimmed)
	}
	flush()
	return out
}

// blocksFromHTML walks go-readability's cleaned content HTML and emits
// structured Block values. Nested headings/paragraphs/quotes/lists/code are
// flattened — the renderer doesn't track outline depth — but tags carry the
// semantic kind so the screen can style them differently.
//
// Unknown tags are descended into so their text content still surfaces;
// strict HTML structure isn't required (readability hands us slightly
// imperfect markup sometimes).
func blocksFromHTML(content string, base *url.URL) []Block {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	root, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}
	var out []Block
	walkBlock(root, &out, base)
	// Drop fully-empty entries the walker may have left behind. Image blocks
	// have empty Text (alt was missing) but a populated URL, so they need a
	// separate keep rule.
	cleaned := out[:0]
	for _, b := range out {
		if b.Kind == BlockImage {
			if b.URL == "" {
				continue
			}
		} else if strings.TrimSpace(b.Text) == "" {
			continue
		}
		cleaned = append(cleaned, b)
	}
	return cleaned
}

// walkBlock walks the DOM and emits one Block per block-level element. List
// items are flattened into "• item" / "1. item" lines inside a single
// BlockList; code blocks preserve internal whitespace + line breaks. <img>
// tags are emitted as BlockImage with the resolved absolute URL — the
// renderer downloads + draws the bytes itself.
func walkBlock(n *html.Node, out *[]Block, base *url.URL) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		switch strings.ToLower(n.Data) {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			*out = append(*out, Block{Kind: BlockHeading, Text: collapseSpaces(textOf(n))})
			return
		case "p":
			text := collapseSpaces(textOf(n))
			if text != "" {
				*out = append(*out, Block{Kind: BlockParagraph, Text: text})
			}
			return
		case "blockquote":
			text := collapseSpaces(textOf(n))
			if text != "" {
				*out = append(*out, Block{Kind: BlockQuote, Text: text})
			}
			return
		case "pre":
			// Preserve whitespace + line breaks for code blocks. A nested
			// <code> child is treated the same way — readability often nests
			// <pre><code> for syntax-highlighted snippets.
			text := strings.TrimRight(textOfPreserve(n), "\n")
			if text != "" {
				*out = append(*out, Block{Kind: BlockCode, Text: text})
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
				body := strings.TrimSpace(collapseSpaces(textOf(c)))
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
				*out = append(*out, Block{Kind: BlockList, Text: strings.Join(lines, "\n")})
			}
			return
		case "img":
			if resolved := resolveImageSrc(n, base); resolved != "" {
				*out = append(*out, Block{Kind: BlockImage, Text: attrValue(n, "alt"), URL: resolved})
			}
			return
		case "figcaption":
			text := collapseSpaces(textOf(n))
			if text != "" {
				*out = append(*out, Block{Kind: BlockParagraph, Text: text})
			}
			return
		case "figure":
			// Figures usually wrap an <img> + <figcaption>. Fall through to
			// the child-descent loop below so each emits its own block.
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkBlock(c, out, base)
	}
}

// resolveImageSrc picks the best src from <img> attrs (preferring the largest
// srcset candidate over src) and resolves it against the article's base URL.
// Returns "" when no usable absolute http(s) URL can be built or the source
// looks like a tracking pixel.
func resolveImageSrc(n *html.Node, base *url.URL) string {
	src := pickSrcset(attrValue(n, "srcset"))
	if src == "" {
		src = attrValue(n, "src")
	}
	if src == "" {
		src = attrValue(n, "data-src")
	}
	src = strings.TrimSpace(src)
	if src == "" || strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "#") {
		return ""
	}
	ref, err := url.Parse(src)
	if err != nil {
		return ""
	}
	if base != nil {
		ref = base.ResolveReference(ref)
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return ""
	}
	// Skip the obvious 1×1 tracking pixels that show up in newsletter HTML.
	low := strings.ToLower(ref.Path)
	if strings.Contains(low, "1x1.") || strings.Contains(low, "pixel.gif") || strings.Contains(low, "/tr?") {
		return ""
	}
	return ref.String()
}

// pickSrcset returns the highest-density candidate URL from a srcset attr,
// or "" when the attr is empty / unparseable. Whitespace in CSS-style srcset
// makes the parsing fiddly but the format is well-defined: comma-separated
// candidates, each "url descriptor" where descriptor is e.g. "2x" or "800w".
func pickSrcset(srcset string) string {
	srcset = strings.TrimSpace(srcset)
	if srcset == "" {
		return ""
	}
	best := ""
	bestScore := -1.0
	for _, cand := range strings.Split(srcset, ",") {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		parts := strings.Fields(cand)
		u := parts[0]
		score := 1.0
		if len(parts) > 1 {
			d := parts[1]
			switch {
			case strings.HasSuffix(d, "w"):
				if n, err := strconv.ParseFloat(strings.TrimSuffix(d, "w"), 64); err == nil {
					score = n
				}
			case strings.HasSuffix(d, "x"):
				if n, err := strconv.ParseFloat(strings.TrimSuffix(d, "x"), 64); err == nil {
					score = n * 1000
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = u
		}
	}
	return best
}

// attrValue returns the value of the named attribute on n, or "".
func attrValue(n *html.Node, name string) string {
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

// textOf collects the concatenated text content of a subtree, collapsing
// whitespace at the seams between text nodes. <br> introduces a space so
// "line1<br>line2" doesn't become "line1line2".
func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
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

// textOfPreserve is like textOf but keeps internal whitespace intact —
// used for <pre> blocks where indentation carries meaning.
func textOfPreserve(n *html.Node) string {
	if n == nil {
		return ""
	}
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

// collapseSpaces collapses any run of internal whitespace into a single space.
// Helps when the upstream extractor leaves stray tabs / non-breaking spaces.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
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
	return b.String()
}
