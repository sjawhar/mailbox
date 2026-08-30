package auth

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultCredentialTimeout = 120 * time.Second
	maxConfigBytes           = 262144
)

type sanitizedWrappedError struct {
	message string
	err     error
}

func (e *sanitizedWrappedError) Error() string { return e.message }

func (e *sanitizedWrappedError) Unwrap() error { return e.err }

var (
	accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envVarNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

var credentialPassthroughDeny = map[string]struct{}{
	"MAILBOX_TOKEN":     {},
	"MAILBOX_TOKEN_URL": {},
	"MAILBOX_CONFIG":    {},
}

type Class string

const (
	ClassRead  Class = "read"
	ClassWrite Class = "write"
	ClassSend  Class = "send"
)

type SourceKind string

const (
	SourceEnv SourceKind = "env"
	SourceCmd SourceKind = "cmd"
)

const (
	RouteEnv Route = "env"
	RouteCmd Route = "cmd"
)

// CredentialSource is one class's compiled source for one account.
type CredentialSource struct {
	Class       Class
	Kind        SourceKind
	EnvVar      string
	Argv        []string
	Argv0       string
	Interactive bool
	Label       string
	ConfigKey   string
}

type AccountConfig struct {
	Name             string
	Read             *CredentialSource
	Write            *CredentialSource
	Send             *CredentialSource
	Passthrough      []string
	ReadPassthrough  []string
	WritePassthrough []string
	SendPassthrough  []string
}

type Config struct {
	Path              string
	DefaultPath       string
	DefaultAccount    string
	Accounts          []*AccountConfig
	ScrubEnv          []string
	ScrubEnvPatterns  []string
	CredentialTimeout time.Duration
}

type rawAccount struct {
	ReadCredentialEnv          *string   `toml:"read_credential_env"`
	ReadCredentialCmd          *[]string `toml:"read_credential_cmd"`
	ReadInteractive            *bool     `toml:"read_interactive"`
	ReadCredentialPassthrough  []string  `toml:"read_credential_env_passthrough"`
	WriteCredentialEnv         *string   `toml:"write_credential_env"`
	WriteCredentialCmd         *[]string `toml:"write_credential_cmd"`
	WriteInteractive           *bool     `toml:"write_interactive"`
	WriteLabel                 *string   `toml:"write_label"`
	WriteCredentialPassthrough []string  `toml:"write_credential_env_passthrough"`
	SendCredentialEnv          *string   `toml:"send_credential_env"`
	SendCredentialCmd          *[]string `toml:"send_credential_cmd"`
	SendInteractive            *bool     `toml:"send_interactive"`
	SendLabel                  *string   `toml:"send_label"`
	SendCredentialPassthrough  []string  `toml:"send_credential_env_passthrough"`
	Passthrough                []string  `toml:"credential_env_passthrough"`
}

type rawConfig struct {
	DefaultAccount        *string               `toml:"default_account"`
	ScrubEnv              []string              `toml:"scrub_env"`
	ScrubEnvPatterns      []string              `toml:"scrub_env_patterns"`
	CredentialTimeoutSecs *int                  `toml:"credential_timeout_secs"`
	Accounts              map[string]rawAccount `toml:"accounts"`
}

// LoadConfig loads the explicit or default mailbox configuration file.
func LoadConfig() (*Config, error) {
	path, explicit, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := readTrustedConfig(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return noConfig(path), nil
		}
		return nil, err
	}

	var raw rawConfig
	metadata, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, configError(path, "%v", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return nil, configError(path, "unknown key %q", undecoded[0].String())
	}

	return compileConfig(path, raw, metadata.Keys())
}

