package art

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// embeddedBoardIcons ships a starter set of per-forum glyphs inside the
// binary. A sysop can drop <slug>.ans into BoardIconsDir to override or add
// icons without rebuilding. Lookups that miss both the filesystem and the
// embed fall back to the "default" icon (also embedded) so every row in the
// forum list renders something even on a fresh deployment.
//
//go:embed board-icons/*.ans
var embeddedBoardIcons embed.FS

// FileSystemBoardIcons resolves per-forum glyphs the same way
// FileSystemLobbyIcons resolves lobby cards. Same interface
// (LobbyIconProvider), different embed FS + override directory. A missing
// slug falls back to the "default" icon rather than the cyan '?' placeholder
// because the forum list always has a sensible "generic forum" visual to
// reach for.
type FileSystemBoardIcons struct {
	Dir    string
	Logger *slog.Logger

	mu       sync.Mutex
	cache    map[string]*CellGrid
	warned   map[string]struct{}
	fallback *CellGrid // memoized "default" lookup
}

// NewFileSystemBoardIcons returns a provider rooted at dir. Empty dir is fine
// — the embedded set covers the default + general forums.
func NewFileSystemBoardIcons(dir string, logger *slog.Logger) *FileSystemBoardIcons {
	return &FileSystemBoardIcons{
		Dir:    dir,
		Logger: logger,
		cache:  map[string]*CellGrid{},
		warned: map[string]struct{}{},
	}
}

func (p *FileSystemBoardIcons) Get(name string) *CellGrid {
	key := strings.ToLower(name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if hit, ok := p.cache[key]; ok {
		return hit
	}
	grid := p.load(key)
	if grid == nil {
		grid = p.defaultLocked()
	}
	p.cache[key] = grid
	return grid
}

func (p *FileSystemBoardIcons) load(name string) *CellGrid {
	if p.Dir != "" {
		path := filepath.Join(p.Dir, name+".ans")
		if data, err := os.ReadFile(path); err == nil {
			if grid := ParseANSI(data); grid != nil && grid.Width > 0 && grid.Height > 0 {
				return grid
			}
			p.warnOnce(name, fmt.Sprintf("board icon override %s parsed empty; falling back to embedded", path))
		}
	}
	data, err := embeddedBoardIcons.ReadFile("board-icons/" + name + ".ans")
	if err != nil {
		return nil
	}
	grid := ParseANSI(data)
	if grid == nil || grid.Width == 0 || grid.Height == 0 {
		p.warnOnce(name, fmt.Sprintf("board icon %s embed parsed empty; using default", name))
		return nil
	}
	return grid
}

// defaultLocked returns the embedded "default" icon, memoized so we don't
// re-parse the bytes for every miss. Caller must hold p.mu.
func (p *FileSystemBoardIcons) defaultLocked() *CellGrid {
	if p.fallback != nil {
		return p.fallback
	}
	data, err := embeddedBoardIcons.ReadFile("board-icons/default.ans")
	if err != nil {
		p.fallback = lobbyIconPlaceholder()
		return p.fallback
	}
	grid := ParseANSI(data)
	if grid == nil || grid.Width == 0 || grid.Height == 0 {
		p.fallback = lobbyIconPlaceholder()
		return p.fallback
	}
	p.fallback = grid
	return p.fallback
}

func (p *FileSystemBoardIcons) warnOnce(name, message string) {
	if _, seen := p.warned[name]; seen {
		return
	}
	p.warned[name] = struct{}{}
	if p.Logger != nil {
		p.Logger.Info(message)
	}
}
