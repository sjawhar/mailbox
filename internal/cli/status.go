package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

func runStatus(cc *cmdCtx, args []string) int {
	fs, accountFlag, jsonOutput := cc.flags("status")
	pos, next, code := cc.parse(fs, accountFlag, jsonOutput, args)
	if code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "status"); err != nil {
		return failUsage(cc.stderr, err)
	}

	accounts, err := next.statusAccounts()
	if err != nil {
		return next.runtimeError("", nil, err)
	}

	pinned := os.Getenv("MAILBOX_TOKEN") != ""
	output := statusOutput{
		Config:   next.cfg.Path,
		Accounts: make([]statusAccount, 0, len(accounts)),
		OK:       true,
	}
	var diagnosticSources []*auth.Source
	for _, acct := range accounts {
		row := statusAccount{
			Name:    acct.Name,
			Default: next.isDefaultAccount(acct),
			Read:    statusSourceOutput(acct.Read),
			Write:   statusSourceOutput(acct.Write),
			Pinned:  pinned,
		}
		source := auth.NewSource(next.cfg, acct)
		row.Cache = cacheOutput(source.CacheState())
		client := gmail.NewClient(gmail.ClientConfig{
			Read:    source.ReadCredentials(auth.BatchAcquirer(next.cfg, acct, auth.ClassRead)),
			Account: acct.Name,
		})

		if _, err := source.Resolve(context.Background(), auth.BatchAcquirer(next.cfg, acct, auth.ClassRead)); err != nil {
			row.Error = err.Error()
			if next.isInteractiveReadError(err) {
				row.Profile.message = fmt.Sprintf("requires interactive unlock (%s)", acct.Read.ConfigKey)
			} else {
				output.OK = false
				next.writeStatusError(acct.Name, err)
			}
			output.Accounts = append(output.Accounts, row)
			continue
		}
		diagnosticSources = append(diagnosticSources, source)
		row.Route = string(source.LastRoute())
		row.Cache = cacheOutput(source.CacheState())

		profile, err := client.GetProfile(context.Background())
		if err != nil {
			row.Error = err.Error()
			row.Profile.message = "unavailable"
			output.OK = false
			next.writeStatusError(acct.Name, err)
			output.Accounts = append(output.Accounts, row)
			continue
		}
		row.Profile.Email = profile.EmailAddress
		output.Accounts = append(output.Accounts, row)
	}

	if next.json {
		if err := writeJSON(next.stdout, output); err != nil {
			return next.runtimeError("", nil, wrapError("write JSON", err))
		}
	} else {
		next.writeStatus(next.stdout, output)
	}
	for _, source := range diagnosticSources {
		next.emitCredentialDiagnostic(source, auth.ClassRead)
	}
	if output.OK {
		return 0
	}
	return 1
}

func (cc *cmdCtx) statusAccounts() ([]*auth.AccountConfig, error) {
	if cc.accountFlag == "" && os.Getenv("MAILBOX_ACCOUNT") == "" {
		return cc.cfg.Accounts, nil
	}
	acct, err := cc.cfg.ResolveAccount(cc.accountFlag)
	if err != nil {
		return nil, err
	}
	return []*auth.AccountConfig{acct}, nil
}

func (cc *cmdCtx) isDefaultAccount(acct *auth.AccountConfig) bool {
	return acct.Name == cc.cfg.DefaultAccount || (cc.cfg.DefaultAccount == "" && len(cc.cfg.Accounts) == 1)
}

func (cc *cmdCtx) isInteractiveReadError(err error) bool {
	var credentialError *auth.NeedsCredentialError
	return errors.As(err, &credentialError) && credentialError.Reason == auth.ReasonInteractive
}

func (cc *cmdCtx) writeStatusError(account string, err error) {
	fmt.Fprintf(cc.stderr, "mailbox: account %s: %s\n", render.SanitizeTerminal(account), render.SanitizeTerminal(err.Error()))
}

type statusOutput struct {
	Config   string          `json:"config"`
	Accounts []statusAccount `json:"accounts"`
	OK       bool            `json:"ok"`
}