func configPath() (string, bool, error) {
	if configured, ok := os.LookupEnv("MAILBOX_CONFIG"); ok {
		path, err := filepath.Abs(configured)
		if err != nil {
			return "", true, fmt.Errorf("resolve MAILBOX_CONFIG path: %w", err)
		}
		return path, true, nil
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("resolve home directory for mailbox config: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	path, err := filepath.Abs(filepath.Join(configHome, "mailbox", "config.toml"))
	if err != nil {
		return "", false, fmt.Errorf("resolve default mailbox config path: %w", err)
	}
	return path, false, nil
}

func noConfig(defaultPath string) *Config {
	return &Config{
		DefaultPath:       defaultPath,
		Accounts:          []*AccountConfig{{Name: "default"}},
		CredentialTimeout: defaultCredentialTimeout,
	}
}

func readTrustedConfig(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &sanitizedWrappedError{
			message: fmt.Sprintf("config %s: open: %s", safeForTerminal(path), safeForTerminal(err.Error())),
			err:     err,
		}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, configError(path, "fstat: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, configError(path, "not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, configError(path, "cannot determine ownership")
	}
	if stat.Uid != uint32(os.Getuid()) {
		return nil, configError(path, "not owned by uid %d", os.Getuid())
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, configError(path, "group- or world-writable (mode %04o); refusing to load", info.Mode().Perm())
	}
	if info.Size() > maxConfigBytes {
		return nil, configError(path, "larger than %d bytes", maxConfigBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, configError(path, "read: %v", err)
	}
	if len(data) > maxConfigBytes {
		return nil, configError(path, "larger than %d bytes", maxConfigBytes)
	}
	return data, nil
}

func compileConfig(configPath string, raw rawConfig, keys []toml.Key) (*Config, error) {
	if len(raw.Accounts) == 0 {
		return nil, configError(configPath, "accounts: at least one account is required")
	}

	for _, name := range raw.ScrubEnv {
		if name == "" || strings.Contains(name, "=") {
			return nil, configError(configPath, "scrub_env: entries must be non-empty and must not contain '='")
		}
	}
	for _, pattern := range raw.ScrubEnvPatterns {
		if _, err := path.Match(pattern, "x"); err != nil {
			return nil, configError(configPath, "scrub_env_patterns: invalid pattern %q: %v", pattern, err)
		}
	}

	timeout := defaultCredentialTimeout
	if raw.CredentialTimeoutSecs != nil {
		if *raw.CredentialTimeoutSecs <= 0 {
			return nil, configError(configPath, "credential_timeout_secs must be positive")
		}
		timeout = time.Duration(*raw.CredentialTimeoutSecs) * time.Second
	}

	order := accountOrder(raw.Accounts, keys)
	accounts := make([]*AccountConfig, 0, len(order))
	seenNames := make(map[string]string, len(order))
	for _, name := range order {
		if !accountNamePattern.MatchString(name) {
			return nil, configError(configPath, "accounts.%s: invalid account name", name)
		}
		folded := strings.ToLower(name)
		if previous, ok := seenNames[folded]; ok {
			return nil, configError(configPath, "accounts.%s collides case-insensitively with accounts.%s", name, previous)
		}
		seenNames[folded] = name

		rawAccount := raw.Accounts[name]
		account, err := compileAccount(configPath, name, rawAccount)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}

	declaredVars := make(map[string]string)
	for _, account := range accounts {
		for _, source := range []*CredentialSource{account.Read, account.Write, account.Send} {
			if source == nil || source.Kind != SourceEnv {
				continue
			}
			if previous, ok := declaredVars[source.EnvVar]; ok {
				return nil, configError(configPath, "%s declared by both %s and %s", source.EnvVar, previous, source.ConfigKey)
			}
			declaredVars[source.EnvVar] = source.ConfigKey
		}
	}
	declaredPassthrough := make(map[string]credentialPassthroughDeclaration)
	for _, account := range accounts {
		for _, declaration := range credentialPassthroughDeclarations(account) {
			for _, variable := range declaration.variables {
				if _, denied := credentialPassthroughDeny[variable]; denied {
					return nil, configError(configPath, "%s must not name %s", declaration.key, variable)
				}
				if source, declared := declaredVars[variable]; declared {
					return nil, configError(configPath, "%s names %s, declared by %s", declaration.key, variable, source)
				}
				if previous, declared := declaredPassthrough[variable]; declared && previous.class != declaration.class {
					return nil, configError(configPath, "%s names %s, already named by %s (ambiguous credential environment passthrough custody)", declaration.key, variable, previous.key)
				}
				declaredPassthrough[variable] = declaration
			}
		}
	}

	defaultAccount := ""
	if raw.DefaultAccount != nil {
		defaultAccount = *raw.DefaultAccount
		if _, ok := accountByName(accounts, defaultAccount); !ok {
			return nil, configError(configPath, "default_account %q names no configured account", defaultAccount)
		}
	} else if len(accounts) > 1 {
		return nil, configError(configPath, "default_account is required when more than one account is configured")
	}

	return &Config{
		Path:              configPath,
		DefaultAccount:    defaultAccount,
		Accounts:          accounts,
		ScrubEnv:          raw.ScrubEnv,
		ScrubEnvPatterns:  raw.ScrubEnvPatterns,
		CredentialTimeout: timeout,
	}, nil
}

func accountOrder(accounts map[string]rawAccount, keys []toml.Key) []string {
	order := make([]string, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, key := range keys {
		if len(key) != 2 || key[0] != "accounts" {
			continue
		}
		name := key[1]
		if _, exists := accounts[name]; !exists {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		order = append(order, name)
	}

	if len(order) == len(accounts) {
		return order
	}
	remaining := make([]string, 0, len(accounts)-len(order))
	for name := range accounts {
		if _, found := seen[name]; !found {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(order, remaining...)
}

func compileAccount(configPath, name string, raw rawAccount) (*AccountConfig, error) {
	read, err := compileCredentialSource(configPath, name, ClassRead, raw.ReadCredentialEnv, raw.ReadCredentialCmd, raw.ReadInteractive, nil)
	if err != nil {
		return nil, err
	}
	if read == nil {
		return nil, configError(configPath, "accounts.%s.read: no credential source configured", name)
	}

	write, err := compileCredentialSource(configPath, name, ClassWrite, raw.WriteCredentialEnv, raw.WriteCredentialCmd, raw.WriteInteractive, raw.WriteLabel)
	if err != nil {
		return nil, err
	}
	if raw.WriteLabel != nil && write == nil {
		return nil, configError(configPath, "accounts.%s.write_label requires a write credential source", name)
	}

	send, err := compileCredentialSource(configPath, name, ClassSend, raw.SendCredentialEnv, raw.SendCredentialCmd, raw.SendInteractive, raw.SendLabel)
	if err != nil {
		return nil, err
	}
	if raw.SendLabel != nil && send == nil {
		return nil, configError(configPath, "accounts.%s.send_label requires a send credential source", name)
	}

	if err := validateCredentialPassthrough(configPath, name, ClassRead, raw.ReadCredentialPassthrough); err != nil {
		return nil, err
	}
	if err := validateCredentialPassthrough(configPath, name, ClassWrite, raw.WriteCredentialPassthrough); err != nil {
		return nil, err
	}
	if err := validateCredentialPassthrough(configPath, name, ClassSend, raw.SendCredentialPassthrough); err != nil {
		return nil, err
	}
	if err := validateCredentialPassthrough(configPath, name, "", raw.Passthrough); err != nil {
		return nil, err
	}

	return &AccountConfig{
		Name:             name,
		Read:             read,
		Write:            write,
		Send:             send,
		Passthrough:      raw.Passthrough,
		ReadPassthrough:  raw.ReadCredentialPassthrough,
		WritePassthrough: raw.WriteCredentialPassthrough,
		SendPassthrough:  raw.SendCredentialPassthrough,
	}, nil
}

type credentialPassthroughDeclaration struct {
	key       string
	class     Class
	variables []string
}

func credentialPassthroughDeclarations(account *AccountConfig) []credentialPassthroughDeclaration {
	prefix := fmt.Sprintf("accounts.%s", account.Name)
	return []credentialPassthroughDeclaration{
		{key: prefix + ".credential_env_passthrough", variables: account.Passthrough},
		{key: prefix + ".read_credential_env_passthrough", class: ClassRead, variables: account.ReadPassthrough},
		{key: prefix + ".write_credential_env_passthrough", class: ClassWrite, variables: account.WritePassthrough},
		{key: prefix + ".send_credential_env_passthrough", class: ClassSend, variables: account.SendPassthrough},
	}
}

func validateCredentialPassthrough(configPath, accountName string, class Class, variables []string) error {
	key := fmt.Sprintf("accounts.%s.credential_env_passthrough", accountName)
	if class != "" {
		key = fmt.Sprintf("accounts.%s.%s_credential_env_passthrough", accountName, class)
	}
	for _, variable := range variables {
		if !envVarNamePattern.MatchString(variable) {
			return configError(configPath, "%s: invalid environment variable name %q", key, variable)
		}
	}
	return nil
}

func compileCredentialSource(configPath, accountName string, class Class, environment *string, command *[]string, interactive *bool, label *string) (*CredentialSource, error) {
	prefix := fmt.Sprintf("accounts.%s.%s", accountName, class)
	environmentKey := prefix + "_credential_env"
	commandKey := prefix + "_credential_cmd"
	interactiveKey := prefix + "_interactive"

	if environment != nil && command != nil {
		return nil, configError(configPath, "%s: both %s and %s are configured", prefix, environmentKey, commandKey)
	}
	if interactive != nil && command == nil {
		return nil, configError(configPath, "%s requires %s", interactiveKey, commandKey)
	}
	if environment == nil && command == nil {
		return nil, nil
	}
	if environment != nil {
		if !envVarNamePattern.MatchString(*environment) {
			return nil, configError(configPath, "%s: invalid environment variable name %q", environmentKey, *environment)
		}
		return &CredentialSource{
			Class:     class,
			Kind:      SourceEnv,
			EnvVar:    *environment,
			ConfigKey: environmentKey,
		}, nil
	}

	argv := *command
	if len(argv) == 0 || argv[0] == "" {
		return nil, configError(configPath, "%s: argv must include a non-empty argv0", commandKey)
	}
	argv0, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, configError(configPath, "%s: argv0 %q not found on PATH", commandKey, argv[0])
	}
	argv0, err = filepath.Abs(argv0)
	if err != nil {
		return nil, configError(configPath, "%s: resolve argv0 %q: %v", commandKey, argv[0], err)
	}

	interactiveValue := class != ClassRead
	if interactive != nil {
		interactiveValue = *interactive
	}
	credential := &CredentialSource{
		Class:       class,
		Kind:        SourceCmd,
		Argv:        argv,
		Argv0:       argv0,
		Interactive: interactiveValue,
		ConfigKey:   commandKey,
	}
	if label != nil {
		credential.Label = *label
	}
	return credential, nil
}

func configError(path, format string, args ...any) error {
	message := safeForTerminal(fmt.Sprintf(format, args...))
	return fmt.Errorf("config %s: %s", safeForTerminal(path), message)
}

func (c *Config) NoConfig() bool {
	return c.Path == ""
}

func (c *Config) Account(name string) (*AccountConfig, bool) {
	return accountByName(c.Accounts, name)
}

func accountByName(accounts []*AccountConfig, name string) (*AccountConfig, bool) {
	for _, account := range accounts {
		if account.Name == name {
			return account, true
		}
	}
	return nil, false
}

func (c *Config) AccountNames() []string {
	names := make([]string, len(c.Accounts))
	for i, account := range c.Accounts {
		names[i] = account.Name
	}
	return names
}

func (c *Config) ResolveAccount(flagValue string) (*AccountConfig, error) {
	name := flagValue
	if name == "" {
		name = os.Getenv("MAILBOX_ACCOUNT")
	}
	if name == "" {
		name = c.DefaultAccount
	}
	if name == "" && len(c.Accounts) == 1 {
		name = c.Accounts[0].Name
	}
	if !accountNamePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid account %q", name)
	}
	if account, ok := c.Account(name); ok {
		return account, nil
	}
	return nil, fmt.Errorf("unknown account %q; configured accounts: %s", name, strings.Join(c.AccountNames(), ", "))
}

// CredentialReason enumerates why a class credential is unavailable.
type CredentialReason string

const (
	ReasonNoSource  CredentialReason = "no credential source configured"
	ReasonNoConfig  CredentialReason = "no config file"
	ReasonEnvUnset  CredentialReason = "declared environment variable is unset"
	ReasonRecursion CredentialReason = "credential command recursion (MAILBOX_CREDENTIAL_DEPTH is set)"
)

// NeedsCredentialError describes an unavailable credential without exposing
// credentials or configured command arguments.
type NeedsCredentialError struct {
	Account    string
	Class      Class
	ConfigKey  string
	ConfigPath string
	Reason     CredentialReason
}

func (e *NeedsCredentialError) Error() string {
	if e.Reason == ReasonNoConfig {
		if e.Class == ClassSend {
			return fmt.Sprintf("account %q has no usable send credential: %s — create %s with accounts.%s.send_credential_env or accounts.%s.send_credential_cmd (see README, Configuration)", safeForTerminal(e.Account), e.Reason, safeForTerminal(e.ConfigPath), safeForTerminal(e.Account), safeForTerminal(e.Account))
		}
		return fmt.Sprintf("account %q has no usable %s credential: %s — create %s (see README, Configuration) or set MAILBOX_TOKEN", safeForTerminal(e.Account), e.Class, e.Reason, safeForTerminal(e.ConfigPath))
	}
	return fmt.Sprintf("account %q has no usable %s credential: %s — %s (config: %s)", safeForTerminal(e.Account), e.Class, e.Reason, safeForTerminal(e.ConfigKey), safeForTerminal(e.ConfigPath))
}
