package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/sjawhar/mailbox/internal/render"
)

const (
	mintStderrLimit = 8 << 10
	diagnosticLimit = 2048
)

var (
	bareTokenPattern     = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{20,}$`)
	ErrCredentialTimeout = errors.New("credential command timed out")
)

func runCredentialCmd(ctx context.Context, cfg *Config, acct *AccountConfig, src *CredentialSource) (Acquired, error) {
	depth, err := credentialDepth(os.Environ())
	if err != nil || depth >= 1 {
		return Acquired{}, credentialError(cfg, acct, src.Class, src, ReasonRecursion)
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
	cmd.Env = CredentialChildEnviron(cfg, acct, src.Class)
	stdout := &cappedBuffer{limit: mintStdoutLimit}
	stderr := &tailBuffer{limit: mintStderrLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// A nil Cmd.Stdin is os.DevNull. Helpers inherit stdin only when an
	// interactive source is launched from a real terminal.
	if src.Interactive && term.IsTerminal(int(os.Stdin.Fd())) {
		cmd.Stdin = os.Stdin
	}

	err = cmd.Run()
	diagnostic := diagnosticFrom(stderr.String())
	if stdout.err != nil {
		return Acquired{}, credentialCommandError(src, diagnostic, stdout.err)
	}
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
	if diagnostic = failureDiagnostic(diagnostic); diagnostic != "" {
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

// cappedBuffer fails the command when stdout exceeds its strict contract cap.
type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
	err   error
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.buf.Len()+len(data) > b.limit {
		b.err = fmt.Errorf("output exceeded %d bytes", b.limit)
		return 0, b.err
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

func failureDiagnostic(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}
