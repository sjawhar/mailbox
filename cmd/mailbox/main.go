// Command mailbox is a Gmail triage CLI and TUI.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/cli"
	"github.com/sjawhar/mailbox/internal/tui"
	"golang.org/x/term"
)

var version = "dev" // set via -ldflags "-X main.version=…"

func main() {
	args := os.Args[1:]
	if versionRequested(args) {
		fmt.Printf("mailbox %s\n", version)
		return
	}
	if !bare(args) {
		os.Exit(cli.Run(args, os.Stdout, os.Stderr))
	}
	// Bare invocation (only global flags): the TUI path.
	fs := flag.NewFlagSet("mailbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	accountFlag := fs.String("account", "", "account name from config")
	helpFlag := fs.Bool("help", false, "show help")
	shortHelpFlag := fs.Bool("h", false, "show help")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *helpFlag || *shortHelpFlag {
		cli.PrintHelp(os.Stdout)
		return
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "mailbox: no subcommand and stdout is not a terminal — agents use subcommands (try 'mailbox inbox --json')")
		os.Exit(2)
	}
	cfg, err := auth.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailbox:", err)
		os.Exit(1)
	}
	account, err := cfg.ResolveAccount(*accountFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailbox:", err)
		os.Exit(1)
	}
	if err := tui.Run(cfg, account); err != nil {
		fmt.Fprintln(os.Stderr, "mailbox:", err)
		os.Exit(1)
	}
}

// versionRequested reports whether a version flag appears in the top-level
// flag sequence. Subcommands and their arguments, including arguments after
// "--", belong to the CLI dispatcher.
func versionRequested(args []string) bool {
	skipNext := false
	for _, a := range args {
		if a == "--" {
			return false
		}
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--version" || a == "-version" {
			return true
		}
		if a == "--account" || a == "-account" {
			skipNext = true
			continue
		}
		if len(a) == 0 || a[0] != '-' {
			return false
		}
	}
	return false
}

// bare reports whether args contain no subcommand — only flags and their
// values. `--account personal` must count as bare: skip the value token of
// any flag that takes one (`--account`/`-account` without '=').
func bare(args []string) bool {
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--" {
			return false // cli.Run treats every following argument as positional.
		}
		if a == "--account" || a == "-account" {
			skipNext = true
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			continue // --json, --account=personal, etc.
		}
		return false // a positional: subcommand present
	}
	return true
}
