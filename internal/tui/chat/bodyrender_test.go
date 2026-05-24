package chat

import (
	"strings"
	"testing"
)

func TestTokenizeBody_PlainText(t *testing.T) {
	tokens, mentioned := TokenizeBody("hello world", "alice")
	if mentioned {
		t.Fatalf("plain text should not be self-mention")
	}
	if len(tokens) != 1 || tokens[0].Kind != BodyPlain || tokens[0].Text != "hello world" {
		t.Fatalf("plain text round-trip: %+v", tokens)
	}
}

func TestTokenizeBody_BoldItalicCode(t *testing.T) {
	tokens, _ := TokenizeBody("a *bold* b _italic_ c `code` d", "alice")
	want := []struct {
		kind BodyKind
		text string
	}{
		{BodyPlain, "a "},
		{BodyBold, "bold"},
		{BodyPlain, " b "},
		{BodyItalic, "italic"},
		{BodyPlain, " c "},
		{BodyCode, "code"},
		{BodyPlain, " d"},
	}
	if len(tokens) != len(want) {
		t.Fatalf("token count: got %d want %d (%+v)", len(tokens), len(want), tokens)
	}
	for i, w := range want {
		if tokens[i].Kind != w.kind || tokens[i].Text != w.text {
			t.Errorf("token %d: got {%v %q} want {%v %q}", i, tokens[i].Kind, tokens[i].Text, w.kind, w.text)
		}
	}
}

func TestTokenizeBody_MentionSelfVsOther(t *testing.T) {
	tokens, mentioned := TokenizeBody("hey @Alice and @bob", "alice")
	if !mentioned {
		t.Fatal("Alice should match self (case-insensitive)")
	}
	// Find the @Alice and @bob tokens.
	var sawSelf, sawOther bool
	for _, tok := range tokens {
		switch {
		case tok.Kind == BodyMentionSelf && tok.Text == "@Alice":
			sawSelf = true
		case tok.Kind == BodyMentionOther && tok.Text == "@bob":
			sawOther = true
		}
	}
	if !sawSelf || !sawOther {
		t.Fatalf("expected self+other mentions, got %+v", tokens)
	}
}

func TestTokenizeBody_NotAMention_EmailBoundary(t *testing.T) {
	// "alice@host" must NOT be treated as a mention — the @ is glued to a
	// preceding identifier byte.
	tokens, mentioned := TokenizeBody("send mail to alice@host today", "alice")
	if mentioned {
		t.Fatal("alice@host should not register as self-mention")
	}
	joined := ""
	for _, tok := range tokens {
		if tok.Kind != BodyPlain {
			t.Errorf("unexpected non-plain token %+v", tok)
		}
		joined += tok.Text
	}
	if joined != "send mail to alice@host today" {
		t.Errorf("round-trip mismatch: %q", joined)
	}
}

func TestTokenizeBody_EmojiSubstitute(t *testing.T) {
	tokens, _ := TokenizeBody("hi :wave: there", "alice")
	if len(tokens) != 1 || !strings.Contains(tokens[0].Text, "\U0001F44B") {
		t.Fatalf("expected wave glyph, got %+v", tokens)
	}
}

func TestWrapBodyLines_WordWrap(t *testing.T) {
	lines, _ := WrapBodyLines("alpha beta gamma delta", "", 12)
	if len(lines) < 2 {
		t.Fatalf("expected wrap onto multiple lines, got %v", lines)
	}
}

func TestWrapBodyLines_PreservesAllContent(t *testing.T) {
	// "bold across multiple words" should wrap without dropping content.
	// Lipgloss strips SGR in non-tty test environments, so we assert on the
	// plain text shape (whitespace permitting) instead of escape bytes.
	lines, _ := WrapBodyLines("plain *bold across multiple words* end", "", 14)
	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d: %v", len(lines), lines)
	}
	joined := strings.Join(lines, " ")
	for _, w := range []string{"plain", "bold", "across", "multiple", "words", "end"} {
		if !strings.Contains(joined, w) {
			t.Errorf("wrap dropped %q: lines=%v", w, lines)
		}
	}
}

func TestHandleColor_Stable(t *testing.T) {
	a1 := HandleColor("Alice")
	a2 := HandleColor("alice")
	if a1 != a2 {
		t.Errorf("HandleColor should be case-insensitive: %q vs %q", a1, a2)
	}
	if a1 == "" {
		t.Error("non-empty handle returned empty color")
	}
	if HandleColor("") != "" {
		t.Error("empty handle should return empty color")
	}
}
