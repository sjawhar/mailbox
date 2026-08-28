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

	"github.com/sjawhar/mailbox/internal/auth"
)

func TestWrite(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)

	if err := Write(auth.AccountWork, []string{"t1", "t2"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	path := refPath(cacheDir, auth.AccountWork)
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
		t.Errorf("cache account = %q, want %q", got.Account, "work")
	}
	if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil {
		t.Errorf("cache createdAt = %q, want RFC3339 timestamp: %v", got.CreatedAt, err)
	}
	if want := []string{"t1", "t2"}; !reflect.DeepEqual(got.ThreadIDs, want) {
		t.Errorf("cache threadIds = %#v, want %#v", got.ThreadIDs, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("cache mode = %o, want 600", got)
	}
}

func TestResolve(t *testing.T) {
	const missingCache = "no ref cache for account 'work' — run 'mailbox inbox' or 'mailbox search' first"
	const zeroOutOfRange = "ref 0 out of range: last listing had 5 results — re-run 'mailbox inbox' or 'mailbox search'"
	const beyondOutOfRange = "ref 6 out of range: last listing had 5 results — re-run 'mailbox inbox' or 'mailbox search'"

	cases := []struct {
		name        string
		account     auth.Account
		arg         string
		setup       func(t *testing.T)
		want        string
		wantErr     string
		wantPathErr bool
		checkAfter  func(t *testing.T)
	}{
		{
			name:    "listing ref resolves to its thread ID",
			account: auth.AccountWork,
			arg:     "2",
			setup: func(t *testing.T) {
				t.Helper()
				if err := Write(auth.AccountWork, []string{"t1", "t2"}); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			},
			want: "t2",
		},
		{
			name:    "zero is out of range",
			account: auth.AccountWork,
			arg:     "0",
			setup: func(t *testing.T) {
				t.Helper()
				if err := Write(auth.AccountWork, []string{"t1", "t2", "t3", "t4", "t5"}); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			},
			wantErr: zeroOutOfRange,
		},
		{
			name:    "ref beyond listing is out of range",
			account: auth.AccountWork,
			arg:     "6",
			setup: func(t *testing.T) {
				t.Helper()
				if err := Write(auth.AccountWork, []string{"t1", "t2", "t3", "t4", "t5"}); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			},
			wantErr: beyondOutOfRange,
		},
		{
			name:    "numeric ref needs a prior listing",
			account: auth.AccountWork,
			arg:     "1",
			wantErr: missingCache,
		},
		{
			name:    "raw ID bypasses a missing cache",
			account: auth.AccountWork,
			arg:     "thread-raw-id",
			want:    "thread-raw-id",
			checkAfter: func(t *testing.T) {
				t.Helper()
				if _, err := os.Stat(refPath(os.Getenv("MAILBOX_CACHE_DIR"), auth.AccountWork)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("raw ID lookup created or read cache unexpectedly: %v", err)
				}
			},
		},
		{
			name:    "accounts have isolated listings",
			account: auth.AccountPersonal,
			arg:     "1",
			setup: func(t *testing.T) {
				t.Helper()
				if err := Write(auth.AccountWork, []string{"work-thread"}); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			},
			wantErr: "no ref cache for account 'personal' — run 'mailbox inbox' or 'mailbox search' first",
		},
		{
			name:    "corrupted cache names its path",
			account: auth.AccountWork,
			arg:     "1",
			setup: func(t *testing.T) {
				t.Helper()
				path := refPath(os.Getenv("MAILBOX_CACHE_DIR"), auth.AccountWork)
				if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
			},
			wantPathErr: true,
		},
		{
			name:    "mismatched cache account names its path",
			account: auth.AccountWork,
			arg:     "1",
			setup: func(t *testing.T) {
				t.Helper()
				path := refPath(os.Getenv("MAILBOX_CACHE_DIR"), auth.AccountWork)
				contents := []byte(`{"account":"personal","createdAt":"2026-08-27T01:02:03Z","threadIds":["personal-thread"]}`)
				if err := os.WriteFile(path, contents, 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
			},
			wantPathErr: true,
		},
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
					t.Fatalf("Resolve() error = %v, want error naming %q", err, refPath(cacheDir, tc.account))
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
				t.Fatalf("Resolve() = %q, %v; want %q, nil", got, err, tc.want)
			}
			if tc.checkAfter != nil {
				tc.checkAfter(t)
			}
		})
	}
}

func TestResolveAll(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
	if err := Write(auth.AccountWork, []string{"t1", "t2"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	cases := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{
			name: "mixed refs preserve argument order",
			args: []string{"2", "raw-thread-id", "1"},
			want: []string{"t2", "raw-thread-id", "t1"},
		},
		{
			name:    "first bad ref aborts resolution",
			args:    []string{"1", "0", "2"},
			wantErr: "ref 0 out of range: last listing had 2 results — re-run 'mailbox inbox' or 'mailbox search'",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAll(auth.AccountWork, tc.args)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("ResolveAll() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveAll() = %#v, %v; want %#v, nil", got, err, tc.want)
			}
		})
	}
}

func refPath(cacheDir string, account auth.Account) string {
	return filepath.Join(cacheDir, string(account)+".refs.json")
}
