package main

import (
	"bytes"
	"github.com/sjawhar/mailbox/internal/auth"
	"os"
	"strings"
	"testing"
)

func TestBare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no arguments", want: true},
		{name: "global flags", args: []string{"--json", "--account=personal"}, want: true},
		{name: "account value", args: []string{"--account", "personal"}, want: true},
		{name: "short account value", args: []string{"-account", "work"}, want: true},
		{name: "inbox command", args: []string{"inbox"}, want: false},
		{name: "command after flag", args: []string{"--json", "inbox"}, want: false},
		{name: "end of flags makes option positional", args: []string{"--", "--json"}, want: false},
		{name: "command after end of flags", args: []string{"--json", "--", "inbox"}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := bare(test.args); got != test.want {
				t.Errorf("bare(%q) = %t, want %t", test.args, got, test.want)
			}
		})
	}
}

func TestVersionRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no version flag", args: []string{"search", "is:unread"}, want: false},
		{name: "long top-level flag", args: []string{"--version"}, want: true},
		{name: "short top-level flag", args: []string{"-version"}, want: true},
		{name: "after global account flag", args: []string{"--account", "personal", "--version"}, want: true},
		{name: "before command", args: []string{"--version", "search"}, want: true},
		{name: "after command", args: []string{"search", "--version"}, want: false},
		{name: "after end of flags", args: []string{"--", "--version"}, want: false},
		{name: "search term after end of flags", args: []string{"search", "--", "--version"}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := versionRequested(test.args); got != test.want {
				t.Errorf("versionRequested(%q) = %t, want %t", test.args, got, test.want)
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
