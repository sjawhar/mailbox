package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// mintStdoutLimit caps the __mint child's stdout (F11): the contract is
	// one small JSON object; anything larger is shim chatter and fails loudly.
	mintStdoutLimit = 16 << 10
	// mintStderrLimit caps how much child stderr an error can embed (F12).
	mintStderrLimit = 8 << 10
)

// mintOutput is the __mint stdout contract: exactly one JSON object.
type mintOutput struct {
	AccessToken string `json:"access_token"`
	Expiry      string `json:"expiry"`
}

// RunMintChild is the child side of `mailbox __mint --account <a>`. It reads
// the authorized_user JSON from GWS_<ACCOUNT>_MODIFY_OAUTH in its own
// environment (placed there by secrets), performs one OAuth refresh, prints
// the token contract to stdout, and writes nothing to disk (spec §2).
func RunMintChild(ctx context.Context, account Account, stdout io.Writer) error {
	if os.Getenv("MAILBOX_TOKEN") != "" {
		// F11: MAILBOX_TOKEN in a mint child is an error, not an override.
		return fmt.Errorf("MAILBOX_TOKEN must not be set in a __mint child; the mint contract refreshes %s only", ModifyEnvKey(account))
	}
	key := ModifyEnvKey(account)
	rawJSON := os.Getenv(key)
	if rawJSON == "" {
		return &NeedsSecretsError{Key: key}
	}
	accessToken, expiry, err := refreshAccessToken(ctx, key, rawJSON)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(mintOutput{
		AccessToken: accessToken,
		Expiry:      expiry.UTC().Format(time.RFC3339),
	})
}

// ExecMinter is the TUI's minter (spec §2 parent side): env fast-path, else
// spawn `secrets GWS_<ACCOUNT>_MODIFY_OAUTH -- <self> __mint --account <a>`.
type ExecMinter struct {
	// Stderr receives the mint child's stderr stream live (secretsd's
	// request/touch messaging, F12). Nil discards it. Mint errors embed a
	// capped copy regardless.
	Stderr io.Writer
}

func (m *ExecMinter) Mint(ctx context.Context, account Account) (Token, error) {
	token, found, err := mintFromEnv(ctx, account)
	if err != nil {
		return Token{}, err
	}
	if found {
		return token, nil
	}
	return m.mintViaSecrets(ctx, account)
}

func (m *ExecMinter) mintViaSecrets(ctx context.Context, account Account) (Token, error) {
	key := ModifyEnvKey(account)
	// os.Executable is a correctness measure (re-exec the same binary), not a
	// tamper defense (spec accepted risk 2).
	self, err := os.Executable()
	if err != nil {
		return Token{}, err
	}
	secretsBin, err := findSecrets()
	if err != nil {
		return Token{}, err
	}
	cmd := exec.CommandContext(ctx, secretsBin, key, "--", self, "__mint", "--account", string(account))
	env := ScrubbedEnviron()
	if tokenFile := os.Getenv("SECRETSD_SESSION_TOKEN_FILE"); tokenFile != "" {
		// The one sanctioned re-injection (F3): the secretsd client proves
		// session scope with it. Every other child mailbox spawns loses it.
		env = append(env, "SECRETSD_SESSION_TOKEN_FILE="+tokenFile)
	}
	cmd.Env = env

	stderr := &truncatingBuffer{limit: mintStderrLimit}
	if m.Stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, m.Stderr)
	} else {
		cmd.Stderr = stderr
	}
	stdout := &cappedBuffer{limit: mintStdoutLimit}
	cmd.Stdout = stdout

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return Token{}, fmt.Errorf("mint %s via secrets: %w", key, err)
		}
		return Token{}, fmt.Errorf("mint %s via secrets: %w: %s", key, err, detail)
	}
	token, err := parseMintOutput(stdout.Bytes())
	if err != nil {
		return Token{}, fmt.Errorf("mint %s: %w", key, err)
	}
	return token, nil
}

// parseMintOutput enforces the strict single-object contract (F11): unknown
// fields, trailing bytes, and oversize output fail loudly. A scavenging scan
// is prohibited; shim chatter must not yield a token.
func parseMintOutput(data []byte) (Token, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var output mintOutput
	if err := decoder.Decode(&output); err != nil {
		return Token{}, fmt.Errorf("decode __mint stdout: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Token{}, fmt.Errorf("__mint stdout has trailing content after the token object")
	}
	if output.AccessToken == "" {
		return Token{}, fmt.Errorf("__mint returned an empty access_token")
	}
	expiry, err := time.Parse(time.RFC3339, output.Expiry)
	if err != nil {
		return Token{}, fmt.Errorf("__mint returned invalid expiry: %w", err)
	}
	if !expiry.After(time.Now()) {
		return Token{}, fmt.Errorf("__mint returned an already-expired token")
	}
	return Token{AccessToken: output.AccessToken, Route: RouteMint, Expiry: expiry}, nil
}

// cappedBuffer fails loudly when the writer exceeds limit (stdout, F11).
type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("output exceeded %d bytes", b.limit)
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// truncatingBuffer keeps the first limit bytes and silently accepts the rest
// (stderr: informational, must not kill the mint).
type truncatingBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *truncatingBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *truncatingBuffer) String() string { return b.buf.String() }
