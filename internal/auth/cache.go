package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sjawhar/mailbox/internal/paths"
)

type cachedToken struct {
	AccessToken string    `json:"access_token"`
	Route       Route     `json:"route"`
	Expiry      time.Time `json:"expiry"`
}

func cachePath(account Account) (string, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, string(account)+".token.json"), nil
}

func readCache(account Account) (*cachedToken, error) {
	path, err := cachePath(account)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read token cache %s: %w", path, err)
	}
	var token cachedToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("decode token cache %s: %w", path, err)
	}
	return &token, nil
}

func writeCache(account Account, token cachedToken) error {
	path, err := cachePath(account)
	if err != nil {
		return err
	}
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("encode token cache %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token cache directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set token cache directory mode %s: %w", dir, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write token cache %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set token cache mode %s: %w", path, err)
	}
	return nil
}

func (c *cachedToken) valid(now time.Time) bool {
	return c != nil && c.AccessToken != "" && c.Expiry.Add(-2*time.Minute).After(now)
}
