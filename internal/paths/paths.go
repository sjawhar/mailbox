// Package paths resolves mailbox's on-disk locations.
package paths

import (
	"os"
	"path/filepath"
)

// CacheDir returns MAILBOX_CACHE_DIR if set, else <user cache dir>/mailbox.
// It does not create the directory.
func CacheDir() (string, error) {
	if dir := os.Getenv("MAILBOX_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mailbox"), nil
}
