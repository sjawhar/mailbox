package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/toon/toontest"
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

func TestMintSubcommandPassesThroughBareAccessToken(t *testing.T) {
	const accessToken = "abcdefghijklmnopqrstuvwxyz0123456789"
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MAILBOX_TOKEN", "")

	t.Setenv("CLI_MINT_ACCESS_TOKEN", accessToken)
	t.Setenv("MAILBOX_TOKEN_URL", server.URL)
	start := time.Now()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint", "--env", "CLI_MINT_ACCESS_TOKEN"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	end := time.Now()

	if got := refreshes.Load(); got != 0 {
		t.Fatalf("bare access token made %d refresh requests, want none", got)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode mint output: %v", err)
	}
	if len(output) != 2 {
		t.Fatalf("mint output fields = %v, want access_token and expiry only", output)
	}
	var gotToken, gotExpiry string
	if err := json.Unmarshal(output["access_token"], &gotToken); err != nil {
		t.Fatalf("decode access_token: %v", err)
	}
	if err := json.Unmarshal(output["expiry"], &gotExpiry); err != nil {
		t.Fatalf("decode expiry: %v", err)
	}
	if gotToken != accessToken {
		t.Fatalf("access_token = %q, want bare access token", gotToken)
	}
	expiry, err := time.Parse(time.RFC3339, gotExpiry)
	if err != nil {
		t.Fatalf("expiry = %q, want RFC 3339 time: %v", gotExpiry, err)
	}
	if expiry.Before(start.Add(50*time.Minute)) || expiry.After(end.Add(50*time.Minute)) {
		t.Fatalf("expiry = %s, want now plus 50 minutes", expiry)
	}
}

func TestMintSubcommandRejectsInvalidBareTokenWithoutEcho(t *testing.T) {
	const invalid = "not a valid access token"
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("CLI_MINT_ACCESS_TOKEN", invalid)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint", "--env", "CLI_MINT_ACCESS_TOKEN"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("invalid bare token result = (%d, %q, %q), want (1, empty stdout, stderr)", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), invalid) {
		t.Fatalf("invalid bare token appeared in stderr %q", stderr.String())
	}
}

func TestMintSubcommandRejectsInvalidJSONWithoutEcho(t *testing.T) {
	const invalid = `{"client_id":"client-sentinel","refresh_token":}`
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("CLI_MINT_INVALID_JSON", invalid)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint", "--env", "CLI_MINT_INVALID_JSON"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("invalid JSON result = (%d, %q, %q), want (1, empty stdout, stderr)", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), invalid) {
		t.Fatalf("invalid JSON appeared in stderr %q", stderr.String())
	}
}

func TestMintSubcommandRejectsMissingEnv(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "requires --env VAR") {
		t.Fatalf("result = %d, %q, %q", code, stdout.String(), stderr.String())
	}
	doc, err := toontest.Decode(strings.TrimSuffix(stdout.String(), "\n"))
	if err != nil {
		t.Fatalf("decode TOON usage envelope %q: %v", stdout.String(), err)
	}
	errObj := toonField(t, doc, "error")
	if got := toonString(t, errObj, "code"); got != "usage" {
		t.Fatalf("error.code = %q, want usage", got)
	}
	if got := toonString(t, errObj, "message"); !strings.Contains(got, "requires --env VAR") {
		t.Fatalf("error.message = %q, want missing env refusal", got)
	}
}

func TestMintEnvFlagValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"__mint", "--env", "1BAD"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid __mint --env value") {
		t.Fatalf("invalid env result = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}
