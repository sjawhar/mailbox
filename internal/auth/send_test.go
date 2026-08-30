package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func sendTestSource(send *CredentialSource) (*Source, *Config, *AccountConfig) {
	acct := &AccountConfig{
		Name: "work",
		Read: &CredentialSource{Class: ClassRead, Kind: SourceEnv, EnvVar: "TEST_READ"},
		Send: send,
	}
	cfg := &Config{Path: "/test/config.toml", Accounts: []*AccountConfig{acct}, CredentialTimeout: defaultCredentialTimeout}
	return NewSource(cfg, acct), cfg, acct
}

func TestSendTokenNeverConsultsMailboxToken(t *testing.T) {
	t.Setenv("MAILBOX_TOKEN", "decoy-token")
	source, cfg, acct := sendTestSource(nil)

	_, err := source.SendToken(context.Background(), BatchAcquirer(cfg, acct, ClassSend))
	var needs *NeedsCredentialError
	if !errors.As(err, &needs) || needs.Class != ClassSend || needs.Reason != ReasonNoSource {
		t.Fatalf("SendToken error = %v, want no-source send credential refusal", err)
	}
	if _, err := source.SendCredentials().AccessToken(context.Background()); !errors.Is(err, ErrExpiredSendToken) {
		t.Fatalf("SendCredentials.AccessToken error = %v, want ErrExpiredSendToken", err)
	}
}

func TestSendTokenIgnoresMailboxTokenWhenSourceConfigured(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "send-helper", `printf '%s\n' 'send.helper.token-value-1234567890'`)
	t.Setenv("MAILBOX_TOKEN", "decoy-token")
	source, cfg, acct := sendTestSource(&CredentialSource{
		Class:     ClassSend,
		Kind:      SourceCmd,
		Argv:      []string{"send-helper"},
		Argv0:     filepath.Join(dir, "send-helper"),
		ConfigKey: "accounts.work.send_credential_cmd",
	})

	token, err := source.SendToken(context.Background(), ExecAcquirer{Cfg: cfg})
	if err != nil || token.AccessToken != "send.helper.token-value-1234567890" || token.Route != RouteCmd || source.SendRoute() != RouteCmd {
		t.Fatalf("SendToken = %+v, %v; route = %q", token, err, source.SendRoute())
	}
	if acct.Send == nil {
		t.Fatal("configured send source was lost")
	}
}

func TestSendAcquisitionLeavesCacheDirByteIdentical(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, "existing"), []byte("keep this byte-identical"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeStub(t, dir, "send-helper", `printf '%s\n' 'send.cache.token-value-1234567890'`)
	t.Setenv("MAILBOX_CACHE_DIR", cache)
	source, cfg, _ := sendTestSource(&CredentialSource{
		Class:     ClassSend,
		Kind:      SourceCmd,
		Argv:      []string{"send-helper"},
		Argv0:     filepath.Join(dir, "send-helper"),
		ConfigKey: "accounts.work.send_credential_cmd",
	})
	before := cacheDigest(t, cache)

	if _, err := source.SendToken(context.Background(), ExecAcquirer{Cfg: cfg}); err != nil {
		t.Fatal(err)
	}
	if after := cacheDigest(t, cache); !reflect.DeepEqual(after, before) {
		t.Fatalf("send acquisition changed cache directory: before=%v after=%v", before, after)
	}
}

func cacheDigest(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	digest := make(map[string][32]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("cache snapshot does not support nested directory %q", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		digest[entry.Name()] = sha256.Sum256(data)
	}
	return digest
}

func TestBatchAcquirerRefusesInteractiveSendSource(t *testing.T) {
	dir := t.TempDir()
	spawn := filepath.Join(dir, "spawned")
	writeStub(t, dir, "interactive-send-helper", `printf spawned > "$SEND_SPAWN_FILE"; printf '%s\n' 'send.interactive.token-value-1234567890'`)
	t.Setenv("SEND_SPAWN_FILE", spawn)
	_, cfg, acct := sendTestSource(&CredentialSource{
		Class:       ClassSend,
		Kind:        SourceCmd,
		Argv:        []string{"interactive-send-helper"},
		Argv0:       filepath.Join(dir, "interactive-send-helper"),
		Interactive: true,
		ConfigKey:   "accounts.work.send_credential_cmd",
	})

	_, err := BatchAcquirer(cfg, acct, ClassSend).Acquire(context.Background(), acct, ClassSend)
	var needs *NeedsCredentialError
	if !errors.As(err, &needs) || needs.Class != ClassSend || needs.Reason != ReasonInteractive {
		t.Fatalf("BatchAcquirer send error = %v, want interactive refusal", err)
	}
	if _, err := os.Stat(spawn); !os.IsNotExist(err) {
		t.Fatalf("interactive send helper spawned: %v", err)
	}
}

func TestSendTokenSingleFlightAndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "send-helper", `sleep 0.1; printf '%s\n' 'send.flight.token-value-1234567890'; printf '%s\n' 'send helper note' >&2`)
	source, cfg, _ := sendTestSource(&CredentialSource{
		Class:     ClassSend,
		Kind:      SourceCmd,
		Argv:      []string{"send-helper"},
		Argv0:     filepath.Join(dir, "send-helper"),
		ConfigKey: "accounts.work.send_credential_cmd",
	})
	acquirer := ExecAcquirer{Cfg: cfg}

	var group sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			token, err := source.SendToken(context.Background(), acquirer)
			if err != nil || token.AccessToken != "send.flight.token-value-1234567890" {
				errs <- errors.New("send credential was not shared")
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if diagnostic := source.TakeDiagnostic(ClassSend); diagnostic != "send helper note" {
		t.Fatalf("send diagnostic = %q, want helper completion note", diagnostic)
	}
	if diagnostic := source.TakeDiagnostic(ClassSend); diagnostic != "" {
		t.Fatalf("second diagnostic drain = %q, want empty", diagnostic)
	}
}
