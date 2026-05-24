package art

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// LoginBanner is the rendered ANSI art shown on the Lobby + Register
// screens. Either Grid (truecolor .ans) or Plain (monochrome text) holds
// the content; the renderer picks based on which is non-nil/non-empty.
//
// Lazy-loaded once per process: the file at NIGHTMS_LOGIN_ART_PATH parses
// on first request and is cached for the rest of the run. Malformed files
// fall back to DefaultBanner so the lobby never paints empty.
type LoginBanner struct {
	Grid  *CellGrid // populated when the file parsed as truecolor .ans
	Plain string    // populated when the file was monochrome text (or default)
	Lines int       // line count, for layout budgeting above the carousel
}

// DefaultBanner is the in-code fallback shown when NIGHTMS_LOGIN_ART_PATH
// is unset or fails to load. Matches the .NET ArtProvider.DefaultArt verbatim
// so both stacks land on the same monochrome frame when nothing else is
// configured.
const DefaultBanner = ` ╓──────────────────────────────────────────────────╖
 ║                                                  ║
 ║   ssh.night.ms   ▒▓█  a small bbs over ssh  █▓▒  ║
 ║                                                  ║
 ╙──────────────────────────────────────────────────╜`

// LoginBannerProvider resolves the LoginBanner. Concrete impl is
// fileLoginBannerProvider; an in-memory mock is straightforward.
type LoginBannerProvider interface {
	Banner() LoginBanner
}

// FileLoginBannerProvider loads from Path on first Banner() call. Subsequent
// calls return the cached result. Concurrency-safe.
type FileLoginBannerProvider struct {
	Path string

	once   sync.Once
	cached LoginBanner
}

// NewLoginBannerProvider returns a provider that reads from path. Empty
// path means "use DefaultBanner immediately, never touch disk".
func NewLoginBannerProvider(path string) *FileLoginBannerProvider {
	return &FileLoginBannerProvider{Path: path}
}

// Banner returns the cached LoginBanner, loading on first call.
func (p *FileLoginBannerProvider) Banner() LoginBanner {
	p.once.Do(func() {
		p.cached = loadBanner(p.Path)
	})
	return p.cached
}

// loadBanner is the actual file -> LoginBanner conversion with all the
// fall-back paths inlined. Files ending in .ans go through the SGR parser;
// anything else (or empty path / read error) lands on the plain text path
// or the in-code default.
func loadBanner(path string) LoginBanner {
	if path == "" {
		return defaultLoginBanner()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultLoginBanner()
	}
	if strings.HasSuffix(strings.ToLower(path), ".ans") {
		grid := ParseANSI(data)
		if grid != nil && grid.Height > 0 {
			return LoginBanner{Grid: grid, Lines: grid.Height}
		}
		// Fall through to plain text on parse failure.
	}
	plain := strings.TrimRight(string(data), "\n")
	return LoginBanner{Plain: plain, Lines: lineCount(plain)}
}

func defaultLoginBanner() LoginBanner {
	plain := strings.TrimRight(DefaultBanner, "\n")
	return LoginBanner{Plain: plain, Lines: lineCount(plain)}
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// Render produces a string ready for lipgloss.JoinVertical / direct
// emission. Caller supplies the CellGrid renderer (avoids a circular
// import between art and components).
func (b LoginBanner) Render(cellRender func(*CellGrid) string) string {
	if b.Grid != nil && cellRender != nil {
		return cellRender(b.Grid)
	}
	return b.Plain
}

// Sprintf is a tiny convenience used by the smoke tests to dump the banner
// state for assertions; not used in normal rendering.
func (b LoginBanner) Sprintf() string {
	if b.Grid != nil {
		return fmt.Sprintf("LoginBanner{Grid=%dx%d}", b.Grid.Width, b.Grid.Height)
	}
	return fmt.Sprintf("LoginBanner{Plain=%d lines}", b.Lines)
}
