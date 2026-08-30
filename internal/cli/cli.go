// Package cli implements mailbox's one-shot command surface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/refs"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/send"
)

type cmdCtx struct {
	accountFlag string
	json        bool
	text        bool
	stdout      io.Writer
	stderr      io.Writer
	stdin       io.Reader
	rawArgs     []string
	cfg         *auth.Config
	filterFlag  string
	acct        *auth.AccountConfig
}

type commandSpec struct {
	name        string
	description string
	usage       string
	help        string
	run         func(*cmdCtx, []string) int
}

const idSemantics = "ids: mailbox ids are THREAD ids everywhere; the one exception is 'send --message', which names a message WITHIN the given thread (message ids appear in 'read' output). All-digit arguments are refs into the last 'inbox'/'search' listing."

const jsonFlagHelp = "machine-readable JSON output (stable)"

const textFlagHelp = "human output (overrides agent/pipe detection)"

const outputFormats = "TOON is the default for agents and pipes. --json is the stable opt-in. --text forces human output."

const sendWorkflow = "Start with the dry run, copy its --message value, then add --send to transmit that exact target."

//go:generate go run github.com/sjawhar/mailbox/cmd/skillgen -out ../../docs/agent-skill/SKILL.md

func commandSpecs() []commandSpec {
	return []commandSpec{
		{
			name:        "inbox",
			description: "list inbox threads",
			usage:       "mailbox inbox [--unread] [--max N] [--filter NAME] [--text|--json]",
			help:        "Lists inbox threads. It takes no positional arguments; --unread restricts results to unread threads, --max sets 1–500 rows (default 25), and --filter restricts rows to a named config filter.",
			run:         runInbox,
		},
		{
			name:        "search",
			description: "search threads",
			usage:       "mailbox search [--max N] [--filter NAME] [--text|--json] <query...>",
			help:        "Searches threads with one or more query terms; --max sets 1–500 rows (default 25) and --filter restricts rows to a named config filter. Gmail query operators pass through verbatim: from: to: cc: bcc: subject: label: is: has: in: filename: after: before: older_than: newer_than: deliveredto: list: (see Gmail search syntax).",
			run:         runSearch,
		},
		{
			name:        "read",
			description: "read a thread",
			usage:       "mailbox read [--full] [--text|--json] <thread>",
			help:        "Reads one thread. Messages print newest first. --full keeps quoted history.",
			run:         runRead,
		},
		{
			name:        "open",
			description: "open thread HTML in a browser",
			usage:       "mailbox open [--text|--json] <thread>",
			help:        "Renders the newest HTML message from one thread and hands it to the system browser.",
			run:         runOpen,
		},
		{
			name:        "archive",
			description: "archive threads",
			usage:       "mailbox archive [--filter NAME] [--text|--json] [<thread>...]",
			help:        "Removes the INBOX label from one or more threads, or every inbox thread matching --filter.",
			run:         func(cc *cmdCtx, args []string) int { return runBulk(cc, "archive", args) },
		},
		{
			name:        "trash",
			description: "move threads to trash",
			usage:       "mailbox trash [--filter NAME] [--text|--json] [<thread>...]",
			help:        "Moves one or more threads to Trash, or every inbox thread matching --filter.",
			run:         func(cc *cmdCtx, args []string) int { return runBulk(cc, "trash", args) },
		},
		{
			name:        "mark",
			description: "mark threads read or unread",
			usage:       "mailbox mark [--filter NAME] [--text|--json] <read|unread> [<thread>...]",
			help:        "Marks one or more threads read or unread, or every inbox thread matching --filter.",
			run:         runMark,
		},
		{
			name:        "label",
			description: "add or remove a label",
			usage:       "mailbox label [--filter NAME] [--text|--json] <add|rm> <label> [<thread>...]",
			help:        "Adds or removes one Gmail label on one or more threads, or every inbox thread matching --filter.",
			run:         runLabel,
		},
		{
			name:        "attachment",
			description: "list or save attachments",
			usage:       "mailbox attachment [-o PATH] [--text|--json] <thread> [attachment]",
			help:        "Lists a thread's attachments, or saves one numbered attachment; -o selects the output file or directory.",
			run:         runAttachment,
		},
		{
			name:        "status",
			description: "show configured account status",
			usage:       "mailbox status [--text|--json]",
			help:        "Reports configured account authentication routes, Gmail profiles, and read-cache state.",
			run:         runStatus,
		},
		{
			name:        "send",
			description: "compose, reply, or forward mail (dry-run by default)",
			usage:       "mailbox send [options]",
			help:        sendCommandHelp(),
			run:         runSend,
		},
	}
}

