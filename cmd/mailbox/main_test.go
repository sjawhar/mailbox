package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/cli"
)

func TestVersionFlagSharesTopLevelGrammar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantVersion bool
		wantFilter  string
	}{
		{name: "no version flag", args: []string{"search", "is:unread"}},
		{name: "long top-level flag", args: []string{"--version"}, wantVersion: true},
		{name: "short top-level flag", args: []string{"-version"}, wantVersion: true},
		{name: "after global account flag", args: []string{"--account", "personal", "--version"}, wantVersion: true},
		{name: "after global filter flag", args: []string{"--filter", "github", "--version"}, wantVersion: true, wantFilter: "github"},
		{name: "filter consumes version-like value", args: []string{"--filter", "--version"}, wantFilter: "--version"},
		{name: "before command", args: []string{"--version", "search"}, wantVersion: true},
		{name: "after command", args: []string{"search", "--version"}},
		{name: "after end of flags", args: []string{"--", "--version"}},
		{name: "search term after end of flags", args: []string{"search", "--", "--version"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags, _, err := cli.ParseTopLevel(test.args)
			if err != nil {
				t.Fatalf("ParseTopLevel(%q) = %v", test.args, err)
			}
			if flags.Version != test.wantVersion || flags.Filter != test.wantFilter {
				t.Errorf("ParseTopLevel(%q) = (version=%t, filter=%q), want (version=%t, filter=%q)", test.args, flags.Version, flags.Filter, test.wantVersion, test.wantFilter)
			}
		})
	}
}

func TestTopLevelFlagGrammarDrivesBareAndCLIPaths(t *testing.T) {
	originalTUI := runTUI
	t.Cleanup(func() { runTUI = originalTUI })
	t.Setenv("MAILBOX_CONFIG", "")
	if err := os.Unsetenv("MAILBOX_CONFIG"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "test-token")
	tuiCalls := 0
	var startFilters []string
	runTUI = func(_ *auth.Config, account *auth.AccountConfig, startFilter string) error {
		tuiCalls++
		if account.Name != "default" {
			t.Fatalf("bare account = %q, want default", account.Name)
		}
		startFilters = append(startFilters, startFilter)
		return nil
	}

	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"--json"}, &stdout, &stderr, true); code != 0 || tuiCalls != 1 || startFilters[0] != "" {
		t.Fatalf("bare --json = (%d, calls=%d, filters=%q, stdout=%q, stderr=%q)", code, tuiCalls, startFilters, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := dispatch([]string{"--filter", "github"}, &stdout, &stderr, true); code != 0 || tuiCalls != 2 || startFilters[1] != "github" {
		t.Fatalf("bare --filter = (%d, calls=%d, filters=%q, stdout=%q, stderr=%q)", code, tuiCalls, startFilters, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := dispatch([]string{"--json", "help"}, &stdout, &stderr, false); code != 0 || !strings.Contains(stdout.String(), "inbox") {
		t.Fatalf("--json help = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := dispatch([]string{"--not-a-real-flag"}, &stdout, &stderr, true); code != 2 {
		t.Fatalf("invalid top-level flag = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestNonTerminalMachineUsageErrorsUseUsageEnvelope(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "unknown top-level flag", args: []string{"--json", "--definitely-unknown"}},
		{name: "missing subcommand", args: []string{"--json"}},
		{name: "JSON takes precedence over text", args: []string{"--json", "--text"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			if code := dispatch(test.args, &stdout, &stderr, false); code != 2 {
				t.Fatalf("dispatch(%q) = %d, stdout=%q, stderr=%q", test.args, code, stdout.String(), stderr.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil || payload.Error.Code != "usage" {
				t.Fatalf("machine usage envelope = %q (%v), code=%q", stdout.String(), err, payload.Error.Code)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("stderr must retain human usage guidance, got %q", stderr.String())
			}
		})
	}
}

func TestNonTerminalTextUsageKeepsBareInvocationDiagnostic(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"--text"}, &stdout, &stderr, false); code != 2 {
		t.Fatalf("dispatch(--text) = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("text bare invocation emitted stdout %q, want no usage envelope", stdout.String())
	}
	const want = "mailbox: no subcommand and stdout is not a terminal — agents use subcommands (try 'mailbox inbox --json')\n"
	if got := stderr.String(); got != want {
		t.Fatalf("text bare invocation stderr = %q, want %q", got, want)
	}
}

func TestFilterValueDoesNotRequestVersion(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"--filter", "--version"}, &stdout, &stderr, false); code != 2 {
		t.Fatalf("dispatch(--filter --version) = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); got == "mailbox "+version+"\n" {
		t.Fatalf("--version consumed as --filter value must not print the version: %q", got)
	}
	if !strings.Contains(stdout.String(), "usage") {
		t.Fatalf("--filter --version must produce a machine usage outcome, got %q", stdout.String())
	}
}
