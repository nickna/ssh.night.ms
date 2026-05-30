package onenote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nickna/ssh.night.ms/internal/auth/usertoken"
)

// fakeTokens implements TokenSource.
type fakeTokens struct {
	token  string
	err    error
	scopes []string
}

func (f *fakeTokens) Token(_ context.Context, _ int64, scopes ...string) (string, error) {
	f.scopes = scopes
	if f.err != nil {
		return "", f.err
	}
	if f.token == "" {
		return "tok", nil
	}
	return f.token, nil
}

// recordedReq captures one outbound Graph request.
type recordedReq struct {
	method      string
	path        string
	rawQuery    string
	contentType string
	auth        string
	body        string
}

// fakeGraph records requests and replies via the per-test handler.
type fakeGraph struct {
	reqs    []recordedReq
	handler func(rr recordedReq) (int, string)
}

func (f *fakeGraph) do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	rr := recordedReq{
		method:      req.Method,
		path:        req.URL.Path,
		rawQuery:    req.URL.RawQuery,
		contentType: req.Header.Get("Content-Type"),
		auth:        req.Header.Get("Authorization"),
		body:        body,
	}
	f.reqs = append(f.reqs, rr)
	status, resp := f.handler(rr)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(resp)),
		Header:     make(http.Header),
	}, nil
}

func newTestService(tokens TokenSource, fg *fakeGraph) *Service {
	return New(Config{
		Tokens:  tokens,
		BaseURL: "http://graph.test",
		HTTPDo:  fg.do,
	})
}

func TestListNotebooks_RequestShapeAndDecode(t *testing.T) {
	tok := &fakeTokens{token: "abc"}
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		return 200, `{"value":[{"id":"nb1","displayName":"Work","isDefault":true,"links":{"oneNoteWebUrl":{"href":"https://web/nb1"}}}]}`
	}}
	svc := newTestService(tok, fg)

	nbs, err := svc.ListNotebooks(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListNotebooks: %v", err)
	}
	if len(nbs) != 1 || nbs[0].ID != "nb1" || nbs[0].Name != "Work" || !nbs[0].IsDefault || nbs[0].WebURL != "https://web/nb1" {
		t.Fatalf("decoded notebook wrong: %+v", nbs)
	}
	rr := fg.reqs[0]
	if rr.method != "GET" || rr.path != "/me/onenote/notebooks" {
		t.Errorf("request = %s %s", rr.method, rr.path)
	}
	if !strings.Contains(rr.rawQuery, "orderby=displayName") {
		t.Errorf("query = %q", rr.rawQuery)
	}
	if rr.auth != "Bearer abc" {
		t.Errorf("auth = %q", rr.auth)
	}
	if len(tok.scopes) != 1 || tok.scopes[0] != "Notes.ReadWrite" {
		t.Errorf("scopes = %v, want [Notes.ReadWrite]", tok.scopes)
	}
}

func TestGetPage_IncludeIDsAndParse(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		if strings.HasSuffix(rr.path, "/content") {
			return 200, `<html><body><p id="p:1">hello</p></body></html>`
		}
		return 200, `{"id":"pg1","title":"Note","parentSection":{"id":"sec1"}}`
	}}
	svc := newTestService(&fakeTokens{}, fg)

	pc, err := svc.GetPage(context.Background(), 1, "pg1")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if pc.Title != "Note" || pc.SectionID != "sec1" {
		t.Errorf("page meta wrong: %+v", pc.Page)
	}
	if len(pc.Elements) != 1 || pc.Elements[0].ID != "p:1" {
		t.Errorf("elements wrong: %+v", pc.Elements)
	}
	// The content fetch must request includeIDs=true.
	var sawIncludeIDs bool
	for _, rr := range fg.reqs {
		if strings.HasSuffix(rr.path, "/content") && strings.Contains(rr.rawQuery, "includeIDs=true") {
			sawIncludeIDs = true
		}
	}
	if !sawIncludeIDs {
		t.Errorf("content fetch missing includeIDs=true; reqs=%+v", fg.reqs)
	}
}

