package auth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	Fingerprint string    `json:"fingerprint"`
}

func sourceFingerprint(account string, class Class, src *CredentialSource) string {
	if src == nil {
		return ""
	}

	var identity []string
	switch src.Kind {
	case SourceEnv:
		identity = []string{src.EnvVar}
	case SourceCmd:
		identity = append([]string{src.Argv0}, src.Argv[1:]...)
	}
	components := append([]string{account, string(class), string(src.Kind)}, identity...)
	preimageLen := 8 * len(components)
	for _, component := range components {
		preimageLen += len(component)
	}
	preimage := make([]byte, 0, preimageLen)
	for _, component := range components {
		preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(component)))
		preimage = append(preimage, component...)
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:])[:16]
}

func cachePath(account, fingerprint string) (string, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, account+"."+fingerprint+".token.json"), nil
}

func readCache(account, fingerprint string) (*cachedToken, error) {
	path, err := cachePath(account, fingerprint)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		legacyPath := filepath.Join(filepath.Dir(path), account+".token.json")
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove legacy token cache %s: %w", legacyPath, err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read token cache %s: %w", path, err)
	}
	var token cachedToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("decode token cache %s: %w", path, err)
	}
	if token.Fingerprint != fingerprint {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove token cache %s: %w", path, err)
		}
		return nil, nil
	}
	return &token, nil
}

func writeCache(account, fingerprint string, token cachedToken) error {
	path, err := cachePath(account, fingerprint)
	if err != nil {
		return err
	}
	token.Fingerprint = fingerprint
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
