package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Route string

const (
	RouteEnvToken     Route = "env-token"
	RouteCache        Route = "cache"
	RouteBroker       Route = "broker"
	RouteOAuthRefresh Route = "oauth-refresh"
)

type Token struct {
	AccessToken string
	Route       Route
	Expiry      time.Time
}

type CacheState struct {
	Path   string
	Exists bool
	Valid  bool
	Expiry time.Time
}

// Source resolves credentials for one account and is safe for concurrent use.
type Source struct {
	account   Account
	mu        sync.Mutex
	mem       *Token
	lastRoute Route

	mutMu     sync.Mutex
	mutToken  *Token
	mutFlight chan struct{}
	mutRoute  Route
}

func NewSource(account Account) *Source {
	return &Source{account: account}
}

func (s *Source) Account() Account {
	return s.account
}

func (s *Source) Resolve(ctx context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if accessToken := os.Getenv("MAILBOX_TOKEN"); accessToken != "" {
		token := Token{AccessToken: accessToken, Route: RouteEnvToken}
		s.lastRoute = token.Route
		return token, nil
	}

	now := time.Now()
	if s.mem != nil && s.mem.Expiry.Add(-2*time.Minute).After(now) {
		s.lastRoute = s.mem.Route
		return *s.mem, nil
	}

	cached, err := readCache(s.account)
	if err != nil {
		return Token{}, err
	}
	if cached.valid(now) {
		token := Token{AccessToken: cached.AccessToken, Route: RouteCache, Expiry: cached.Expiry}
		s.mem = &token
		s.lastRoute = token.Route
		return token, nil
	}

	if s.account == AccountWork && onEC2() {
		accessToken, err := runBroker(ctx)
		if err != nil {
			return Token{}, err
		}
		token := Token{AccessToken: accessToken, Route: RouteBroker, Expiry: time.Now().Add(time.Hour)}
		if err := writeCache(s.account, cachedToken{AccessToken: token.AccessToken, Route: token.Route, Expiry: token.Expiry}); err != nil {
			return Token{}, err
		}
		s.mem = &token
		s.lastRoute = token.Route
		return token, nil
	}

	key := readEnvKey(s.account)
	rawJSON := os.Getenv(key)
	if rawJSON == "" {
		return Token{}, &NeedsSecretsError{Key: key}
	}
	accessToken, expiry, err := refreshAccessToken(ctx, key, rawJSON)
	if err != nil {
		return Token{}, err
	}
	token := Token{AccessToken: accessToken, Route: RouteOAuthRefresh, Expiry: expiry}
	if err := writeCache(s.account, cachedToken{AccessToken: token.AccessToken, Route: token.Route, Expiry: token.Expiry}); err != nil {
		return Token{}, err
	}
	s.mem = &token
	s.lastRoute = token.Route
	return token, nil
}

func (s *Source) AccessToken(ctx context.Context) (string, error) {
	token, err := s.Resolve(ctx)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func (s *Source) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRoute == RouteEnvToken || os.Getenv("MAILBOX_TOKEN") != "" {
		return nil
	}

	s.mem = nil
	path, err := cachePath(s.account)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete token cache %s: %w", path, err)
	}
	return nil
}

func (s *Source) CacheState() CacheState {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := cachePath(s.account)
	if err != nil {
		return CacheState{}
	}
	state := CacheState{Path: path}
	if _, err := os.Stat(path); err != nil {
		return state
	}
	state.Exists = true
	cached, err := readCache(s.account)
	if err != nil || cached == nil {
		return state
	}
	state.Expiry = cached.Expiry
	state.Valid = cached.valid(time.Now())
	return state
}

func (s *Source) LastRoute() Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRoute
}

type NeedsSecretsError struct {
	Key string
}

func (e *NeedsSecretsError) Error() string {
	return fmt.Sprintf("%s is unset", e.Key)
}

func (s *Source) EnsureEnv(argv []string) error {
	_, err := s.Resolve(context.Background())
	var needs *NeedsSecretsError
	if !errors.As(err, &needs) {
		return err
	}
	if os.Getenv("MAILBOX_SECRETS_REEXEC") != "" {
		return fmt.Errorf("%s still unset after re-exec under secrets", needs.Key)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	secretsBin, err := findSecrets()
	if err != nil {
		return err
	}
	args := append([]string{"secrets", needs.Key, "--", executable}, argv...)
	env := append(ScrubbedEnviron(), "MAILBOX_SECRETS_REEXEC=1")
	return syscall.Exec(secretsBin, args, env)
}

func ProvisioningHint(account Account, route Route) string {
	switch route {
	case RouteBroker:
		return fmt.Sprintf("the broker token is read-only; mutations need %s (human tier); see README", ModifyEnvKey(account))
	case RouteEnvToken:
		return "MAILBOX_TOKEN lacks the gmail.modify scope; see README"
	case RouteMint, RouteMutationEnv:
		return fmt.Sprintf("%s lacks the gmail.modify scope; re-run the provisioning ceremony (README)", ModifyEnvKey(account))
	default:
		return fmt.Sprintf("%s lacks the gmail.modify scope; see README", readEnvKey(account))
	}
}

func readEnvKey(account Account) string {
	if account == AccountPersonal {
		return "GWS_PERSONAL_READ_OAUTH"
	}
	return "GWS_WORK_READ_OAUTH"
}

func onEC2() bool {
	vendorFile := os.Getenv("MAILBOX_DMI_SYS_VENDOR")
	if vendorFile == "" {
		vendorFile = "/sys/class/dmi/id/sys_vendor"
	}
	vendor, err := os.ReadFile(vendorFile)
	return err == nil && strings.HasPrefix(string(vendor), "Amazon EC2")
}

func runBroker(ctx context.Context) (string, error) {
	broker, err := findBroker()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, broker, "--scopes", "gmail.readonly")
	cmd.Env = ScrubbedEnviron()
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("google-user-token: %w", err)
	}
	accessToken := strings.TrimSpace(string(output))
	if accessToken == "" {
		return "", fmt.Errorf("google-user-token returned an empty token")
	}
	return accessToken, nil
}

func findBroker() (string, error) {
	if broker := os.Getenv("MAILBOX_BROKER"); broker != "" {
		return broker, nil
	}
	if broker, err := exec.LookPath("google-user-token"); err == nil {
		return broker, nil
	}
	return "", fmt.Errorf("google-user-token not found on PATH")
}

func findSecrets() (string, error) {
	if secrets, err := exec.LookPath("secrets"); err == nil {
		return secrets, nil
	}
	return "", fmt.Errorf("secrets executable not found on PATH")
}

// ScrubbedEnviron returns the process environment without credentials or
// one-shot mailbox execution state. It is the required environment for every
// child process mailbox starts.
func ScrubbedEnviron() []string {
	env := os.Environ()
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !isCredentialEnvironment(name) {
			kept = append(kept, kv)
		}
	}
	return kept
}

func isCredentialEnvironment(name string) bool {
	if name == "MAILBOX_TOKEN" || name == "MAILBOX_SECRETS_REEXEC" || name == "SECRETSD_SESSION_TOKEN_FILE" {
		return true
	}
	return strings.HasPrefix(name, "GWS_") && strings.HasSuffix(name, "_OAUTH")
}
