package auth

import (
	"context"
	"fmt"
	"os"
)

// Acquired is one credential acquisition outcome.
type Acquired struct {
	Token      Token
	Diagnostic string
}

// Acquirer obtains one credential class for an account.
type Acquirer interface {
	Acquire(ctx context.Context, acct *AccountConfig, class Class) (Acquired, error)
}

// EnvOnlyAcquirer can obtain only environment-declared credentials. It has no
// command execution path.
type EnvOnlyAcquirer struct {
	Cfg *Config
}

func (a EnvOnlyAcquirer) Acquire(ctx context.Context, acct *AccountConfig, class Class) (Acquired, error) {
	src := sourceFor(acct, class)
	if src == nil {
		return Acquired{}, credentialError(a.Cfg, acct, class, nil, ReasonNoSource)
	}
	if src.Kind != SourceEnv {
		return Acquired{}, credentialError(a.Cfg, acct, class, src, ReasonNoSource)
	}
	return acquireEnv(ctx, a.Cfg, acct, src)
}

// ExecAcquirer can obtain environment credentials and command credentials.
type ExecAcquirer struct {
	Cfg *Config
}

func (a ExecAcquirer) Acquire(ctx context.Context, acct *AccountConfig, class Class) (Acquired, error) {
	src := sourceFor(acct, class)
	if src == nil {
		return Acquired{}, credentialError(a.Cfg, acct, class, nil, ReasonNoSource)
	}
	if src.Kind == SourceEnv {
		return acquireEnv(ctx, a.Cfg, acct, src)
	}
	return runCredentialCmd(ctx, a.Cfg, acct, src)
}

type refusalAcquirer struct {
	err *NeedsCredentialError
}

func (a refusalAcquirer) Acquire(context.Context, *AccountConfig, Class) (Acquired, error) {
	return Acquired{}, a.err
}

// BatchAcquirer is the choke point for every batch surface. It refuses absent
// sources and keeps environment sources on environment acquisition; command
// sources always execute through ExecAcquirer.
func BatchAcquirer(cfg *Config, acct *AccountConfig, class Class) Acquirer {
	src := sourceFor(acct, class)
	if src == nil {
		return refusalAcquirer{err: credentialError(cfg, acct, class, nil, ReasonNoSource)}
	}
	if src.Kind == SourceEnv {
		return EnvOnlyAcquirer{Cfg: cfg}
	}
	return ExecAcquirer{Cfg: cfg}
}

func acquireEnv(ctx context.Context, cfg *Config, acct *AccountConfig, src *CredentialSource) (Acquired, error) {
	raw := os.Getenv(src.EnvVar)
	if raw == "" {
		return Acquired{}, credentialError(cfg, acct, src.Class, src, ReasonEnvUnset)
	}
	if raw[0] == '{' {
		accessToken, scope, expiry, err := refreshAccessToken(ctx, src.ConfigKey, raw)
		if err != nil {
			return Acquired{}, err
		}
		return Acquired{Token: Token{AccessToken: accessToken, Route: RouteEnv, Expiry: expiry, Scope: scope}}, nil
	}
	token, err := parseBareCredential([]byte(raw), RouteEnv)
	if err != nil {
		return Acquired{}, fmt.Errorf("credential %s: %w", safeForTerminal(src.ConfigKey), err)
	}
	return Acquired{Token: token}, nil
}
