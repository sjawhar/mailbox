package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const mintStdoutLimit = 16 << 10

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
