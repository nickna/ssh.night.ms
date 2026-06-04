package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChatTemplatesParse guards the two new chat templates against a malformed
// {{...}} action — parseTemplates is otherwise only run at NewServer() time.
func TestChatTemplatesParse(t *testing.T) {
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	for _, page := range []string{"chat_index", "chat_channel"} {
		if _, ok := tpl[page]; !ok {
			t.Errorf("template %q missing from set", page)
		}
	}
}

// Every /chat route requires a session. The login gate fires before any
// dependency is touched, so a zero-value handlers + identity-free request is
// enough to exercise it.
func TestChatRoutesRequireLogin(t *testing.T) {
	h := &handlers{}
	cases := []struct {
		name    string
		method  string
		target  string
		handler http.HandlerFunc
	}{
		{"index", http.MethodGet, "/chat", h.chatIndex},
		{"channel", http.MethodGet, "/chat/1", h.chatChannel},
		{"send", http.MethodPost, "/chat/1/send", h.chatSend},
		{"join", http.MethodPost, "/chat/join", h.chatJoin},
		{"dm", http.MethodPost, "/chat/dm", h.chatStartDM},
		{"stream", http.MethodGet, "/chat/1/stream", h.chatStream},
		{"react", http.MethodPost, "/chat/1/react", h.chatReact},
		{"unreact", http.MethodPost, "/chat/1/unreact", h.chatUnreact},
		{"pin", http.MethodPost, "/chat/1/pin", h.chatPin},
		{"edit", http.MethodPost, "/chat/1/edit", h.chatEdit},
		{"delete", http.MethodPost, "/chat/1/delete", h.chatDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			w := httptest.NewRecorder()
			tc.handler(w, req)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
			}
			if loc := w.Header().Get("Location"); loc != "/login" {
				t.Errorf("Location = %q, want %q", loc, "/login")
			}
		})
	}
}

func TestRenderChatBody(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		self       string
		want       string
		wantAction bool
	}{
		{"plain text passes through", "hello world", "me", "hello world", false},
		{"html is escaped", "<b>x</b> & y", "me", "&lt;b&gt;x&lt;/b&gt; &amp; y", false},
		{"emoji shortcode substituted", "ship it :fire:", "me", "ship it \U0001F525", false},
		{"escaping applies after substitution", "<:fire:>", "me", "&lt;\U0001F525&gt;", false},
		{"bold", "*hi*", "me", "<strong>hi</strong>", false},
		{"italic", "_hi_", "me", "<em>hi</em>", false},
		{"code escapes inside", "`<x>`", "me", "<code>&lt;x&gt;</code>", false},
		{"mention other", "yo @bob", "me", `yo <span class="mention">@bob</span>`, false},
		{"mention self", "yo @me", "me", `yo <span class="mention-self">@me</span>`, false},
		{"me action strips marker", "/me waves", "me", "waves", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, action := renderChatBody(tt.in, tt.self)
			if string(got) != tt.want {
				t.Errorf("renderChatBody(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if action != tt.wantAction {
				t.Errorf("renderChatBody(%q) action = %v, want %v", tt.in, action, tt.wantAction)
			}
		})
	}
}

func TestValidReaction(t *testing.T) {
	for _, e := range chatReactionPalette {
		if !validReaction(e) {
			t.Errorf("palette emoji %q rejected by validReaction", e)
		}
	}
	for _, bad := range []string{"", "x", ":fire:", "<script>", "🦄"} {
		if validReaction(bad) {
			t.Errorf("validReaction(%q) = true, want false", bad)
		}
	}
}

func TestDMPartner(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		self    string
		want    string
	}{
		{"self is lo half", "dm-alice-bob", "alice", "bob"},
		{"self is hi half", "dm-alice-bob", "bob", "alice"},
		{"case-insensitive self", "dm-alice-bob", "Alice", "bob"},
		{"hyphenated partner, self lo", "dm-a-b-x", "a-b", "x"},
		{"hyphenated partner, self hi", "dm-a-b-x", "x", "a-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dmPartner(tt.channel, tt.self); got != tt.want {
				t.Errorf("dmPartner(%q, %q) = %q, want %q", tt.channel, tt.self, got, tt.want)
			}
		})
	}
}

// Sanity-check the SSE frame shape the client parses: a single "data: <json>"
// line. A raw newline in the marshaled payload would split the frame, so a
// multi-line message body must survive as escaped \n inside the JSON string.
func TestChatStreamFrameShape(t *testing.T) {
	body, _ := renderChatBody("multi\nline", "me")
	payload, err := json.Marshal(chatStreamFrame{
		Kind:   "message_created",
		ID:     7,
		Handle: "alice",
		Body:   string(body),
		Time:   "Jan 2, 3:04pm",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), "\n") {
		t.Errorf("marshaled SSE payload contains a raw newline: %q", payload)
	}
}
