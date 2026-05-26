package carbonyl

import (
	"strings"
	"sync"
	"testing"
)

func TestTokensAcquireRespectsCaps(t *testing.T) {
	tk := newTokens(Limits{Global: 2, PerIP: 1, PerHandle: 1})

	rel1, _, ok := tk.Acquire("1.1.1.1", 100)
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	// Same IP, different handle — blocked by PerIP=1.
	if _, reason, ok := tk.Acquire("1.1.1.1", 200); ok || reason != RejectIP {
		t.Fatalf("same-IP second acquire: ok=%v reason=%s", ok, reason)
	}
	// Same handle, different IP — blocked by PerHandle=1.
	if _, reason, ok := tk.Acquire("2.2.2.2", 100); ok || reason != RejectHandle {
		t.Fatalf("same-handle second acquire: ok=%v reason=%s", ok, reason)
	}
	// Different IP and handle — fills Global=2.
	rel2, _, ok := tk.Acquire("2.2.2.2", 200)
	if !ok {
		t.Fatal("second distinct acquire should succeed")
	}
	// Global cap now full.
	if _, reason, ok := tk.Acquire("3.3.3.3", 300); ok || reason != RejectGlobal {
		t.Fatalf("over-cap acquire: ok=%v reason=%s", ok, reason)
	}

	rel1()
	// After release the IP-1 slot opens up and the global counter drops to 1.
	if _, _, ok := tk.Acquire("1.1.1.1", 100); !ok {
		t.Fatal("re-acquire after release should succeed")
	}

	rel2()
	rel2() // idempotent: second call must be a no-op
	g, _, _ := tk.snapshot()
	if g != 1 {
		t.Fatalf("after rel2 called twice and the single re-acquire, global should be 1, got %d", g)
	}
}

func TestTokensAcquireConcurrent(t *testing.T) {
	tk := newTokens(Limits{Global: 10, PerIP: 10, PerHandle: 10})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, _, ok := tk.Acquire("1.1.1.1", 1)
			if ok {
				rel()
			}
		}()
	}
	wg.Wait()
	g, perIP, perHandle := tk.snapshot()
	if g != 0 {
		t.Errorf("global leaked: %d", g)
	}
	if len(perIP) != 0 {
		t.Errorf("perIP leaked: %v", perIP)
	}
	if len(perHandle) != 0 {
		t.Errorf("perHandle leaked: %v", perHandle)
	}
}

func TestTokensZeroCapsDisable(t *testing.T) {
	tk := newTokens(Limits{}) // all zero -> all gates open
	for i := 0; i < 1000; i++ {
		if _, _, ok := tk.Acquire("9.9.9.9", 9); !ok {
			t.Fatalf("acquire %d failed with all caps zero", i)
		}
	}
	g, _, _ := tk.snapshot()
	if g != 1000 {
		t.Errorf("expected 1000 acquired, got %d", g)
	}
}

func TestValidateURLAccepts(t *testing.T) {
	good := []string{
		"https://example.com",
		"http://example.com/path?q=1",
		"https://news.ycombinator.com",
		"about:blank",
		"data:text/plain,hello",
	}
	for _, u := range good {
		if err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestValidateURLRejects(t *testing.T) {
	bad := []struct {
		url, contains string
	}{
		{"", "empty"},
		{"file:///etc/passwd", "scheme file"},
		{"chrome://settings", "scheme chrome"},
		{"view-source:https://x", "scheme view-source"},
		{"http://localhost/", "loopback"},
		{"http://127.0.0.1/", "private/loopback"},
		{"http://10.0.0.1/", "private/loopback"},
		{"http://192.168.1.1/", "private/loopback"},
		{"http://169.254.1.1/", "private/loopback"},
		{"http://[::1]/", "private/loopback"},
		{"http://[fe80::1]/", "private/loopback"},
		{"about:config", "about:blank"},
		{"http://", "missing host"},
	}
	for _, tc := range bad {
		err := ValidateURL(tc.url)
		if err == nil {
			t.Errorf("ValidateURL(%q) = nil, want error containing %q", tc.url, tc.contains)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("ValidateURL(%q) = %v, want error containing %q", tc.url, err, tc.contains)
		}
	}
}

func TestBuildArgsContainsHardeningFlags(t *testing.T) {
	req := LaunchRequest{URL: "https://example.com"}
	args := buildArgs(req, "/data/carbonyl/42", 120, 40)

	required := []string{
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--user-data-dir=/data/carbonyl/42",
		"--disable-extensions",
	}
	for _, want := range required {
		found := false
		for _, got := range args {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("buildArgs missing %q (got %v)", want, args)
		}
	}
	// URL must be the very last arg so an arg-shaped URL fragment can never
	// be re-interpreted as a flag.
	if args[len(args)-1] != req.URL {
		t.Errorf("URL not last in args: %v", args)
	}
	// host-resolver-rules must block localhost in some form.
	foundResolver := false
	for _, a := range args {
		if strings.HasPrefix(a, "--host-resolver-rules=") && strings.Contains(a, "localhost") {
			foundResolver = true
			break
		}
	}
	if !foundResolver {
		t.Errorf("buildArgs missing --host-resolver-rules with localhost block: %v", args)
	}
}
