// Package cli implements mailbox's one-shot command surface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/refs"
	"github.com/sjawhar/mailbox/internal/render"
)

type cmdCtx struct {
	accountFlag string
	json        bool
	stdout      io.Writer
	stderr      io.Writer
	rawArgs     []string
	cfg         *auth.Config
	acct        *auth.AccountConfig
}

type commandSpec struct {
	name        string
	description string
	run         func(*cmdCtx, []string) int
}

func commandSpecs() []commandSpec {
	return []commandSpec{
		{name: "inbox", description: "list inbox threads", run: runInbox},
		{name: "search", description: "search threads", run: runSearch},
		{name: "read", description: "read a thread", run: runRead},
		{name: "open", description: "open thread HTML in a browser", run: runOpen},
		{name: "archive", description: "archive threads", run: func(cc *cmdCtx, args []string) int { return runBulk(cc, "archive", args) }},
		{name: "trash", description: "move threads to trash", run: func(cc *cmdCtx, args []string) int { return runBulk(cc, "trash", args) }},
		{name: "mark", description: "mark threads read or unread", run: runMark},
		{name: "label", description: "add or remove a label", run: runLabel},
		{name: "attachment", description: "list or save attachments", run: runAttachment},
		{name: "status", description: "show configured account status", run: runStatus},
	}
}

func commandByName(name string) (commandSpec, bool) {
	for _, command := range commandSpecs() {
		if command.name == name {
			return command, true
		}
	}
	return commandSpec{}, false
}

// PrintHelp writes the public command and configuration summary.
func PrintHelp(output io.Writer) {
	fmt.Fprintln(output, "usage: mailbox [--account NAME] [--json] <command> [options]")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "global flags:")
	fmt.Fprintln(output, "  --account NAME   account name from config")
	fmt.Fprintln(output, "  --json           machine-readable output")
	fmt.Fprintln(output, "  --help, -h       show this help")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "commands:")
	for _, command := range commandSpecs() {
		fmt.Fprintf(output, "  %-12s %s\n", command.name, command.description)
	}
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "configuration: $XDG_CONFIG_HOME/mailbox/config.toml (or ~/.config/mailbox/config.toml); MAILBOX_CONFIG overrides")
}

// TopLevelFlags are the shared grammar for one-shot and bare mailbox entry
// points. The main package uses the same parser before deciding whether to
// launch the TUI.
type TopLevelFlags struct {
	Account string
	JSON    bool
	Help    bool
}

// ParseTopLevel parses global flags and returns the remaining subcommand
// arguments. The caller owns rendering parse errors.
func ParseTopLevel(args []string) (TopLevelFlags, []string, error) {
	flags := flag.NewFlagSet("mailbox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	account := flags.String("account", "", "account name from config")
	jsonOutput := flags.Bool("json", false, "machine output")
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args); err != nil {
		return TopLevelFlags{}, nil, err
	}
	return TopLevelFlags{Account: *account, JSON: *jsonOutput, Help: *help || *shortHelp}, flags.Args(), nil
}

// Run executes a one-shot command. args excludes the program name.
func Run(args []string, stdout, stderr io.Writer) int {
	global, rest, err := ParseTopLevel(args)
	if err != nil {
		return failUsage(stderr, err)
	}
	if global.Help {
		PrintHelp(stdout)
		return 0
	}
	if len(rest) == 0 {
		return failUsage(stderr, nil)
	}

	cc := &cmdCtx{accountFlag: global.Account, json: global.JSON, stdout: stdout, stderr: stderr, rawArgs: args}
	if rest[0] == "__mint" {
		return runMint(cc, rest[1:])
	}
	if rest[0] == "help" {
		PrintHelp(stdout)
		return 0
	}
	command, found := commandByName(rest[0])
	if !found {
		fmt.Fprintf(stderr, "mailbox: unknown command %q\n", rest[0])
		usage(stderr)
		return 2
	}
	cfg, err := auth.LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "mailbox: %s\n", render.SanitizeTerminal(err.Error()))
		return 1
	}
	cc.cfg = cfg
	return command.run(cc, rest[1:])
}

func (cc *cmdCtx) flags(name string) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	account := fs.String("account", cc.accountFlag, "account name from config")
	jsonOutput := fs.Bool("json", cc.json, "machine output")
	return fs, account, jsonOutput
}

func (cc *cmdCtx) parse(fs *flag.FlagSet, account *string, jsonOutput *bool, args []string) ([]string, *cmdCtx, int) {
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return nil, nil, failUsage(cc.stderr, err)
	}
	next := *cc
	next.accountFlag = *account
	next.json = *jsonOutput
	return pos, &next, 0
}

func (cc *cmdCtx) start() (string, *auth.Source, *gmail.Client, int) {
	acct, err := cc.cfg.ResolveAccount(cc.accountFlag)
	if err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
	cc.acct = acct
	source := auth.NewSource(cc.cfg, acct)
	client := gmail.NewClient(gmail.ClientConfig{Read: source.ReadCredentials(auth.BatchAcquirer(cc.cfg, acct, auth.ClassRead)), Account: acct.Name})
	return acct.Name, source, client, 0
}

