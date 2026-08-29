package refs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWrite(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
	if err := Write("work", []string{"t1", "t2"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	path := refPath(cacheDir, "work")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	var got struct {
		Account   string   `json:"account"`
		CreatedAt string   `json:"createdAt"`
		ThreadIDs []string `json:"threadIds"`
	}
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("cache JSON error = %v", err)
	}
	if got.Account != "work" {
		t.Errorf("cache account = %q, want work", got.Account)
	}
	if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil {
		t.Errorf("cache createdAt = %q: %v", got.CreatedAt, err)
	}
	if want := []string{"t1", "t2"}; !reflect.DeepEqual(got.ThreadIDs, want) {
		t.Errorf("cache threadIds = %#v, want %#v", got.ThreadIDs, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %v, %v", info, err)
	}
}

func TestResolve(t *testing.T) {
	const missingCache = "no ref cache for account 'work' — run 'mailbox inbox' or 'mailbox search' first"
	const zeroOutOfRange = "ref 0 out of range: last listing had 5 results — re-run 'mailbox inbox' or 'mailbox search'"
	const beyondOutOfRange = "ref 6 out of range: last listing had 5 results — re-run 'mailbox inbox' or 'mailbox search'"
	cases := []struct {
		name        string
		account     string
		arg         string
		setup       func(*testing.T)
		want        string
		wantErr     string
		wantPathErr bool
		checkAfter  func(*testing.T)
	}{
		{name: "listing ref resolves", account: "work", arg: "2", setup: func(t *testing.T) {
			if err := Write("work", []string{"t1", "t2"}); err != nil {
				t.Fatal(err)
			}
		}, want: "t2"},
		{name: "zero is out of range", account: "work", arg: "0", setup: func(t *testing.T) {
			if err := Write("work", []string{"t1", "t2", "t3", "t4", "t5"}); err != nil {
				t.Fatal(err)
			}
		}, wantErr: zeroOutOfRange},
		{name: "ref beyond listing", account: "work", arg: "6", setup: func(t *testing.T) {
			if err := Write("work", []string{"t1", "t2", "t3", "t4", "t5"}); err != nil {
				t.Fatal(err)
			}
		}, wantErr: beyondOutOfRange},
		{name: "numeric needs prior listing", account: "work", arg: "1", wantErr: missingCache},
		{name: "raw ID bypasses cache", account: "work", arg: "thread-raw-id", want: "thread-raw-id", checkAfter: func(t *testing.T) {
			if _, err := os.Stat(refPath(os.Getenv("MAILBOX_CACHE_DIR"), "work")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("raw lookup touched cache: %v", err)
			}
		}},
		{name: "accounts are isolated", account: "personal", arg: "1", setup: func(t *testing.T) {
			if err := Write("work", []string{"work-thread"}); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "no ref cache for account 'personal' — run 'mailbox inbox' or 'mailbox search' first"},
		{name: "corrupt cache names path", account: "work", arg: "1", setup: func(t *testing.T) {
			path := refPath(os.Getenv("MAILBOX_CACHE_DIR"), "work")
			if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantPathErr: true},
		{name: "mismatched account names path", account: "work", arg: "1", setup: func(t *testing.T) {
			path := refPath(os.Getenv("MAILBOX_CACHE_DIR"), "work")
			if err := os.WriteFile(path, []byte(`{"account":"personal","createdAt":"2026-08-27T01:02:03Z","threadIds":["personal-thread"]}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantPathErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
			if tc.setup != nil {
				tc.setup(t)
			}
			got, err := Resolve(tc.account, tc.arg)
			if tc.wantPathErr {
				if err == nil || !strings.Contains(err.Error(), refPath(cacheDir, tc.account)) {
					t.Fatalf("Resolve() error = %v", err)
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("Resolve() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve() = %q, %v; want %q", got, err, tc.want)
			}
			if tc.checkAfter != nil {
				tc.checkAfter(t)
			}
		})
	}
}

func TestResolveAll(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	if err := Write("work", []string{"t1", "t2"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args, want []string
		wantErr    string
	}{
		{name: "mixed refs preserve order", args: []string{"2", "raw-thread-id", "1"}, want: []string{"t2", "raw-thread-id", "t1"}},
		{name: "first invalid aborts", args: []string{"1", "0", "2"}, wantErr: "ref 0 out of range: last listing had 2 results — re-run 'mailbox inbox' or 'mailbox search'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAll("work", tc.args)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("ResolveAll() error = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveAll() = %#v, %v", got, err)
			}
		})
	}
}

func refPath(cacheDir, account string) string { return filepath.Join(cacheDir, account+".refs.json") }