func sendCommandHelp() string {
	var output strings.Builder
	output.WriteString("Compose:\n")
	output.WriteString("  mailbox send --to a@x [--cc b@y] [--bcc c@z] --subject S --body TEXT      # compose\n")
	output.WriteString("  mailbox send --reply=<thread-id>  --body TEXT [--message=<id>] [--to ...] # reply\n")
	output.WriteString("  mailbox send --forward=<thread-id> --to a@x --body TEXT [--message=<id>]  # forward\n\n")
	output.WriteString("The body comes from exactly one of: --body TEXT, --body - (stdin), or --body-file PATH (- for stdin) — file input suits agent-drafted content.\n\n")
	output.WriteString("A dry-run is the default: resolve the envelope first. " + sendWorkflow + " Reply and forward previews select the newest message unless --message selects one; --send requires --message so it pins the exact message within the named thread.\n\n")
	output.WriteString("Refusal rules:\n")
	for _, rule := range send.RuleDocs() {
		fmt.Fprintf(&output, "  %s (%s): %s\n", rule.Rule, rule.Code, rule.Doc)
	}
	return strings.TrimSuffix(output.String(), "\n")
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
	fmt.Fprintln(output, "usage: mailbox [--account NAME] [--json] [--text] <command> [options]")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "global flags:")
	fmt.Fprintln(output, "  --account NAME   account name from config")
	fmt.Fprintln(output, "  --filter NAME    named filter from config")
	fmt.Fprintf(output, "  --json           %s\n", jsonFlagHelp)
	fmt.Fprintf(output, "  --text           %s\n", textFlagHelp)
	fmt.Fprintln(output, "  --help, -h       show this help")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "commands:")
	for _, command := range commandSpecs() {
		fmt.Fprintf(output, "  %-12s %s\n", command.name, command.description)
	}
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "output formats: "+outputFormats)
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, idSemantics)
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "configuration: $XDG_CONFIG_HOME/mailbox/config.toml (or ~/.config/mailbox/config.toml); MAILBOX_CONFIG overrides")
}

// TopLevelFlags are the shared grammar for one-shot and bare mailbox entry
// points. The main package uses the same parser before deciding whether to
// launch the TUI.
type TopLevelFlags struct {
	Account string
	JSON    bool
	Text    bool
	Filter  string
	Help    bool
}

// ParseTopLevel parses global flags and returns the remaining subcommand
// arguments. The caller owns rendering parse errors.
func ParseTopLevel(args []string) (TopLevelFlags, []string, error) {
	flags := flag.NewFlagSet("mailbox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	account := flags.String("account", "", "account name from config")
	jsonOutput := flags.Bool("json", false, "machine output")
	textOutput := flags.Bool("text", false, "human output")
	filter := flags.String("filter", "", "named filter from config")
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args); err != nil {
		return TopLevelFlags{Account: *account, Filter: *filter, JSON: *jsonOutput, Text: *textOutput, Help: *help || *shortHelp}, nil, err
	}
	return TopLevelFlags{Account: *account, Filter: *filter, JSON: *jsonOutput, Text: *textOutput, Help: *help || *shortHelp}, flags.Args(), nil
}