func TestCreatePage_TextHTMLBody(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		return 201, `{"id":"new1","title":"T","parentSection":{"id":"sec1"}}`
	}}
	svc := newTestService(&fakeTokens{}, fg)

	page, err := svc.CreatePage(context.Background(), 1, "sec1", NewPage{Title: "T", Markdown: "hello"})
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if page.ID != "new1" {
		t.Errorf("created page id = %q", page.ID)
	}
	rr := fg.reqs[0]
	if rr.method != "POST" || rr.path != "/me/onenote/sections/sec1/pages" {
		t.Errorf("request = %s %s", rr.method, rr.path)
	}
	if rr.contentType != "text/html" {
		t.Errorf("content-type = %q, want text/html", rr.contentType)
	}
	if !strings.Contains(rr.body, "<title>T</title>") || !strings.Contains(rr.body, "<p>hello</p>") {
		t.Errorf("body = %q", rr.body)
	}
}

func TestAppendBlock_PatchCommandShape(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) { return 204, "" }}
	svc := newTestService(&fakeTokens{}, fg)

	if err := svc.AppendBlock(context.Background(), 1, "pg1", "new line"); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	rr := fg.reqs[0]
	if rr.method != "PATCH" || rr.path != "/me/onenote/pages/pg1/content" {
		t.Errorf("request = %s %s", rr.method, rr.path)
	}
	if rr.contentType != "application/json" {
		t.Errorf("content-type = %q", rr.contentType)
	}
	// Decode the command array so the assertion is robust to JSON's HTML
	// escaping of '<' (Graph decodes < back to '<' fine).
	var cmds []struct{ Target, Action, Content string }
	if err := json.Unmarshal([]byte(rr.body), &cmds); err != nil {
		t.Fatalf("patch body not a JSON array: %v (%s)", err, rr.body)
	}
	if len(cmds) != 1 || cmds[0].Target != "body" || cmds[0].Action != "append" || cmds[0].Content != "<p>new line</p>" {
		t.Errorf("patch command wrong: %+v", cmds)
	}
}

func TestReplaceBody_StrategyA_InPlace(t *testing.T) {
	// Text-only page with two id'd paragraphs; new content also two blocks →
	// in-place replace, page id preserved, no delete/create.
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		switch {
		case strings.HasSuffix(rr.path, "/content") && rr.method == "GET":
			return 200, `<body><p id="p:1">a</p><p id="p:2">b</p></body>`
		case rr.method == "GET":
			return 200, `{"id":"pg1","title":"T","parentSection":{"id":"sec1"}}`
		case rr.method == "PATCH":
			return 204, ""
		}
		return 500, "unexpected"
	}}
	svc := newTestService(&fakeTokens{}, fg)

	page, err := svc.ReplaceBody(context.Background(), 1, "pg1", "x\n\ny", false)
	if err != nil {
		t.Fatalf("ReplaceBody: %v", err)
	}
	if page.ID != "pg1" {
		t.Errorf("strategy A should preserve page id, got %q", page.ID)
	}
	var patch *recordedReq
	for i := range fg.reqs {
		if fg.reqs[i].method == "PATCH" {
			patch = &fg.reqs[i]
		}
		if fg.reqs[i].method == "DELETE" || fg.reqs[i].method == "POST" {
			t.Fatalf("strategy A must not delete/create: %s", fg.reqs[i].method)
		}
	}
	if patch == nil {
		t.Fatal("expected a PATCH")
	}
	for _, want := range []string{`"target":"#p:1"`, `"target":"#p:2"`, `"action":"replace"`} {
		if !strings.Contains(patch.body, want) {
			t.Errorf("patch missing %q: %s", want, patch.body)
		}
	}
}

