package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDraftFirstExactSentinelWins(t *testing.T) {
	content := "To: a@b (From)\nSubject: s\n\n" + ScissorsLine + "\nbody line\n" + ScissorsLine + "\nstill body\n"
	body, ok := ParseDraft([]byte(content))
	if !ok || body != "body line\n"+ScissorsLine+"\nstill body\n" {
		t.Fatalf("ParseDraft() = %q, %t — later exact sentinel lines are body", body, ok)
	}
}

func TestParseDraftLookalikesAreNotSplitPoints(t *testing.T) {
	lookalike := "# ----------------------- >8 ------------------------"
	content := lookalike + "\n" + ScissorsLine + "\nreal body\n"
	body, ok := ParseDraft([]byte(content))
	if !ok || body != "real body\n" {
		t.Fatalf("ParseDraft() = %q, %t — lookalike must not split", body, ok)
	}
	prefixed := "Subject: " + ScissorsLine + "\n" + ScissorsLine + "\nbody\n"
	if body, ok := ParseDraft([]byte(prefixed)); !ok || body != "body\n" {
		t.Fatalf("sentinel-shaped SUFFIX of a labeled line must not split: %q %t", body, ok)
	}
}

func TestParseDraftMissingSentinelRefuses(t *testing.T) {
	if _, ok := ParseDraft([]byte("no scissors here\n")); ok {
		t.Fatal("missing sentinel must refuse")
	}
}

func TestParseDraftPreservesCRLFBodyBytes(t *testing.T) {
	content := "To: a@b\r\n\r\n" + ScissorsLine + "\r\nline one\r\nline two\n"
	body, ok := ParseDraft([]byte(content))
	if !ok || body != "line one\r\nline two\n" {
		t.Fatalf("ParseDraft() = %q, %t; want CRLF body unchanged", body, ok)
	}
}

func TestCreateDraftCustody(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "compose")
	dir, path, err := CreateDraft(parent, "To: a@b\nSubject: s\n")
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(parent); info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o, want 0700", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("compose dir mode = %o, want 0700", info.Mode().Perm())
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("draft mode = %o, want 0600", info.Mode().Perm())
	}
	content, _ := os.ReadFile(path)
	if body, ok := ParseDraft(content); !ok || body != "" {
		t.Fatalf("fresh draft body = %q, %t, want empty below sentinel", body, ok)
	}
	if err := RemoveDraft(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("RemoveDraft must delete the per-compose directory")
	}
}

func TestCreateDraftRefusesUnsafeParent(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateDraft(link, ""); err == nil {
		t.Fatal("symlinked parent must be refused")
	}
	loose := filepath.Join(base, "loose")
	if err := os.MkdirAll(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(loose); err != nil || info.Mode().Perm() != 0o777 {
		t.Fatalf("fixture mode = %v, %v — want 0777 before the custody check", info.Mode().Perm(), err)
	}
	if _, _, err := CreateDraft(loose, ""); err == nil {
		t.Fatal("group/world-writable parent must be refused")
	}
}

func TestCreateDraftRefusesNonDirectoryParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateDraft(parent, ""); err == nil {
		t.Fatal("non-directory parent must be refused")
	}
}
