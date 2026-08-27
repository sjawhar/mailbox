package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheDir(t *testing.T) {
	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("MAILBOX_CACHE_DIR", "/tmp/mbx-test")
		got, err := CacheDir()
		if err != nil || got != "/tmp/mbx-test" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("default under user cache dir", func(t *testing.T) {
		t.Setenv("MAILBOX_CACHE_DIR", "")
		os.Unsetenv("MAILBOX_CACHE_DIR")
		base, _ := os.UserCacheDir()
		got, err := CacheDir()
		if err != nil || got != filepath.Join(base, "mailbox") {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}