// Run executes a one-shot command. args excludes the program name.
func Run(args []string, stdout, stderr io.Writer) int {
	global, rest, err := ParseTopLevel(args)
	cc := &cmdCtx{accountFlag: global.Account, filterFlag: global.Filter, json: global.JSON, text: global.Text, stdout: stdout, stderr: stderr, stdin: os.Stdin, rawArgs: args}
	if err != nil {
		return cc.failUsage(err)
	}
	if global.Help {
		PrintHelp(stdout)
		return 0
	}
	if len(rest) == 0 {
		return cc.failUsage(nil)
	}
	if rest[0] == "__mint" {
		return runMint(cc, rest[1:])
	}
	if rest[0] == "help" {
		PrintHelp(stdout)
		return 0
	}
	command, found := commandByName(rest[0])
	if !found {
		return cc.failUsage(fmt.Errorf("unknown command %q", rest[0]))
	}
	return command.run(cc, rest[1:])
}

type commonFlags struct {
	fs         *flag.FlagSet
	account    *string
	filter     *string
	json, text *bool
	help       *bool
}

func (cc *cmdCtx) flags(name string) commonFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	account := fs.String("account", cc.accountFlag, "account name from config")
	jsonOutput := fs.Bool("json", cc.json, "machine output")
	filter := fs.String("filter", cc.filterFlag, "named filter from config")
	textOutput := fs.Bool("text", cc.text, "human output")
	help := false
	fs.BoolVar(&help, "help", false, "show help")
	fs.BoolVar(&help, "h", false, "show help")
	return commonFlags{fs: fs, account: account, filter: filter, json: jsonOutput, text: textOutput, help: &help}
}

func (cc *cmdCtx) parse(cf commonFlags, args []string) (pos []string, next *cmdCtx, done bool, code int) {
	pos, err := parseInterspersed(cf.fs, args)
	if err != nil {
		failed := *cc
		failed.accountFlag = *cf.account
		failed.json = *cf.json
		failed.text = *cf.text
		failed.filterFlag = *cf.filter
		return nil, nil, false, failed.failUsage(err)
	}
	copy := *cc
	copy.accountFlag = *cf.account
	copy.json = *cf.json
	copy.text = *cf.text
	copy.filterFlag = *cf.filter
	next = &copy
	if *cf.filter != "" && !filterCommands[cf.fs.Name()] {
		return nil, nil, false, next.failUsage(fmt.Errorf("--filter is not supported by %s", cf.fs.Name()))
	}
	if *cf.help {
		if command, ok := commandByName(cf.fs.Name()); ok {
			printCommandHelp(next.stdout, command)
		}
		return pos, next, true, 0
	}
	return pos, next, false, 0
}

var filterCommands = map[string]bool{
	"inbox":   true,
	"search":  true,
	"archive": true,
	"trash":   true,
	"mark":    true,
	"label":   true,
}

func printCommandHelp(output io.Writer, spec commandSpec) {
	fmt.Fprintf(output, "usage: %s\n\n%s\n", spec.usage, spec.help)
}

func (cc *cmdCtx) loadConfig() error {
	if cc.cfg != nil {
		return nil
	}
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	cc.cfg = cfg
	return nil
}

