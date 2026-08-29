package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintSubcommandUsesEnvWithoutLoadingConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"minted","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MAILBOX_TOKEN_URL", server.URL)
	t.Setenv("CLI_MINT_OAUTH", `{"client_id":"c","client_secret":"s","refresh_token":"r"}`)
	t.Setenv("MAILBOX_CONFIG", t.TempDir()+"/missing.toml")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint", "--env", "CLI_MINT_OAUTH"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	var output struct {
		AccessToken string `json:"access_token"`
		Expiry      string `json:"expiry"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.AccessToken != "minted" || output.Expiry == "" {
		t.Fatalf("stdout = %q, output = %+v, error = %v", stdout.String(), output, err)
	}
}

func TestMintSubcommandRejectsMissingEnv(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "requires --env VAR") {
		t.Fatalf("result = %d, %q, %q", code, stdout.String(), stderr.String())
	}
}

func TestMintEnvFlagValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint", "--env", "1BAD"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid __mint --env value") {
		t.Fatalf("invalid env result = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}
