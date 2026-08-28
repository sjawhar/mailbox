package render

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentDestinationRejectsEscapingFilename(t *testing.T) {
	_, _, err := AttachmentDestination(t.TempDir(), "../escape.pdf")
	if err == nil {
		t.Fatal("AttachmentDestination() accepted an escaping filename")
	}
}

func TestAttachmentDestinationUsesDirectoryWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	path, overwrite, err := AttachmentDestination(directory, "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "report.pdf") || overwrite {
		t.Fatalf("AttachmentDestination() = (%q, %t), want (%q, false)", path, overwrite, filepath.Join(directory, "report.pdf"))
	}
	if err := WriteAttachment(path, []byte("first"), overwrite); err != nil {
		t.Fatal(err)
	}
	if err := WriteAttachment(path, []byte("second"), overwrite); !errors.Is(err, os.ErrExist) {
		t.Fatalf("WriteAttachment() second write error = %v, want existing-file error", err)
	}
}

func TestAttachmentDestinationExplicitFileAllowsOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chosen.pdf")
	got, overwrite, err := AttachmentDestination(path, "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != path || !overwrite {
		t.Fatalf("AttachmentDestination() = (%q, %t), want (%q, true)", got, overwrite, path)
	}
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAttachment(path, []byte("new"), overwrite); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new" {
		t.Fatalf("WriteAttachment() contents = %q, want new", contents)
	}
}