func TestReplaceBody_NonTextNeedsConfirm(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		if strings.HasSuffix(rr.path, "/content") {
			return 200, `<body><p id="p:1">a</p><img id="i:1" src="https://x/y.png" alt="pic"/></body>`
		}
		return 200, `{"id":"pg1","title":"T","parentSection":{"id":"sec1"}}`
	}}
	svc := newTestService(&fakeTokens{}, fg)

	_, err := svc.ReplaceBody(context.Background(), 1, "pg1", "new text", false)
	if !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("err = %v, want ErrConfirmRequired", err)
	}
}

func TestReplaceBody_StrategyB_DeleteRecreate(t *testing.T) {
	// Non-text content + confirm → delete then create (new page id).
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		switch {
		case strings.HasSuffix(rr.path, "/content") && rr.method == "GET":
			return 200, `<body><p id="p:1">a</p><img id="i:1" src="https://x/y.png" alt="pic"/></body>`
		case rr.method == "GET":
			return 200, `{"id":"pg1","title":"T","parentSection":{"id":"sec1"}}`
		case rr.method == "DELETE":
			return 204, ""
		case rr.method == "POST":
			return 201, `{"id":"pg2","title":"T","parentSection":{"id":"sec1"}}`
		}
		return 500, "unexpected"
	}}
	svc := newTestService(&fakeTokens{}, fg)

	page, err := svc.ReplaceBody(context.Background(), 1, "pg1", "new text", true)
	if err != nil {
		t.Fatalf("ReplaceBody: %v", err)
	}
	if page.ID != "pg2" {
		t.Errorf("strategy B should yield new page id, got %q", page.ID)
	}
	var sawDelete, sawPost bool
	for _, rr := range fg.reqs {
		switch rr.method {
		case "DELETE":
			sawDelete = true
		case "POST":
			sawPost = true
		}
	}
	if !sawDelete || !sawPost {
		t.Errorf("expected delete+create; reqs=%+v", fg.reqs)
	}
}

func TestCacheInvalidation_AppendBustsPageCache(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		if strings.HasSuffix(rr.path, "/content") && rr.method == "GET" {
			return 200, `<body><p id="p:1">a</p></body>`
		}
		if rr.method == "GET" {
			return 200, `{"id":"pg1","title":"T","parentSection":{"id":"sec1"}}`
		}
		return 204, ""
	}}
	svc := newTestService(&fakeTokens{}, fg)

	if _, err := svc.GetPage(context.Background(), 1, "pg1"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if _, ok := svc.pageCache.Peek(pageKey{1, "pg1"}); !ok {
		t.Fatal("expected page cached after GetPage")
	}
	if err := svc.AppendBlock(context.Background(), 1, "pg1", "more"); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	if _, ok := svc.pageCache.Peek(pageKey{1, "pg1"}); ok {
		t.Fatal("expected page cache busted after AppendBlock")
	}
}

func TestTokenError_Propagates(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) { return 200, "{}" }}
	svc := newTestService(&fakeTokens{err: usertoken.ErrNoLink}, fg)

	_, err := svc.ListNotebooks(context.Background(), 1)
	if !errors.Is(err, usertoken.ErrNoLink) {
		t.Fatalf("err = %v, want ErrNoLink", err)
	}
	if len(fg.reqs) != 0 {
		t.Errorf("no Graph request should be made when token fails")
	}
}

func TestGraphError_Status(t *testing.T) {
	fg := &fakeGraph{handler: func(rr recordedReq) (int, string) {
		return 404, `{"error":{"code":"ResourceNotFound","message":"nope"}}`
	}}
	svc := newTestService(&fakeTokens{}, fg)

	_, err := svc.ListNotebooks(context.Background(), 1)
	var ge *GraphError
	if !errors.As(err, &ge) || ge.StatusCode != 404 || ge.Code != "ResourceNotFound" {
		t.Fatalf("err = %v, want GraphError 404", err)
	}
}