func (cc *cmdCtx) startWrite() (string, *auth.Source, *gmail.Client, int) {
	acct, err := cc.cfg.ResolveAccount(cc.accountFlag)
	if err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
	cc.acct = acct
	source := auth.NewSource(cc.cfg, acct)
	if _, err := source.WriteToken(context.Background(), auth.BatchAcquirer(cc.cfg, acct, auth.ClassWrite)); err != nil {
		return "", nil, nil, cc.runtimeError(acct.Name, source, err)
	}
	credentials := source.WriteCredentials()
	client := gmail.NewClient(gmail.ClientConfig{Read: credentials, Write: credentials, Account: acct.Name})
	return acct.Name, source, client, 0
}

func (cc *cmdCtx) runtimeError(account string, source *auth.Source, err error) int {
	return cc.runtimeErrorForScope(account, source, err, false)
}

func (cc *cmdCtx) writeRuntimeError(account string, source *auth.Source, err error) int {
	return cc.runtimeErrorForScope(account, source, err, true)
}

func (cc *cmdCtx) runtimeErrorForScope(_ string, source *auth.Source, err error, write bool) int {
	if source != nil {
		class := auth.ClassRead
		if write {
			class = auth.ClassWrite
		}
		cc.emitCredentialDiagnostic(source, class)
	}
	var credentialError *auth.NeedsCredentialError
	if errors.As(err, &credentialError) {
		return cc.needsCredential(credentialError)
	}
	fmt.Fprintf(cc.stderr, "mailbox: %s\n", render.SanitizeTerminal(err.Error()))
	if source != nil && gmail.IsInsufficientScope(err) {
		class, route, scope := auth.ClassRead, source.LastRoute(), "gmail.readonly"
		if write {
			class, route, scope = auth.ClassWrite, source.WriteRoute(), "gmail.modify"
		}
		var typed *gmail.ErrInsufficientScope
		if errors.As(err, &typed) {
			scope = typed.Scope
			if scope == "gmail.modify" {
				class, route = auth.ClassWrite, source.WriteRoute()
			}
		}
		fmt.Fprintf(cc.stderr, "provision: %s\n", auth.ScopeHint(source.Account(), class, route, scope))
	}
	return 1
}

func (cc *cmdCtx) needsCredential(err *auth.NeedsCredentialError) int {
	if !cc.json {
		fmt.Fprintf(cc.stderr, "mailbox: %s\n", render.SanitizeTerminal(err.Error()))
		return 1
	}
	output := struct {
		Error struct {
			Code      string `json:"code"`
			Account   string `json:"account"`
			ConfigKey string `json:"config_key"`
			Config    string `json:"config"`
		} `json:"error"`
	}{}
	output.Error.Code = "needs_" + string(err.Class) + "_credential"
	output.Error.Account = err.Account
	output.Error.ConfigKey = err.ConfigKey
	output.Error.Config = err.ConfigPath
	if writeErr := writeJSON(cc.stdout, output); writeErr != nil {
		fmt.Fprintf(cc.stderr, "mailbox: write credential error JSON: %v\n", writeErr)
	}
	return 1
}

func (cc *cmdCtx) emitCredentialDiagnostic(source *auth.Source, class auth.Class) {
	if source == nil {
		return
	}
	diagnostic := render.SanitizeTerminal(source.TakeDiagnostic(class))
	diagnostic = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(diagnostic))
	if diagnostic != "" {
		fmt.Fprintf(cc.stderr, "mailbox: credential helper: %s\n", diagnostic)
	}
}

func (cc *cmdCtx) retryWrite(source *auth.Source, action func() error) error {
	err := action()
	if !errors.Is(err, auth.ErrExpiredToken) {
		return err
	}
	source.InvalidateWrite()
	if _, err = source.WriteToken(context.Background(), auth.BatchAcquirer(cc.cfg, cc.acct, auth.ClassWrite)); err != nil {
		return err
	}
	return action()
}

func failUsage(stderr io.Writer, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "mailbox: %v\n", err)
	}
	usage(stderr)
	return 2
}

func usage(stderr io.Writer) {
	PrintHelp(stderr)
}

func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			return append(pos, args[1:]...), nil
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			if index := strings.IndexByte(name, '='); index >= 0 {
				name = name[:index]
			}
			field := fs.Lookup(name)
			if field == nil {
				return nil, fmt.Errorf("unknown flag -%s", name)
			}
			tokens := []string{a}
			boolValue, isBool := field.Value.(interface{ IsBoolFlag() bool })
			if (!isBool || !boolValue.IsBoolFlag()) && !strings.Contains(a, "=") {
				if len(args) < 2 {
					return nil, fmt.Errorf("flag -%s needs a value", name)
				}
				tokens = append(tokens, args[1])
				args = args[1:]
			}
			if err := fs.Parse(tokens); err != nil {
				return nil, err
			}
			args = args[1:]
			continue
		}
		pos = append(pos, a)
		args = args[1:]
	}
	return pos, nil
}

func requireArity(pos []string, min, max int, name string) error {
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		if min == max {
			return fmt.Errorf("%s requires %d argument(s)", name, min)
		}
		return fmt.Errorf("%s requires %d to %d argument(s)", name, min, max)
	}
	return nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, runeValue := range value {
		if runeValue < '0' || runeValue > '9' {
			return false
		}
	}
	return true
}

func resolveThreadRef(ctx context.Context, client *gmail.Client, account, ref string) (string, error) {
	id, err := refs.Resolve(account, ref)
	if err != nil {
		return "", err
	}
	if isNumeric(ref) {
		return id, nil
	}
	return client.ResolveThreadID(ctx, id)
}

func resolveThreadRefs(ctx context.Context, client *gmail.Client, account string, values []string) ([]string, error) {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, err := resolveThreadRef(ctx, client, account, value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func wrapError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}
