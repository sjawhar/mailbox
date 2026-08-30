package auth

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
)

const (
	credentialDepthEnvironment  = "MAILBOX_CREDENTIAL_DEPTH"
	maximumCredentialChildDepth = 2
)

// ScrubbedEnviron returns the parent environment minus the unconditional deny
// set, configured credential environment variables, and configured scrub rules.
// MAILBOX_CREDENTIAL_DEPTH is deliberately retained for credential command
// recursion detection.
func ScrubbedEnviron(cfg *Config) []string {
	return scrubbedEnviron(cfg, os.Environ())
}

// CredentialChildEnviron returns the restricted environment for a requested
// credential class, restoring only its account's shared and class-private safe
// configured passthrough values and incrementing the credential recursion depth.
func CredentialChildEnviron(cfg *Config, acct *AccountConfig, class Class) []string {
	parent := os.Environ()
	child := withoutCredentialPassthrough(scrubbedEnviron(cfg, parent), cfg)
	child = restoreCredentialPassthrough(child, parent, cfg, acct.Passthrough)
	child = restoreCredentialPassthrough(child, parent, cfg, credentialPassthroughForClass(acct, class))

	depth, err := credentialDepth(parent)
	if err != nil {
		depth = 0
	}
	if depth >= maximumCredentialChildDepth {
		depth = maximumCredentialChildDepth - 1
	}
	return setEnvironmentValue(child, credentialDepthEnvironment, strconv.Itoa(depth+1))
}

func scrubbedEnviron(cfg *Config, parent []string) []string {
	kept := make([]string, 0, len(parent))
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if shouldScrubEnvironment(cfg, name) || hasEnvironmentName(kept, name) {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

func shouldScrubEnvironment(cfg *Config, name string) bool {
	if name == credentialDepthEnvironment {
		return false
	}
	if _, denied := credentialPassthroughDeny[name]; denied {
		return true
	}
	if cfg == nil {
		return false
	}
	if isConfiguredCredentialEnvironment(cfg, name) {
		return true
	}
	for _, scrubbed := range cfg.ScrubEnv {
		if name == scrubbed {
			return true
		}
	}
	for _, pattern := range cfg.ScrubEnvPatterns {
		matched, _ := path.Match(pattern, name)
		if matched {
			return true
		}
	}
	return false
}

func isConfiguredCredentialEnvironment(cfg *Config, name string) bool {
	if cfg == nil {
		return false
	}
	for _, acct := range cfg.Accounts {
		for _, source := range []*CredentialSource{acct.Read, acct.Write, acct.Send} {
			if source != nil && source.Kind == SourceEnv && source.EnvVar == name {
				return true
			}
		}
	}
	return false
}

func withoutCredentialPassthrough(env []string, cfg *Config) []string {
	if cfg == nil {
		return env
	}
	kept := env[:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if isConfiguredCredentialPassthrough(cfg, name) {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

func restoreCredentialPassthrough(child, parent []string, cfg *Config, names []string) []string {
	for _, name := range names {
		if _, forbidden := credentialPassthroughDeny[name]; forbidden || isConfiguredCredentialEnvironment(cfg, name) {
			continue
		}
		if value, ok := environmentValue(parent, name); ok {
			child = setEnvironmentValue(child, name, value)
		}
	}
	return child
}

func credentialPassthroughForClass(account *AccountConfig, class Class) []string {
	switch class {
	case ClassRead:
		return account.ReadPassthrough
	case ClassWrite:
		return account.WritePassthrough
	case ClassSend:
		return account.SendPassthrough
	default:
		return nil
	}
}

func isConfiguredCredentialPassthrough(cfg *Config, name string) bool {
	for _, account := range cfg.Accounts {
		if containsName(account.Passthrough, name) ||
			containsName(account.ReadPassthrough, name) ||
			containsName(account.WritePassthrough, name) ||
			containsName(account.SendPassthrough, name) {
			return true
		}
	}
	return false
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func credentialDepth(env []string) (int, error) {
	current, ok := environmentValue(env, credentialDepthEnvironment)
	if !ok || current == "" {
		return 0, nil
	}
	depth, err := strconv.Atoi(current)
	if err != nil {
		return 0, fmt.Errorf("parse credential depth: %w", err)
	}
	if depth < 0 {
		return 0, nil
	}
	return depth, nil
}

func environmentValue(env []string, want string) (string, bool) {
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if name == want {
			return value, true
		}
	}
	return "", false
}

func hasEnvironmentName(env []string, want string) bool {
	_, ok := environmentValue(env, want)
	return ok
}

func setEnvironmentValue(env []string, name, value string) []string {
	for i, kv := range env {
		current, existing, _ := strings.Cut(kv, "=")
		if current != name {
			continue
		}
		if existing != value {
			env[i] = name + "=" + value
		}
		return env
	}
	return append(env, name+"="+value)
}
