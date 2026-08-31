package render

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalFilenameHostileTable(t *testing.T) {
	cases := []struct {
		name       string
		index      int
		want       string
		hadControl bool
	}{
		{"report.pdf", 0, "report.pdf", false},
		{"../../.bashrc", 0, "bashrc", false},
		{`..\evil`, 0, "evil", false},
		{`C:\Users\x\evil.exe`, 1, "evil.exe", false},
		{"..", 2, "attachment-2", false},
		{".", 3, "attachment-3", false},
		{"", 4, "attachment-4", false},
		{"...", 5, "attachment-5", false},
		{".hidden", 0, "hidden", false},
		{"a\x00b.txt", 0, "ab.txt", true},
		{"a\rb\nc.txt", 0, "abc.txt", true},
		{"del\x7f.txt", 0, "del.txt", true},
		{"\x1b]0;pwn\x07.txt", 0, "]0;pwn.txt", true},
		{"quote\"per%cent.txt", 0, "quote\"per%cent.txt", false},
		{"résumé.pdf", 0, "résumé.pdf", false},
		// Bidi override survives as bytes (not C0/DEL); display layers sanitize. (Plan note 12)
		{"\u202Efdp.txt", 0, "\u202Efdp.txt", false},
		{"\r\n", 6, "attachment-6", true},
	}
	for _, c := range cases {
		got, hadControl := CanonicalFilename(c.name, c.index)
		if got != c.want || hadControl != c.hadControl {
			t.Fatalf("CanonicalFilename(%q, %d) = (%q, %v), want (%q, %v)", c.name, c.index, got, hadControl, c.want, c.hadControl)
		}
		if strings.ContainsAny(got, "/\\") {
			t.Fatalf("CanonicalFilename(%q) kept a path separator: %q", c.name, got)
		}
	}
}

func TestSaveAttachmentCreateExclusiveIsNoClobber(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAttachment(dir, "report.pdf", []byte("one")); err != nil {
		t.Fatalf("first SaveAttachment: %v", err)
	}
	err := SaveAttachment(dir, "report.pdf", []byte("two"))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second SaveAttachment error = %v, want os.ErrExist", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.pdf"))
	if err != nil || string(data) != "one" {
		t.Fatalf("existing file mutated: %q, %v", data, err)
	}
}

func TestSaveAttachmentCaseFoldNoClobber(t *testing.T) { // [R10]
	dir := t.TempDir()
	// Filesystem-aware: probe whether this volume folds case; skip only when
	// it is case-sensitive (a case-fold collision cannot exist there).
	if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err != nil {
		t.Skip("case-sensitive filesystem: case-fold collision cannot occur here")
	}
	if err := SaveAttachment(dir, "Report.pdf", []byte("original")); err != nil {
		t.Fatal(err)
	}
	err := SaveAttachment(dir, "report.pdf", []byte("evil"))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("case-folded save error = %v, want os.ErrExist (create-exclusive IS the no-clobber check)", err) // [G8]
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "Report.pdf"))
	if readErr != nil || string(data) != "original" {
		t.Fatalf("case-folded original mutated: %q, %v", data, readErr)
	}
}

func TestSaveAttachmentNeverFollowsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "report.pdf")); err != nil {
		t.Fatal(err)
	}
	err := SaveAttachment(dir, "report.pdf", []byte("evil"))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("symlinked final component error = %v, want os.ErrExist", err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "safe" {
		t.Fatalf("symlink target overwritten: %q", data)
	}
}

func TestSaveAttachmentSymlinkedDirIsUsersChosenRootOnceOpened(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "cwd")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := SaveAttachment(link, "report.pdf", []byte("ok")); err != nil {
		t.Fatalf("symlinked chosen directory must be allowed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "report.pdf")); err != nil {
		t.Fatalf("file missing under the resolved root: %v", err)
	}
}

func TestSaveAttachmentParentSwapBetweenOpenAndCreateCannotEscape(t *testing.T) {
	// Spec §3: never authorize by Stat + later joined path. The descriptor
	// pins the directory: replacing the path with a symlink AFTER OpenRoot
	// must not redirect the create.
	parent := t.TempDir()
	dir := filepath.Join(parent, "out")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	elsewhere := t.TempDir()
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("report.pdf", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("descriptor create failed: %v", err)
	}
	file.Close()
	if _, err := os.Stat(filepath.Join(elsewhere, "report.pdf")); err == nil {
		t.Fatal("create escaped through the swapped symlink")
	}
	if _, err := os.Stat(filepath.Join(dir+".moved", "report.pdf")); err != nil {
		t.Fatalf("create did not land in the pinned directory: %v", err)
	}
}
