package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func mintEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"MAILBOX_TOKEN", "GWS_WORK_MODIFY_OAUTH", "GWS_PERSONAL_MODIFY_OAUTH", "GWS_ACCOUNT"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func TestMintSubcommandPrintsStrictContract(t *testing.T) {
	mintEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"minted","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MAILBOX_TOKEN_URL", server.URL)
	t.Setenv("GWS_WORK_MODIFY_OAUTH", `{"client_id":"c","client_secret":"s","refresh_token":"r"}`)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint", "--account", "work"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	decoder.DisallowUnknownFields()
	var output struct {
		AccessToken string `json:"access_token"`
		Expiry      string `json:"expiry"`
	}
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
	if output.AccessToken != "minted" || output.Expiry == "" {
		t.Fatalf("output = %+v", output)
	}
}

func TestMintSubcommandAbsentKeyIsLoudAndSilentOnStdout(t *testing.T) {
	mintEnv(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint", "--account", "personal"}, &stdout, &stderr); code == 0 {
		t.Fatal("exit = 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "GWS_PERSONAL_MODIFY_OAUTH") {
		t.Fatalf("stderr = %q, want the key named", stderr.String())
	}
}

func TestMintSubcommandRejectsMailboxToken(t *testing.T) {
	mintEnv(t)
	t.Setenv("GWS_WORK_MODIFY_OAUTH", `{"client_id":"c","client_secret":"s","refresh_token":"r"}`)
	t.Setenv("MAILBOX_TOKEN", "pinned")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint"}, &stdout, &stderr); code == 0 {
		t.Fatal("exit = 0, want non-zero (F11)")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
