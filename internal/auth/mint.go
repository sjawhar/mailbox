package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sjawhar/mailbox/internal/render"
)

const (
	mintStdoutLimit = 16 << 10
	mintStderrLimit = 8 << 10
	diagnosticLimit = 2048
)

var (
	bareTokenPattern     = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{20,}$`)
	ErrCredentialTimeout = errors.New("credential command timed out")
)

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
		return Acquired{}, credentialError(a.Cfg, acct, class, src, ReasonInteractive)
	}
	return acquireEnv(ctx, a.Cfg, acct, src)
}

// ExecAcquirer can obtain environment credentials and non-interactive command
// credentials. BatchAcquirer selects it only for the latter.
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
	if src.Interactive {
		return Acquired{}, credentialError(a.Cfg, acct, class, src, ReasonInteractive)
	}
	return runCredentialCmd(ctx, a.Cfg, acct, src)
}

// InteractiveExecAcquirer is the sole acquirer that can execute interactive
// credential commands. Only the TUI constructs it.
type InteractiveExecAcquirer struct {
	Cfg *Config
}

func (a InteractiveExecAcquirer) Acquire(ctx context.Context, acct *AccountConfig, class Class) (Acquired, error) {
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

// BatchAcquirer is the choke point for every non-interactive surface. Its
// selected type makes a forbidden command spawn impossible for env, absent,
// and interactive sources.
func BatchAcquirer(cfg *Config, acct *AccountConfig, class Class) Acquirer {
	src := sourceFor(acct, class)
	if src == nil {
		return refusalAcquirer{err: credentialError(cfg, acct, class, nil, ReasonNoSource)}
	}
	if src.Kind == SourceEnv {
		return EnvOnlyAcquirer{Cfg: cfg}
	}
	if src.Interactive {
		return refusalAcquirer{err: credentialError(cfg, acct, class, src, ReasonInteractive)}
	}
	return ExecAcquirer{Cfg: cfg}
}

func acquireEnv(ctx context.Context, cfg *Config, acct *AccountConfig, src *CredentialSource) (Acquired, error) {
	raw := os.Getenv(src.EnvVar)
	if raw == "" {
		return Acquired{}, credentialError(cfg, acct, src.Class, src, ReasonEnvUnset)
	}
	if raw[0] == '{' {
		accessToken, expiry, err := refreshAccessToken(ctx, src.ConfigKey, raw)
		if err != nil {
			return Acquired{}, err
		}
		return Acquired{Token: Token{AccessToken: accessToken, Route: RouteEnv, Expiry: expiry}}, nil
	}
	token, err := parseBareCredential([]byte(raw), RouteEnv)
	if err != nil {
		return Acquired{}, fmt.Errorf("credential %s: %w", safeForTerminal(src.ConfigKey), err)
	}
	return Acquired{Token: token}, nil
}

func runCredentialCmd(ctx context.Context, cfg *Config, acct *AccountConfig, src *CredentialSource) (Acquired, error) {
	if current, ok := os.LookupEnv(credentialDepthEnvironment); ok {
		if depth, err := strconv.Atoi(current); err == nil && depth >= 1 {
			return Acquired{}, credentialError(cfg, acct, src.Class, src, ReasonRecursion)
		}
	}

	timeout := defaultCredentialTimeout
	if cfg != nil && cfg.CredentialTimeout > 0 {
		timeout = cfg.CredentialTimeout
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(deadline, src.Argv0, src.Argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = CredentialChildEnviron(cfg, acct)
	stdout := &cappedBuffer{limit: mintStdoutLimit}
	stderr := &tailBuffer{limit: mintStderrLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	diagnostic := diagnosticFrom(stderr.String())
	if err != nil {
		if errors.Is(deadline.Err(), context.DeadlineExceeded) {
			return Acquired{}, credentialCommandError(src, diagnostic, ErrCredentialTimeout)
		}
		return Acquired{}, credentialCommandError(src, diagnostic, err)
	}
	token, err := parseCredentialOutput(stdout.Bytes())
	if err != nil {
		return Acquired{}, credentialCommandError(src, diagnostic, err)
	}
	return Acquired{Token: token, Diagnostic: diagnostic}, nil
}

func credentialCommandError(src *CredentialSource, diagnostic string, err error) error {
	message := fmt.Sprintf("credential command %s (via %s)", safeForTerminal(src.ConfigKey), safeForTerminal(src.Argv0))
	if diagnostic != "" {
		return fmt.Errorf("%s: %w: %s", message, err, diagnostic)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func parseCredentialOutput(output []byte) (Token, error) {
	if len(output) == 0 {
		return Token{}, errors.New("credential command returned empty stdout")
	}
	if output[0] == '{' {
		return parseMintOutput(output)
	}
	return parseBareCredential(output, RouteCmd)
}

func parseBareCredential(value []byte, route Route) (Token, error) {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
	}
	return parseBareToken(value, route)
}

func parseBareToken(value []byte, route Route) (Token, error) {
	if len(value) > 4096 || !bareTokenPattern.Match(value) {
		return Token{}, errors.New("credential output is not a valid bare token")
	}
	return Token{AccessToken: string(value), Route: route}, nil
}

type mintOutput struct {
	AccessToken string `json:"access_token"`
	Expiry      string `json:"expiry"`
}

// RunMintChild implements `mailbox __mint --env VAR`. It deliberately loads
// no config and writes only the strict mint object to stdout.
func RunMintChild(ctx context.Context, envVar string, stdout io.Writer) error {
	if !envVarNamePattern.MatchString(envVar) {
		return fmt.Errorf("invalid __mint --env value %q", envVar)
	}
	if os.Getenv("MAILBOX_TOKEN") != "" {
		return fmt.Errorf("MAILBOX_TOKEN must not be set in a __mint child; the mint contract refreshes %s only", envVar)
	}
	raw := os.Getenv(envVar)
	if raw == "" {
		return fmt.Errorf("%s is unset", envVar)
	}
	accessToken, expiry, err := refreshAccessToken(ctx, envVar, raw)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(mintOutput{
		AccessToken: accessToken,
		Expiry:      expiry.UTC().Format(time.RFC3339),
	})
}

// parseMintOutput enforces one strict JSON object without trailing data.
func parseMintOutput(data []byte) (Token, error) {
	if len(data) > mintStdoutLimit {
		return Token{}, fmt.Errorf("output exceeded %d bytes", mintStdoutLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var output mintOutput
	if err := decoder.Decode(&output); err != nil {
		return Token{}, fmt.Errorf("decode __mint stdout: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Token{}, errors.New("__mint stdout has trailing content after the token object")
	}
	if output.AccessToken == "" {
		return Token{}, errors.New("__mint returned an empty access_token")
	}
	expiry, err := time.Parse(time.RFC3339, output.Expiry)
	if err != nil {
		return Token{}, fmt.Errorf("__mint returned invalid expiry: %w", err)
	}
	if !expiry.After(time.Now()) {
		return Token{}, errors.New("__mint returned an already-expired token")
	}
	return Token{AccessToken: output.AccessToken, Route: RouteCmd, Expiry: expiry}, nil
}

// cappedBuffer fails the command when stdout exceeds its strict contract cap.
type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if b.buf.Len()+len(data) > b.limit {
		return 0, fmt.Errorf("output exceeded %d bytes", b.limit)
	}
	return b.buf.Write(data)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// tailBuffer retains the final stderr bytes without allowing informational
// output to keep the process alive or grow without bound.
type tailBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	if len(data) >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(data[len(data)-b.limit:])
		return len(data), nil
	}
	overflow := b.buf.Len() + len(data) - b.limit
	if overflow > 0 {
		existing := append([]byte(nil), b.buf.Bytes()[overflow:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(existing)
	}
	_, _ = b.buf.Write(data)
	return len(data), nil
}

func (b *tailBuffer) String() string { return b.buf.String() }

func diagnosticFrom(value string) string {
	value = render.SanitizeTerminal(strings.TrimSpace(value))
	if len(value) > diagnosticLimit {
		return value[len(value)-diagnosticLimit:]
	}
	return value
}
