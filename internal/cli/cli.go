// Package cli implements mailbox's one-shot command surface.
package cli

import (
	"context"
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
}

// Run executes a one-shot command. args excludes the program name.
func Run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("mailbox", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	accountFlag := global.String("account", "", "work|personal")
	jsonFlag := global.Bool("json", false, "machine output")
	if err := global.Parse(args); err != nil {
		return failUsage(stderr, err)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return failUsage(stderr, nil)
	}

	cc := &cmdCtx{
		accountFlag: *accountFlag,
		json:        *jsonFlag,
		stdout:      stdout,
		stderr:      stderr,
		rawArgs:     args,
	}
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
		fmt.Fprintf(stderr, "mailbox: unknown command '%s'\n", rest[0])
		usage(stderr)
		return 2
	}
}

func (cc *cmdCtx) flags(name string) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	account := fs.String("account", cc.accountFlag, "work|personal")
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

func (cc *cmdCtx) start() (auth.Account, *auth.Source, *gmail.Client, int) {
	account, err := auth.ResolveAccount(cc.accountFlag)
	if err != nil {
		return "", nil, nil, cc.runtimeError("", nil, err)
	}
	source := auth.NewSource(account)
	if err := source.EnsureEnv(cc.rawArgs); err != nil {
		return "", nil, nil, cc.runtimeError(account, source, err)
	}
	client := gmail.NewClient(source)
	client.Mutation = source.MutationCredentials()
	client.Account = string(account)
	return account, source, client, 0
}

func (cc *cmdCtx) runtimeError(account auth.Account, source *auth.Source, err error) int {
	fmt.Fprintf(cc.stderr, "mailbox: %v\n", err)
	if source != nil && gmail.IsInsufficientScope(err) {
		fmt.Fprintf(cc.stderr, "provision: %s\n", auth.ProvisioningHint(account, source.LastRoute()))
	}
	return 1
}

func failUsage(stderr io.Writer, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "mailbox: %v\n", err)
	}
	usage(stderr)
	return 2
}

func usage(stderr io.Writer) {
	fmt.Fprintln(stderr, "usage: mailbox [--account work|personal] [--json] <inbox|search|read|open|archive|trash|mark|label|attachment|status> [options]")
}

// parseInterspersed parses flags wherever they appear among positionals and
// returns the positionals in order. `--` ends flag parsing: every later token
// is positional (needed for Gmail's negation syntax: mailbox search -- -label:promos).
// Unknown flags and missing values are errors (usage, exit 2).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			pos = append(pos, args[1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			f := fs.Lookup(name)
			if f == nil {
				return nil, fmt.Errorf("unknown flag -%s", name)
			}
			toks := []string{a}
			bv, isBool := f.Value.(interface{ IsBoolFlag() bool })
			needsValue := (!isBool || !bv.IsBoolFlag()) && !strings.Contains(a, "=")
			if needsValue {
				if len(args) < 2 {
					return nil, fmt.Errorf("flag -%s needs a value", name)
				}
				toks = append(toks, args[1])
				args = args[1:]
			}
			if err := fs.Parse(toks); err != nil {
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
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func resolveThreadRef(ctx context.Context, client *gmail.Client, account auth.Account, ref string) (string, error) {
	id, err := refs.Resolve(account, ref)
	if err != nil {
		return "", err
	}
	if isNumeric(ref) {
		return id, nil
	}
	return client.ResolveThreadID(ctx, id)
}

func resolveThreadRefs(ctx context.Context, client *gmail.Client, account auth.Account, values []string) ([]string, error) {
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
