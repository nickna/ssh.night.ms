package art

import (
	"embed"
	"fmt"
	"image/color"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// embeddedLobbyIcons ships the curated glyph set inside the binary so a
// fresh deployment renders the carousel correctly with no asset setup. A
// sysop can override any name by dropping <name>.ans into the configured
// LobbyIconsDir — the filesystem read wins ahead of the embed lookup.
//
//go:embed lobby-icons/*.ans
var embeddedLobbyIcons embed.FS

// LobbyIconProvider hands the carousel a small ANSI-art glyph for each card.
// One <name>.ans per button under Dir, parsed once and cached. Missing or
// malformed files fall back to a built-in '?' placeholder so the carousel
// always has something to draw.
type LobbyIconProvider interface {
	Get(name string) *CellGrid
}

// FileSystemLobbyIcons resolves icons from the filesystem with a per-process
// cache. Safe for concurrent use.
type FileSystemLobbyIcons struct {
	Dir    string
	Logger *slog.Logger

	mu     sync.Mutex
	cache  map[string]*CellGrid
	warned map[string]struct{}
}

// NewFileSystemLobbyIcons returns a provider rooted at dir. Empty dir means
// every Get falls straight through to the placeholder — convenient for tests
// or for a deployment that hasn't shipped icon assets yet.
func NewFileSystemLobbyIcons(dir string, logger *slog.Logger) *FileSystemLobbyIcons {
	return &FileSystemLobbyIcons{
		Dir:    dir,
		Logger: logger,
		cache:  map[string]*CellGrid{},
		warned: map[string]struct{}{},
	}
}

func (p *FileSystemLobbyIcons) Get(name string) *CellGrid {
	key := strings.ToLower(name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if hit, ok := p.cache[key]; ok {
		return hit
	}
	grid := p.load(name)
	if grid == nil {
		grid = lobbyIconPlaceholder()
	}
	p.cache[key] = grid
	return grid
}

func (p *FileSystemLobbyIcons) load(name string) *CellGrid {
	// Sysop override path: a file on disk in p.Dir wins so a deployment can
	// re-skin the lobby without rebuilding. Missing files (the common case)
	// fall through to the embedded built-ins silently.
	if p.Dir != "" {
		path := filepath.Join(p.Dir, name+".ans")
		if data, err := os.ReadFile(path); err == nil {
			if grid := ParseANSI(data); grid != nil && grid.Width > 0 && grid.Height > 0 {
				return grid
			}
			p.warnOnce(name, fmt.Sprintf("lobby icon override %s parsed empty; falling back to embedded", path))
		}
	}
	data, err := embeddedLobbyIcons.ReadFile("lobby-icons/" + name + ".ans")
	if err != nil {
		p.warnOnce(name, fmt.Sprintf("lobby icon %s not found in embed or %s; using placeholder", name, p.Dir))
		return nil
	}
	grid := ParseANSI(data)
	if grid == nil || grid.Width == 0 || grid.Height == 0 {
		p.warnOnce(name, fmt.Sprintf("lobby icon %s embed parsed empty; using placeholder", name))
		return nil
	}
	return grid
}

func (p *FileSystemLobbyIcons) warnOnce(name, message string) {
	if _, seen := p.warned[name]; seen {
		return
	}
	p.warned[name] = struct{}{}
	if p.Logger != nil {
		p.Logger.Info(message)
	}
}

// lobbyIconPlaceholder is the 10×2 cyan '?' frame used when a name has no
// matching file. Width matches the inner content area of an unselected card
// with a one-col gutter on each side.
func lobbyIconPlaceholder() *CellGrid {
	grid := NewCellGrid(10, 2)
	fg := &color.NRGBA{R: 0x55, G: 0xFF, B: 0xFF, A: 0xFF}
	for x := 0; x < grid.Width; x++ {
		grid.Cells[0][x] = Cell{Rune: '░', Fg: fg}
		grid.Cells[1][x] = Cell{Rune: '░', Fg: fg}
	}
	grid.Cells[0][4] = Cell{Rune: '?', Fg: fg, Bold: true}
	grid.Cells[0][5] = Cell{Rune: ' ', Fg: fg}
	return grid
}
