// wsprobe is a one-shot client that exercises the web login + /ws/bbs path
// without needing a browser. Mirrors what xterm.js does in the page:
//
//   1. GET /login (collect the CSRF cookie + token)
//   2. POST /login with credentials and the token (gets nightms_session cookie)
//   3. WebSocket upgrade /ws/bbs (passing the session cookie + Origin)
//   4. Send {"type":"resize","cols":80,"rows":24}
//   5. Read binary frames for ~2s and assert an expected substring appears
//      in the rendered output.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	var (
		base     = flag.String("base", "http://127.0.0.1:5081", "base URL of the web server")
		user     = flag.String("user", "phase2test", "handle")
		password = flag.String("password", "phase2-test-passphrase-2026", "password")
		expect   = flag.String("expect", "welcome back, phase2test", "substring to find in rendered output")
		capture  = flag.Duration("capture", 2*time.Second, "how long to read WS frames before asserting")
	)
	flag.Parse()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// 1. Pull login page to seed CSRF cookie + token.
	resp, err := client.Get(*base + "/login")
	if err != nil {
		fail("GET /login", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	tok := extractCSRFToken(body)
	if tok == "" {
		fail("CSRF token extraction", fmt.Errorf("no token found in /login HTML"))
	}
	fmt.Printf("[1] /login GET %d, csrf token len=%d\n", resp.StatusCode, len(tok))

	// 2. POST login.
	form := url.Values{}
	form.Set("handle", *user)
	form.Set("password", *password)
	form.Set("gorilla.csrf.Token", tok)
	resp, err = client.PostForm(*base+"/login", form)
	if err != nil {
		fail("POST /login", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 303 {
		fail("POST /login status", fmt.Errorf("got %d, want 303", resp.StatusCode))
	}
	fmt.Printf("[2] /login POST %d (303 after redirect-follow)\n", resp.StatusCode)

	// 3. WebSocket upgrade. The CookieJar feeds nightms_session via the
	//    HTTPClient option.
	wsURL := strings.Replace(*base, "http", "ws", 1) + "/ws/bbs"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:      client,
		Subprotocols:    []string{"nightms.bbs.v1"},
		HTTPHeader:      http.Header{"Origin": []string{*base}},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		fail("ws dial", err)
	}
	defer conn.CloseNow()
	fmt.Printf("[3] /ws/bbs upgraded (status %d, subprotocol %q)\n", resp.StatusCode, conn.Subprotocol())

	// 4. Initial resize.
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`)); err != nil {
		fail("send resize", err)
	}
	fmt.Println("[4] sent initial resize 80x24")

	// 5. Drain binary frames for the capture window, then assert.
	var buf bytes.Buffer
	deadline := time.Now().Add(*capture)
	for time.Now().Before(deadline) {
		readCtx, cancelRead := context.WithDeadline(ctx, deadline)
		mt, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			break
		}
		if mt == websocket.MessageBinary {
			buf.Write(data)
		}
	}
	rendered := stripAnsi(buf.String())
	fmt.Printf("[5] captured %d bytes of rendered output (post-ANSI strip: %d chars)\n", buf.Len(), len(rendered))

	if !strings.Contains(rendered, *expect) {
		fmt.Fprintf(os.Stderr, "FAIL: expected substring %q not found\n", *expect)
		os.Exit(2)
	}
	fmt.Printf("OK: found %q in rendered output\n", *expect)

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

var csrfRe = regexp.MustCompile(`name="gorilla\.csrf\.Token" value="([^"]+)"`)

func extractCSRFToken(html []byte) string {
	m := csrfRe.FindSubmatch(html)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// stripAnsi mirrors cmd/smoketest's helper — drops cursor positioning,
// SGR, alt-screen escapes so a substring assertion works on rendered text.
func stripAnsi(s string) string {
	var out strings.Builder
	in := []byte(s)
	for i := 0; i < len(in); i++ {
		b := in[i]
		if b != 0x1b {
			if b >= 0x20 || b == '\n' || b == '\t' {
				out.WriteByte(b)
			}
			continue
		}
		if i+1 >= len(in) {
			break
		}
		if in[i+1] == '[' {
			i += 2
			for i < len(in) && !(in[i] >= 0x40 && in[i] <= 0x7E) {
				i++
			}
			continue
		}
		if in[i+1] == ']' {
			i += 2
			for i < len(in) && in[i] != 0x07 && !(in[i] == 0x1b && i+1 < len(in) && in[i+1] == '\\') {
				i++
			}
			if i < len(in) && in[i] == 0x1b {
				i++
			}
			continue
		}
		i++
	}
	return out.String()
}

func fail(stage string, err error) {
	fmt.Fprintf(os.Stderr, "FAIL at %s: %v\n", stage, err)
	os.Exit(1)
}
