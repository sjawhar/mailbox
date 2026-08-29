package auth

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testSource() *CredentialSource {
	return &CredentialSource{
		Class: ClassRead,
		Kind:  SourceCmd,
		Argv:  []string{"my-token-helper", "--scopes", "x"},
		Argv0: "/abs/my-token-helper",
	}
}

func TestSourceFingerprintDistinguishesIdentity(t *testing.T) {
	base := sourceFingerprint("work", ClassRead, testSource())
	if len(base) != 16 {
		t.Fatalf("fingerprint length = %d, want 16 hex chars", len(base))
	}
	if _, err := hex.DecodeString(base); err != nil {
		t.Fatalf("fingerprint %q is not hexadecimal: %v", base, err)
	}
	variants := []struct {
		name string
		fp   string
	}{
		{"account", sourceFingerprint("Work", ClassRead, testSource())},
		{"class", sourceFingerprint("work", ClassWrite, testSource())},
		{"argv tail", sourceFingerprint("work", ClassRead, &CredentialSource{Kind: SourceCmd, Argv: []string{"my-token-helper", "--scopes", "y"}, Argv0: "/abs/my-token-helper"})},
		{"argv0", sourceFingerprint("work", ClassRead, &CredentialSource{Kind: SourceCmd, Argv: []string{"my-token-helper", "--scopes", "x"}, Argv0: "/other/my-token-helper"})},
	}
	for _, variant := range variants {
		if variant.fp == base {
			t.Errorf("fingerprint ignores %s", variant.name)
		}
	}
}
func TestSourceFingerprintIncludesSourceKind(t *testing.T) {
	env := sourceFingerprint("work", ClassRead, &CredentialSource{Kind: SourceEnv, EnvVar: "identity"})
	cmd := sourceFingerprint("work", ClassRead, &CredentialSource{
		Kind:  SourceCmd,
		Argv:  []string{"identity"},
		Argv0: "identity",
	})
	if env == cmd {
		t.Fatal("fingerprint ignores source kind")
	}
}

func TestSourceFingerprintFramesNULCommandArguments(t *testing.T) {
	first := sourceFingerprint("work", ClassRead, &CredentialSource{
		Kind:  SourceCmd,
		Argv:  []string{"helper", "a", "b"},
		Argv0: "/abs/helper",
	})
	second := sourceFingerprint("work", ClassRead, &CredentialSource{
		Kind:  SourceCmd,
		Argv:  []string{"helper", "a\x00b"},
		Argv0: "/abs/helper",
	})
	if first == second {
		t.Fatal("fingerprint collides for distinct NUL-containing command arguments")
	}
}

func TestSourceFingerprintUsesDeclaredEnvName(t *testing.T) {
	first := sourceFingerprint("work", ClassRead, &CredentialSource{Kind: SourceEnv, EnvVar: "READ_TOKEN"})
	second := sourceFingerprint("work", ClassRead, &CredentialSource{Kind: SourceEnv, EnvVar: "OTHER_READ_TOKEN"})
	if first == second {
		t.Fatal("fingerprint ignores declared environment variable name")
	}
}

func TestSourceFingerprintNilSourceCannotCache(t *testing.T) {
	if got := sourceFingerprint("work", ClassRead, nil); got != "" {
		t.Fatalf("sourceFingerprint(nil) = %q, want empty", got)
	}
}

func TestCacheRoundTripAndByteIdenticalWrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", dir)
	fp := sourceFingerprint("work", ClassRead, testSource())
	tok := cachedToken{AccessToken: "tok", Route: RouteCmd, Expiry: time.Now().Add(time.Hour).UTC().Truncate(time.Second), Fingerprint: fp}
	path, err := cachePath("work", fp)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(path), "work."+fp+".token.json"; got != want {
		t.Fatalf("cache file = %q, want %q", got, want)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCache("work", fp, tok); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCache("work", fp, tok); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("cache write is not byte-identical across writes")
	}
	got, err := readCache("work", fp)
	if err != nil || got == nil || got.AccessToken != "tok" || got.Fingerprint != fp {
		t.Fatalf("readCache = %+v, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestCacheFingerprintMismatchRemovesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", dir)
	fp := sourceFingerprint("work", ClassRead, testSource())
	path, err := cachePath("work", fp)
	if err != nil {
		t.Fatal(err)
	}
	stale := `{"access_token":"tok","route":"cmd","expiry":"2099-01-01T00:00:00Z","fingerprint":"deadbeefdeadbeef"}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readCache("work", fp)
	if err != nil || got != nil {
		t.Fatalf("mismatched entry must miss: %+v, %v", got, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("mismatched cache file was not removed")
	}
}

func TestCacheMissingFingerprintRemovesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", dir)
	fp := sourceFingerprint("work", ClassRead, testSource())
	path, err := cachePath("work", fp)
	if err != nil {
		t.Fatal(err)
	}
	stale := `{"access_token":"tok","route":"cmd","expiry":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readCache("work", fp)
	if err != nil || got != nil {
		t.Fatalf("missing fingerprint entry must miss: %+v, %v", got, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("cache file missing fingerprint was not removed")
	}
}

func TestCacheV020ShapedFileSelfInvalidates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", dir)
	legacy := filepath.Join(dir, "work.token.json")
	if err := os.WriteFile(legacy, []byte(`{"access_token":"old","route":"broker","expiry":"2099-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := sourceFingerprint("work", ClassRead, testSource())
	got, err := readCache("work", fp)
	if err != nil || got != nil {
		t.Fatalf("legacy file must never be read as a hit: %+v, %v", got, err)
	}
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatal("v0.2.0-shaped cache file was not removed at cutover")
	}
}
