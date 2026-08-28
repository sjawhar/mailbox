package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
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
	account, err := auth.ResolveAccount(next.accountFlag)
	if err != nil {
		return next.runtimeError("", nil, err)
	}
	source := auth.NewSource(account)
	if err := source.EnsureEnv(next.rawArgs); err != nil {
		next.writeStatus(next.stderr, account, source)
		return next.runtimeError(account, source, err)
	}
	client := gmail.NewClient(gmail.ClientConfig{
		Read:    source,
		Account: string(account),
	})
	if _, err := source.Resolve(context.Background()); err != nil {
		next.writeStatus(next.stderr, account, source)
		return next.runtimeError(account, source, err)
	}
	profile, err := client.GetProfile(context.Background())
	if err != nil {
		next.writeStatus(next.stderr, account, source)
		return next.runtimeError(account, source, err)
	}
	cache := source.CacheState()
	if next.json {
		output := statusOutput{
			Account: string(account),
			Route:   string(source.LastRoute()),
			Cache:   cacheOutput(cache),
			Profile: statusProfile{Email: profile.EmailAddress},
			OK:      true,
		}
		if err := writeJSON(next.stdout, output); err != nil {
			next.writeStatus(next.stderr, account, source)
			return next.runtimeError(account, source, wrapError("write JSON", err))
		}
		return 0
	}
	next.writeStatus(next.stdout, account, source)
	fmt.Fprintf(next.stdout, "profile: %s\n", profile.EmailAddress)
	return 0
}

type statusOutput struct {
	Account string        `json:"account"`
	Route   string        `json:"route"`
	Cache   statusCache   `json:"cache"`
	Profile statusProfile `json:"profile"`
	OK      bool          `json:"ok"`
}

type statusCache struct {
	Path   string    `json:"path"`
	Exists bool      `json:"exists"`
	Valid  bool      `json:"valid"`
	Expiry time.Time `json:"expiry"`
}

type statusProfile struct {
	Email string `json:"email"`
}

func cacheOutput(cache auth.CacheState) statusCache {
	return statusCache{Path: cache.Path, Exists: cache.Exists, Valid: cache.Valid, Expiry: cache.Expiry}
}

func (cc *cmdCtx) writeStatus(output io.Writer, account auth.Account, source *auth.Source) {
	cache := source.CacheState()
	fmt.Fprintf(output, "account: %s\n", account)
	fmt.Fprintf(output, "route: %s\n", source.LastRoute())
	switch {
	case cache.Valid:
		fmt.Fprintf(output, "cache: valid until %s\n", cache.Expiry.UTC().Format(time.RFC3339))
	case cache.Exists:
		fmt.Fprintf(output, "cache: expired %s\n", cache.Expiry.UTC().Format(time.RFC3339))
	default:
		fmt.Fprintln(output, "cache: absent")
	}
}
