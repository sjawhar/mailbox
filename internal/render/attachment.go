package render

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
)

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

// AttachmentSource provides the Gmail attachment bytes for externally stored
// MIME parts.
type AttachmentSource interface {
	GetAttachment(context.Context, string, string) ([]byte, error)
}

// ResolveAttachmentBytes returns inline MIME data directly, or fetches an
// externally stored attachment through the read-configured Gmail source.
func ResolveAttachmentBytes(ctx context.Context, source AttachmentSource, attachment Attachment) ([]byte, error) {
	if attachment.AttachmentID == "" {
		return attachment.Inline, nil
	}
	return source.GetAttachment(ctx, attachment.MessageID, attachment.AttachmentID)
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
