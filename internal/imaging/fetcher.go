package imaging

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for image.Decode
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"io"
	"net/http"
	"strings"
	"time"
)

// Fetcher downloads images by URL and decodes them with a size + content-type
// guard so a hostile poster can't pin a multi-GB blob into chat memory. The
// returned Image is whatever stdlib's image.Decode produces (PNG/JPEG/GIF
// out of the box; WebP would need the x/image/webp blank import).
type Fetcher struct {
	Client   *http.Client
	MaxBytes int64
}

// New builds a fetcher with sensible defaults: 5s timeout, 5 MiB cap.
func New() *Fetcher {
	return &Fetcher{
		Client: &http.Client{
			Timeout: 5 * time.Second,
		},
		MaxBytes: 5 << 20,
	}
}

// ErrTooLarge surfaces when the upstream Content-Length (or actual body) is
// larger than MaxBytes. ErrBadContentType surfaces for non-image responses.
var (
	ErrTooLarge       = errors.New("image: response exceeds size cap")
	ErrBadContentType = errors.New("image: unsupported content type")
)

// Fetch downloads the URL, validates content-type + size, and decodes. The
// returned image is suitable for RenderToANSILines.
func (f *Fetcher) Fetch(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("image: build request: %w", err)
	}
	// User-Agent identifies us so hosts can rate-limit or block politely.
	req.Header.Set("User-Agent", "nightms-chat-image-fetch/1")
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/*;q=0.5")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: http %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	// Strip ";charset=..." or ";boundary=..." suffix if present.
	if i := strings.IndexByte(ct, ';'); i > 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if !strings.HasPrefix(ct, "image/") {
		return nil, ErrBadContentType
	}
	// Hard cap: even if Content-Length lies, the LimitReader stops us at the
	// boundary before the decoder can swallow more.
	if resp.ContentLength > 0 && f.MaxBytes > 0 && resp.ContentLength > f.MaxBytes {
		return nil, ErrTooLarge
	}
	var r io.Reader = resp.Body
	if f.MaxBytes > 0 {
		r = io.LimitReader(resp.Body, f.MaxBytes+1)
	}
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("image: decode: %w", err)
	}
	return img, nil
}
