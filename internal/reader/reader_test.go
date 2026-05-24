package reader

import (
	"net/url"
	"strings"
	"testing"
)

func TestBlocksFromHTML(t *testing.T) {
	html := `
<h1>Top</h1>
<p>Opening paragraph with <em>emphasis</em>.</p>
<h2>Detail</h2>
<p>Second paragraph.<br>Line two after a br.</p>
<blockquote>Wise words from elsewhere.</blockquote>
<pre><code>line one
  line two indented
line three</code></pre>
<ul>
  <li>first</li>
  <li>second item</li>
</ul>
<ol>
  <li>alpha</li>
  <li>beta</li>
</ol>
`
	blocks := blocksFromHTML(html, nil)
	// Expected: h1, p, h2, p, blockquote, pre, ul, ol → 8 blocks (one list
	// block per <ul>/<ol> rather than a merged list).
	if len(blocks) != 8 {
		t.Fatalf("expected 8 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != BlockHeading || blocks[0].Text != "Top" {
		t.Errorf("blocks[0] = %+v, want Heading Top", blocks[0])
	}
	if blocks[1].Kind != BlockParagraph || !strings.Contains(blocks[1].Text, "emphasis") {
		t.Errorf("blocks[1] = %+v, want Paragraph with emphasis", blocks[1])
	}
	if blocks[2].Kind != BlockHeading || blocks[2].Text != "Detail" {
		t.Errorf("blocks[2] = %+v, want Heading Detail", blocks[2])
	}
	// <br> in paragraph collapses to a space.
	if blocks[3].Kind != BlockParagraph || !strings.Contains(blocks[3].Text, "Second paragraph. Line two") {
		t.Errorf("blocks[3] = %+v, want br-joined paragraph", blocks[3])
	}
	if blocks[4].Kind != BlockQuote {
		t.Errorf("blocks[4] = %+v, want Quote", blocks[4])
	}
	// <pre><code> preserves whitespace.
	if blocks[5].Kind != BlockCode || !strings.Contains(blocks[5].Text, "  line two indented") {
		t.Errorf("blocks[5] = %+v, want Code with indent preserved", blocks[5])
	}
	// <ul> + <ol> both produce BlockList; check each got the right markers.
	if blocks[6].Kind != BlockList || !strings.Contains(blocks[6].Text, "• first") {
		t.Errorf("blocks[6] = %+v, want List with bullets", blocks[6])
	}
	if blocks[7].Kind != BlockList || !strings.Contains(blocks[7].Text, "1. alpha") {
		t.Errorf("blocks[7] = %+v, want List with numbers", blocks[7])
	}
}

func TestBlocksFromHTML_Images(t *testing.T) {
	base, _ := url.Parse("https://example.com/articles/x")
	html := `
<p>Before.</p>
<figure>
  <img src="/photo.jpg" alt="A nice photo">
  <figcaption>Caption text</figcaption>
</figure>
<img srcset="small.jpg 320w, large.jpg 1200w, medium.jpg 640w" alt="srcset choice">
<img src="data:image/png;base64,iVBOR" alt="inline">
<img src="https://tracker.example.com/1x1.gif" alt="pixel">
<img src="//cdn.example.com/banner.png" alt="proto-relative">
<p>After.</p>
`
	blocks := blocksFromHTML(html, base)
	var imgs []Block
	for _, b := range blocks {
		if b.Kind == BlockImage {
			imgs = append(imgs, b)
		}
	}
	if len(imgs) != 3 {
		t.Fatalf("want 3 image blocks (figure img, srcset, proto-relative), got %d: %+v", len(imgs), imgs)
	}
	if imgs[0].URL != "https://example.com/photo.jpg" {
		t.Errorf("figure img URL = %q, want resolved against base", imgs[0].URL)
	}
	if imgs[0].Text != "A nice photo" {
		t.Errorf("figure img Text = %q, want alt text", imgs[0].Text)
	}
	if imgs[1].URL != "https://example.com/articles/large.jpg" {
		t.Errorf("srcset URL = %q, want largest-w candidate", imgs[1].URL)
	}
	if imgs[2].URL != "https://cdn.example.com/banner.png" {
		t.Errorf("proto-relative URL = %q, want https-resolved", imgs[2].URL)
	}
	// And the figcaption text falls through to a paragraph (or heading) —
	// confirm we didn't accidentally swallow it inside the <figure> branch.
	foundCaption := false
	for _, b := range blocks {
		if strings.Contains(b.Text, "Caption text") {
			foundCaption = true
			break
		}
	}
	if !foundCaption {
		t.Errorf("figcaption text didn't surface as its own block: %+v", blocks)
	}
}

func TestPickSrcset(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"only.jpg", "only.jpg"},
		{"small.jpg 320w, big.jpg 1200w", "big.jpg"},
		{"a.jpg 1x, b.jpg 2x, c.jpg 3x", "c.jpg"},
		{"  ", ""},
	}
	for _, tc := range cases {
		if got := pickSrcset(tc.in); got != tc.want {
			t.Errorf("pickSrcset(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBlocksFromHTML_EmptyContent(t *testing.T) {
	if got := blocksFromHTML("", nil); got != nil {
		t.Errorf("empty content should produce nil, got %v", got)
	}
	if got := blocksFromHTML("   \n  ", nil); got != nil {
		t.Errorf("whitespace-only content should produce nil, got %v", got)
	}
}