func (cc *cmdCtx) start() (string, *auth.Source, *gmail.Client, int) {
	if err := cc.loadConfig(); err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
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
	if err := cc.loadConfig(); err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
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

func (cc *cmdCtx) startSend() (string, *auth.Source, *gmail.Client, int) {
	if err := cc.loadConfig(); err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
	acct, err := cc.cfg.ResolveAccount(cc.accountFlag)
	if err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
	cc.acct = acct
	source := auth.NewSource(cc.cfg, acct)
	if _, err := source.SendToken(context.Background(), auth.BatchAcquirer(cc.cfg, acct, auth.ClassSend)); err != nil {
		return "", nil, nil, cc.sendRuntimeError(acct.Name, source, err)
	}
	client := gmail.NewClient(gmail.ClientConfig{
		Read:    source.ReadCredentials(auth.BatchAcquirer(cc.cfg, acct, auth.ClassRead)),
		Send:    source.SendCredentials(),
		Account: acct.Name,
	})
	return acct.Name, source, client, 0
}

func (cc *cmdCtx) runtimeError(account string, source *auth.Source, err error) int {
	return cc.runtimeErrorForClass(account, source, err, auth.ClassRead)
}

func (cc *cmdCtx) writeRuntimeError(account string, source *auth.Source, err error) int {
	return cc.runtimeErrorForClass(account, source, err, auth.ClassWrite)
}

func (cc *cmdCtx) sendRuntimeError(account string, source *auth.Source, err error) int {
	return cc.runtimeErrorForClass(account, source, err, auth.ClassSend)
}

func (cc *cmdCtx) runtimeErrorForClass(_ string, source *auth.Source, err error, class auth.Class) int {
	if source != nil {
		cc.emitCredentialDiagnostic(source, class)
	}
	var credentialError *auth.NeedsCredentialError
	if errors.As(err, &credentialError) {
		return cc.needsCredential(credentialError)
	}
	fmt.Fprintf(cc.stderr, "mailbox: %s\n", render.SanitizeTerminal(err.Error()))
	if source != nil && gmail.IsInsufficientScope(err) {
		route, scope := source.LastRoute(), "gmail.readonly"
		switch class {
		case auth.ClassWrite:
			route, scope = source.WriteRoute(), "gmail.modify"
		case auth.ClassSend:
			route, scope = source.SendRoute(), "gmail.send"
		}
		var typed *gmail.ErrInsufficientScope
		if errors.As(err, &typed) {
			scope = typed.Scope
			switch scope {
			case "gmail.modify":
				class, route = auth.ClassWrite, source.WriteRoute()
			case "gmail.send":
				class, route = auth.ClassSend, source.SendRoute()
			}
		}
		fmt.Fprintf(cc.stderr, "provision: %s\n", auth.ScopeHint(source.Account(), class, route, scope))
	}
	return 1
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Account   string `json:"account"`
		ConfigKey string `json:"config_key"`
		Config    string `json:"config"`
	} `json:"error"`
}

type usageErrorPayload struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (cc *cmdCtx) needsCredential(err *auth.NeedsCredentialError) int {
	if cc.format() == FormatText {
		fmt.Fprintf(cc.stderr, "mailbox: %s\n", render.SanitizeTerminal(err.Error()))
		return 1
	}
	output := errorEnvelope{}
	output.Error.Code = "needs_" + string(err.Class) + "_credential"
	output.Error.Account = err.Account
	output.Error.ConfigKey = err.ConfigKey
	output.Error.Config = err.ConfigPath
	if writeErr := cc.writeMachine(output); writeErr != nil {
		fmt.Fprintf(cc.stderr, "mailbox: write credential error output: %v\n", writeErr)
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

func (cc *cmdCtx) retrySend(source *auth.Source, action func() error) error {
	err := action()
	if !errors.Is(err, auth.ErrExpiredSendToken) {
		return err
	}
	source.InvalidateSend()
	if _, err = source.SendToken(context.Background(), auth.BatchAcquirer(cc.cfg, cc.acct, auth.ClassSend)); err != nil {
		return err
	}
	return action()
}

func (cc *cmdCtx) failUsage(err error) int {
	if err != nil {
		fmt.Fprintf(cc.stderr, "mailbox: %v\n", err)
	}
	usage(cc.stderr)
	if cc.format() != FormatText {
		payload := usageErrorPayload{}
		payload.Error.Code = "usage"
		payload.Error.Message = "missing or unknown command"
		if err != nil {
			payload.Error.Message = err.Error()
		}
		if writeErr := cc.writeMachine(payload); writeErr != nil {
			fmt.Fprintf(cc.stderr, "mailbox: write usage error output: %v\n", writeErr)
		}
	}
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
