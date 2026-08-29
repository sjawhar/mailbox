// Command mailbox is a Gmail triage CLI and TUI.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/cli"
	"github.com/sjawhar/mailbox/internal/tui"
	"golang.org/x/term"
)

var version = "dev" // set via -ldflags "-X main.version=…"

var runTUI = tui.Run

func main() {
	if code := dispatch(os.Args[1:], os.Stdout, os.Stderr, term.IsTerminal(int(os.Stdout.Fd()))); code != 0 {
		os.Exit(code)
	}
}

func dispatch(args []string, stdout, stderr io.Writer, terminal bool) int {
	if versionRequested(args) {
		fmt.Fprintf(stdout, "mailbox %s\n", version)
		return 0
	}
	flags, rest, err := cli.ParseTopLevel(args)
	if err != nil {
		fmt.Fprintf(stderr, "mailbox: %v\n", err)
		cli.PrintHelp(stderr)
		return 2
	}
	if flags.Help {
		cli.PrintHelp(stdout)
		return 0
	}
	if len(rest) != 0 {
		return cli.Run(args, stdout, stderr)
	}
	if !terminal {
		fmt.Fprintln(stderr, "mailbox: no subcommand and stdout is not a terminal — agents use subcommands (try 'mailbox inbox --json')")
		return 2
	}
	cfg, err := auth.LoadConfig()
	if err != nil {
		fmt.Fprintln(stderr, "mailbox:", err)
		return 1
	}
	account, err := cfg.ResolveAccount(flags.Account)
	if err != nil {
		fmt.Fprintln(stderr, "mailbox:", err)
		return 1
	}
	if err := runTUI(cfg, account); err != nil {
		fmt.Fprintln(stderr, "mailbox:", err)
		return 1
	}
	return 0
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

// bare reports whether the shared top-level grammar contains no subcommand.
func bare(args []string) bool {
	flags, rest, err := cli.ParseTopLevel(args)
	return err == nil && !flags.Help && len(rest) == 0
}
