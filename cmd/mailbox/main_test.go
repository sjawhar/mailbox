package main

import "testing"

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