type statusAccount struct {
	Name    string        `json:"name"`
	Default bool          `json:"default"`
	Read    statusSource  `json:"read"`
	Write   statusSource  `json:"write"`
	Route   string        `json:"route"`
	Cache   statusCache   `json:"cache"`
	Profile statusProfile `json:"profile"`
	Pinned  bool          `json:"pinned"`
	Error   string        `json:"error"`
}

type statusSource struct {
	Kind        string `json:"kind"`
	Argv0       string `json:"argv0,omitempty"`
	Interactive bool   `json:"interactive"`
	Label       string `json:"label,omitempty"`
}

type statusCache struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Valid  bool   `json:"valid"`
	Expiry string `json:"expiry,omitempty"`
}

type statusProfile struct {
	Email   string `json:"email"`
	message string
}

func statusSourceOutput(source *auth.CredentialSource) statusSource {
	if source == nil {
		return statusSource{Kind: "none"}
	}
	output := statusSource{Kind: string(source.Kind), Interactive: source.Interactive}
	if source.Kind == auth.SourceCmd {
		output.Argv0 = source.Argv0
		output.Label = source.Label
	}
	return output
}

func cacheOutput(cache auth.CacheState) statusCache {
	output := statusCache{Path: cache.Path, Exists: cache.Exists, Valid: cache.Valid}
	if !cache.Expiry.IsZero() {
		output.Expiry = cache.Expiry.UTC().Format(time.RFC3339)
	}
	return output
}

func (cc *cmdCtx) writeStatus(output io.Writer, report statusOutput) {
	configPath := report.Config
	if configPath == "" {
		configPath = "<none>"
	}
	fmt.Fprintf(output, "config: %s\n", render.SanitizeTerminal(configPath))
	for _, account := range report.Accounts {
		name := render.SanitizeTerminal(account.Name)
		if account.Default {
			fmt.Fprintf(output, "account: %s (default)\n", name)
		} else {
			fmt.Fprintf(output, "account: %s\n", name)
		}
		writeStatusSource(output, "read", account.Read)
		writeStatusSource(output, "write", account.Write)
		route := account.Route
		if route == "" {
			route = "unavailable"
		}
		fmt.Fprintf(output, "  route: %s\n", route)
		writeStatusCache(output, account.Cache)
		switch {
		case account.Profile.Email != "":
			fmt.Fprintf(output, "  profile: %s\n", render.SanitizeTerminal(account.Profile.Email))
		case account.Profile.message != "":
			fmt.Fprintf(output, "  profile: %s\n", render.SanitizeTerminal(account.Profile.message))
		default:
			fmt.Fprintln(output, "  profile: unavailable")
		}
		if account.Error != "" && account.Profile.message == "" {
			fmt.Fprintf(output, "  error: %s\n", render.SanitizeTerminal(account.Error))
		}
	}
	if len(report.Accounts) > 0 && report.Accounts[0].Pinned {
		fmt.Fprintln(output, "note: MAILBOX_TOKEN pins one identity for all accounts")
	}
}

func writeStatusSource(output io.Writer, class string, source statusSource) {
	switch source.Kind {
	case "cmd":
		interactive := "non-interactive"
		if source.Interactive {
			interactive = "interactive"
		}
		fmt.Fprintf(output, "  %s: cmd %s (%s)", class, render.SanitizeTerminal(source.Argv0), interactive)
		if source.Label != "" {
			fmt.Fprintf(output, " label %q", render.SanitizeTerminal(source.Label))
		}
		fmt.Fprintln(output)
	case "env":
		fmt.Fprintf(output, "  %s: env\n", class)
	default:
		fmt.Fprintf(output, "  %s: not configured\n", class)
	}
}

func writeStatusCache(output io.Writer, cache statusCache) {
	switch {
	case cache.Valid:
		fmt.Fprintf(output, "  cache: valid until %s\n", cache.Expiry)
	case cache.Exists:
		fmt.Fprintf(output, "  cache: expired %s\n", cache.Expiry)
	default:
		fmt.Fprintln(output, "  cache: absent")
	}
}
