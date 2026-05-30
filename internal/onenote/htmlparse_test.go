package onenote

import "testing"

func TestParsePageHTML_BlocksAndElements(t *testing.T) {
	html := `<html><head><title>T</title></head><body data-absolute-enabled="true">
		<div>
			<h1 id="h:1">Heading</h1>
			<p id="p:1">First paragraph</p>
			<ul id="u:1"><li>alpha</li><li>beta</li></ul>
			<pre id="c:1">code here</pre>
			<blockquote id="q:1">quoted</blockquote>
		</div>
	</body></html>`
	blocks, elements := parsePageHTML(html)

	wantKinds := []BlockKind{BlockHeading, BlockParagraph, BlockList, BlockCode, BlockQuote}
	if len(blocks) != len(wantKinds) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(wantKinds), blocks)
	}
	for i, k := range wantKinds {
		if blocks[i].Kind != k {
			t.Errorf("block %d kind = %v, want %v", i, blocks[i].Kind, k)
		}
	}
	if blocks[2].Text != "• alpha\n• beta" {
		t.Errorf("list text = %q", blocks[2].Text)
	}

	// Every block carried an id, so every block is editable, in order.
	if len(elements) != len(blocks) {
		t.Fatalf("got %d elements, want %d", len(elements), len(blocks))
	}
	if elements[1].ID != "p:1" || elements[1].Kind != BlockParagraph {
		t.Errorf("element[1] = %+v", elements[1])
	}
}

func TestParsePageHTML_NonTextPlaceholders(t *testing.T) {
	html := `<body>
		<p id="p:1">text</p>
		<img id="i:1" src="https://x/y.png" alt="a picture" />
		<table id="t:1"><tr><td>r1c1</td><td>r1c2</td></tr></table>
	</body>`
	blocks, _ := parsePageHTML(html)
	var sawImage, sawTable bool
	for _, b := range blocks {
		switch b.Kind {
		case BlockImage:
			sawImage = true
			if b.Text != "a picture" || b.URL == "" {
				t.Errorf("image block = %+v", b)
			}
		case BlockTable:
			sawTable = true
			if b.Text != "r1c1\tr1c2" {
				t.Errorf("table text = %q", b.Text)
			}
		}
	}
	if !sawImage || !sawTable {
		t.Fatalf("expected image+table blocks, got %+v", blocks)
	}
}

func TestParsePageHTML_Empty(t *testing.T) {
	blocks, elements := parsePageHTML("")
	if blocks != nil || elements != nil {
		t.Fatalf("empty input should yield nil slices")
	}
}
