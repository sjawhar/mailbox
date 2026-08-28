// Package refs stores the numbered thread references from a mailbox listing.
package refs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/paths"
)

type cache struct {
	Account   auth.Account `json:"account"`
	CreatedAt time.Time    `json:"createdAt"`
	ThreadIDs []string     `json:"threadIds"`
}

// Write stores the account's listing order: index i (1-based) maps to threadIDs[i-1].
func Write(account auth.Account, threadIDs []string) error {
	dir, err := paths.CacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create ref cache directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set ref cache directory permissions %q: %w", dir, err)
	}

	contents, err := json.Marshal(cache{
		Account:   account,
		CreatedAt: time.Now().UTC(),
		ThreadIDs: threadIDs,
	})
	if err != nil {
		return fmt.Errorf("encode ref cache: %w", err)
	}

	path := filepath.Join(dir, string(account)+".refs.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return fmt.Errorf("write ref cache %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set ref cache permissions %q: %w", path, err)
	}
	return nil
}

// Resolve maps an all-digit listing reference to its thread ID; other IDs are returned verbatim.
func Resolve(account auth.Account, arg string) (string, error) {
	if !isNumber(arg) {
		return arg, nil
	}

	cached, _, err := read(account)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no ref cache for account '%s' — run 'mailbox inbox' or 'mailbox search' first", account)
		}
		return "", err
	}

	ref, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || ref == 0 || ref > uint64(len(cached.ThreadIDs)) {
		return "", fmt.Errorf("ref %s out of range: last listing had %d results — re-run 'mailbox inbox' or 'mailbox search'", arg, len(cached.ThreadIDs))
	}
	return cached.ThreadIDs[ref-1], nil
}

// ResolveAll resolves each reference in order, stopping at the first error.
func ResolveAll(account auth.Account, args []string) ([]string, error) {
	resolved := make([]string, 0, len(args))
	for _, arg := range args {
		id, err := Resolve(account, arg)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, id)
	}
	return resolved, nil
}

func read(account auth.Account) (cache, string, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return cache{}, "", err
	}
	path := filepath.Join(dir, string(account)+".refs.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return cache{}, path, err
	}

	var cached cache
	if err := json.Unmarshal(contents, &cached); err != nil {
		return cache{}, path, fmt.Errorf("decode ref cache %q: %w", path, err)
	}
	if cached.Account != account {
		return cache{}, path, fmt.Errorf("ref cache %q is for account %q, not %q", path, cached.Account, account)
	}
	return cached, path, nil
}

func isNumber(value string) bool {
	if len(value) == 0 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
