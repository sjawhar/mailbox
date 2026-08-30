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
	flags, rest, err := cli.ParseTopLevel(args)
	if flags.Version {
		fmt.Fprintf(stdout, "mailbox %s\n", version)
		return 0
	}
	if err != nil {
		return cli.Run(args, stdout, stderr)
	}
	if flags.Help {
		cli.PrintHelp(stdout)
		return 0
	}
	if len(rest) != 0 {
		return cli.Run(args, stdout, stderr)
	}
	if !terminal {
		// Machine formats get the structured usage envelope from cli.Run;
		// text mode keeps the specific bare-invocation diagnostic.
		if cli.ResolveFormat(flags.JSON, flags.Text, false, terminal) == cli.FormatText {
			fmt.Fprintln(stderr, "mailbox: no subcommand and stdout is not a terminal — agents use subcommands (try 'mailbox inbox --json')")
			return 2
		}
		return cli.Run(args, stdout, stderr)
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
	if err := runTUI(cfg, account, flags.Filter); err != nil {
		fmt.Fprintln(stderr, "mailbox:", err)
		return 1
	}
	return 0
}

// bare reports whether the shared top-level grammar contains no subcommand.
func bare(args []string) bool {
	flags, rest, err := cli.ParseTopLevel(args)
	return err == nil && !flags.Help && len(rest) == 0
}
