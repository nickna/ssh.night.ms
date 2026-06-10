// Package httpjson is the one GET-and-decode helper shared by the outbound
// JSON providers (news, finance, weather, geocoding). It owns the repetitive
// request/status/decode plumbing; callers keep their provider-specific error
// prefixes by wrapping the returned error, and can special-case upstream
// statuses (a CoinGecko 429, say) by errors.As-ing into *StatusError.
package httpjson

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// StatusError is returned when the upstream answers with a non-200 status.
// Snippet carries the first bytes of the response body for diagnostics —
// many of these APIs put the useful "why" in the error body.
type StatusError struct {
	Code    int
	Snippet string
}

func (e *StatusError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("status %d", e.Code)
	}
	return fmt.Sprintf("status %d: %s", e.Code, e.Snippet)
}

// snippetMax bounds how much of an error body lands in StatusError.Snippet.
const snippetMax = 512

// Get issues a GET request for url with the supplied headers (nil is fine),
// requires a 200, and JSON-decodes the body into out. A non-200 response
// yields a *StatusError. out may be nil to discard the body.
func Get(ctx context.Context, client *http.Client, url string, out any, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, snippetMax))
		return &StatusError{Code: resp.StatusCode, Snippet: string(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
