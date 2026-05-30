package onenote

import (
	"strings"
	"testing"
)

func TestMarkdownToHTML_Blocks(t *testing.T) {
	md := "# Title\n\nHello **world** and `code`.\n\n- a\n- b\n\n1. one\n2. two\n\n> a quote\n\n```\nline1\nline2\n```"
	got := markdownToHTML(md)
	wants := []string{
		"<h1>Title</h1>",
		"<p>Hello <b>world</b> and <code>code</code>.</p>",
		"<ul><li>a</li><li>b</li></ul>",
		"<ol><li>one</li><li>two</li></ol>",
		"<blockquote>a quote</blockquote>",
		"<pre>line1\nline2</pre>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestMarkdownToHTML_Escaping(t *testing.T) {
	got := markdownToHTML("a < b & c > d")
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") || !strings.Contains(got, "&gt;") {
		t.Fatalf("expected HTML-escaped output, got %q", got)
	}
}

func TestMarkdownToPageHTML_Wraps(t *testing.T) {
	got := markdownToPageHTML("My <Note>", "Body")
	if !strings.HasPrefix(got, "<!DOCTYPE html><html><head><title>") {
		t.Fatalf("missing doc prefix: %q", got)
	}
	if !strings.Contains(got, "<title>My &lt;Note&gt;</title>") {
		t.Errorf("title not escaped: %q", got)
	}
	if !strings.Contains(got, "<body><p>Body</p></body>") {
		t.Errorf("body wrong: %q", got)
	}
}

func TestMarkdownToPageHTML_EmptyBody(t *testing.T) {
	got := markdownToPageHTML("T", "   ")
	if !strings.Contains(got, "<body><p></p></body>") {
		t.Fatalf("empty body should produce a blank paragraph: %q", got)
	}
}

func TestMarkdownBlocks_Count(t *testing.T) {
	blocks := markdownBlocks("para one\n\npara two\n\n- list item")
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3: %v", len(blocks), blocks)
	}
}

func TestEditMarkdown_RoundTripKinds(t *testing.T) {
	pc := PageContent{Blocks: []Block{
		{Kind: BlockHeading, Text: "Head"},
		{Kind: BlockParagraph, Text: "Para"},
		{Kind: BlockList, Text: "• one\n• two"},
		{Kind: BlockImage, Text: "alt"},
	}}
	md := pc.EditMarkdown()
	for _, want := range []string{"## Head", "Para", "- one", "- two", "[image: alt]"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}
