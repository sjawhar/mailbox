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

// Run executes a one-shot command. args excludes the program name.
func Run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("mailbox", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	accountFlag := global.String("account", "", "account name from config")
	jsonFlag := global.Bool("json", false, "machine output")
	if err := global.Parse(args); err != nil {
		return failUsage(stderr, err)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return failUsage(stderr, nil)
	}

	cc := &cmdCtx{accountFlag: *accountFlag, json: *jsonFlag, stdout: stdout, stderr: stderr, rawArgs: args}
	if rest[0] == "__mint" {
		return runMint(cc, rest[1:])
	}
	cfg, err := auth.LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "mailbox: %v\n", err)
		return 1
	}
	cc.cfg = cfg

	switch rest[0] {
	case "inbox":
		return runInbox(cc, rest[1:])
	case "search":
		return runSearch(cc, rest[1:])
	case "read":
		return runRead(cc, rest[1:])
	case "open":
		return runOpen(cc, rest[1:])
	case "archive", "trash":
		return runBulk(cc, rest[0], rest[1:])
	case "mark":
		return runMark(cc, rest[1:])
	case "label":
		return runLabel(cc, rest[1:])
	case "attachment":
		return runAttachment(cc, rest[1:])
	case "status":
		return runStatus(cc, rest[1:])
	default:
		fmt.Fprintf(stderr, "mailbox: unknown command %q\n", rest[0])
		usage(stderr)
		return 2
	}
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
	fmt.Fprintf(cc.stderr, "mailbox: %v\n", err)
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
	fmt.Fprintln(stderr, "usage: mailbox [--account NAME] [--json] <inbox|search|read|open|archive|trash|mark|label|attachment|status> [options]")
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
