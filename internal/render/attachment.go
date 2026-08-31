package render

import (
	"fmt"
	"os"
	"path"
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

// CanonicalFilename reduces an untrusted attachment name to one safe basename:
// backslashes normalize to slashes, path.Base drops directories, C0/DEL runes
// drop (reported via hadControl), leading dots drop, and empty or degenerate
// results become "attachment-<index>" (zero-based). Both the send and download
// paths route every filename through this one function.
func CanonicalFilename(name string, index int) (clean string, hadControl bool) {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	var out strings.Builder
	for _, r := range base {
		if r < 0x20 || r == 0x7f {
			hadControl = true
			continue
		}
		out.WriteRune(r)
	}
	clean = strings.TrimLeft(out.String(), ".")
	if clean == "" {
		clean = fmt.Sprintf("attachment-%d", index)
	}
	return clean, hadControl
}

// SaveAttachment creates basename inside dir through a directory descriptor:
// the directory is opened once, and only the single basename is created
// relative to that descriptor — create-exclusive, never following a symlink
// final component, never overwriting. A pre-existing file (however the
// filesystem folds case) surfaces os.ErrExist.
func SaveAttachment(dir, basename string, contents []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open output directory %q: %w", dir, err)
	}
	defer root.Close()
	file, err := root.OpenFile(basename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
