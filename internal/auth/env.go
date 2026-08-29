package auth

import (
	"os"
	"path"
	"strconv"
	"strings"
)

const credentialDepthEnvironment = "MAILBOX_CREDENTIAL_DEPTH"

// configScrubbedEnviron returns the parent environment minus the unconditional
// deny set, configured credential environment variables, and configured scrub
// rules. MAILBOX_CREDENTIAL_DEPTH is deliberately retained for credential
// command recursion detection.
//
// This bridge name avoids colliding with the legacy zero-argument
// ScrubbedEnviron until the config cutover removes it.
func configScrubbedEnviron(cfg *Config) []string {
	return scrubbedEnviron(cfg, os.Environ())
}

// CredentialChildEnviron returns the restricted environment for an account's
// credential command, restoring only its safe configured passthrough values
// and incrementing the credential recursion depth.
func CredentialChildEnviron(cfg *Config, acct *AccountConfig) []string {
	parent := os.Environ()
	child := scrubbedEnviron(cfg, parent)

	for _, name := range acct.Passthrough {
		if _, forbidden := credentialPassthroughDeny[name]; forbidden || isConfiguredCredentialEnvironment(cfg, name) {
			continue
		}
		if value, ok := environmentValue(parent, name); ok {
			child = setEnvironmentValue(child, name, value)
		}
	}

	depth := 0
	if current, ok := environmentValue(parent, credentialDepthEnvironment); ok {
		if parsed, err := strconv.Atoi(current); err == nil {
			depth = parsed
		}
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
	if _, denied := credentialPassthroughDeny[name]; denied || isConfiguredCredentialEnvironment(cfg, name) {
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
	for _, acct := range cfg.Accounts {
		for _, source := range []*CredentialSource{acct.Read, acct.Write} {
			if source != nil && source.Kind == SourceEnv && source.EnvVar == name {
				return true
			}
		}
	}
	return false
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
