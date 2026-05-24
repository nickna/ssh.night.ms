package art

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadFile reads a .ans file from disk and parses it into a CellGrid. Files
// are expected to be UTF-8 (or ASCII, which is a subset). Malformed escape
// sequences degrade gracefully — what can be parsed paints, the rest is
// skipped.
func LoadFile(path string) (*CellGrid, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("art: read %q: %w", path, err)
	}
	return ParseANSI(data), nil
}

// GalleryEntry is one piece listed in the gallery directory.
type GalleryEntry struct {
	Path  string
	Title string // filename minus extension + numeric prefix
}

// FileSystemGallery enumerates *.ans files under Dir, ordered by the
// numeric prefix (e.g., "010-welcome.ans" sorts before "020-...").
type FileSystemGallery struct {
	Dir string
}

// List re-enumerates on every call so a sysop dropping a file in mid-session
// shows up at the next refresh.
func (g *FileSystemGallery) List() ([]GalleryEntry, error) {
	entries, err := os.ReadDir(g.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gallery: readdir %q: %w", g.Dir, err)
	}
	out := make([]GalleryEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.EqualFold(filepath.Ext(name), ".ans") {
			continue
		}
		title := strings.TrimSuffix(name, filepath.Ext(name))
		// Strip a leading numeric prefix like "010-" or "010_" or "010 ".
		if i := strings.IndexAny(title, "-_ "); i > 0 {
			prefix := title[:i]
			if isAllDigits(prefix) {
				title = title[i+1:]
			}
		}
		out = append(out, GalleryEntry{
			Path:  filepath.Join(g.Dir, name),
			Title: title,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Filenames already sort numerically thanks to zero-padded prefixes;
		// fall back to a simple byte sort.
		return strings.Compare(filepath.Base(out[i].Path), filepath.Base(out[j].Path)) < 0
	})
	return out, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
