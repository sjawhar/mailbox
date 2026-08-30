package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ScissorsLine separates informational draft metadata from the editable body.
const ScissorsLine = "# ------------------------ >8 ------------------------"

// CreateDraft verifies the mailbox-owned parent directory, then creates a
// private compose directory and draft file within it.
func CreateDraft(parent, envelopeBlock string) (dir, path string, err error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", "", fmt.Errorf("compose: create draft parent: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", "", fmt.Errorf("compose: inspect draft parent: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("compose: unsafe draft parent: symlink")
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("compose: unsafe draft parent: not a directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", "", fmt.Errorf("compose: unsafe draft parent: group- or world-writable (mode %04o)", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", fmt.Errorf("compose: unsafe draft parent: cannot determine ownership")
	}
	if stat.Uid != uint32(os.Getuid()) {
		return "", "", fmt.Errorf("compose: unsafe draft parent: not owned by uid %d", os.Getuid())
	}

	dir, err = os.MkdirTemp(parent, "draft-")
	if err != nil {
		return "", "", fmt.Errorf("compose: create draft directory: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("compose: secure draft directory: %w", err)
	}

	path = filepath.Join(dir, "draft.md")
	content := envelopeBlock + "\n" + ScissorsLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", "", fmt.Errorf("compose: write draft: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", "", fmt.Errorf("compose: secure draft: %w", err)
	}
	success = true
	return dir, path, nil
}

// ParseDraft extracts every byte after the first exact scissors line. It
// accepts LF and CRLF line endings without normalizing the body bytes.
func ParseDraft(content []byte) (body string, ok bool) {
	for start := 0; start < len(content); {
		relativeEnd := bytes.IndexByte(content[start:], '\n')
		if relativeEnd < 0 {
			if bytes.Equal(content[start:], []byte(ScissorsLine)) {
				return "", true
			}
			break
		}

		end := start + relativeEnd
		lineEnd := end
		if lineEnd > start && content[lineEnd-1] == '\r' {
			lineEnd--
		}
		if bytes.Equal(content[start:lineEnd], []byte(ScissorsLine)) {
			return string(content[end+1:]), true
		}
		start = end + 1
	}
	return "", false
}

// RemoveDraft deletes a per-compose directory and its draft file.
func RemoveDraft(dir string) error {
	return os.RemoveAll(dir)
}
