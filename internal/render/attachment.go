package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AttachmentDestination resolves an attachment filename against an optional
// output path. An explicit file destination permits overwrite; a directory
// destination never does.
func AttachmentDestination(output, filename string) (path string, overwrite bool, err error) {
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) {
		return "", false, fmt.Errorf("unsafe attachment filename %q", filename)
	}
	directory := "."
	if output != "" {
		info, statErr := os.Stat(output)
		switch {
		case statErr == nil && info.IsDir():
			directory = output
		case statErr == nil:
			return output, true, nil
		case os.IsNotExist(statErr):
			return output, true, nil
		default:
			return "", false, fmt.Errorf("inspect output %q: %w", output, statErr)
		}
	}
	path = filepath.Join(directory, filename)
	relative, err := filepath.Rel(directory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("unsafe attachment filename %q", filename)
	}
	return path, false, nil
}

// WriteAttachment writes an attachment using private file permissions.
func WriteAttachment(path string, contents []byte, overwrite bool) error {
	if overwrite {
		return os.WriteFile(path, contents, 0o600)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if count, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	} else if count != len(contents) {
		_ = file.Close()
		return fmt.Errorf("short attachment write: %d of %d bytes", count, len(contents))
	}
	return file.Close()
}
