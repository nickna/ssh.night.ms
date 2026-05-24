package transport

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureHostKey returns the path to an ed25519 host key, creating the directory
// if needed. wish.WithHostKeyPath will generate the key on first use if the
// file does not exist, so we only need to make sure the parent directory is
// writable and return a stable path.
func EnsureHostKey(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("hostkey: dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("hostkey: mkdir %q: %w", dir, err)
	}
	return filepath.Join(dir, "host_ed25519"), nil
}
