package httpjson

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetDecodesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "test-ua" {
			t.Errorf("User-Agent = %q, want test-ua", got)
		}
		_, _ = w.Write([]byte(`{"name":"nightms"}`))
	}))
	defer srv.Close()

	var out struct {
		Name string `json:"name"`
	}
	err := Get(context.Background(), srv.Client(), srv.URL, &out, map[string]string{"User-Agent": "test-ua"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.Name != "nightms" {
		t.Errorf("Name = %q, want nightms", out.Name)
	}
}

func TestGetNon200ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	err := Get(context.Background(), srv.Client(), srv.URL, nil, nil)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.Code != http.StatusTooManyRequests {
		t.Errorf("Code = %d, want 429", se.Code)
	}
	if se.Snippet != "slow down" {
		t.Errorf("Snippet = %q, want body text", se.Snippet)
	}
}

func TestGetNilOutDiscardsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not even json`))
	}))
	defer srv.Close()

	if err := Get(context.Background(), srv.Client(), srv.URL, nil, nil); err != nil {
		t.Fatalf("Get with nil out: %v", err)
	}
}
