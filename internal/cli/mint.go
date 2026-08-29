package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/sjawhar/mailbox/internal/auth"
)

// runMint implements the hidden `mailbox __mint --env VAR` child contract.
// It deliberately receives no config or account selection.
func runMint(cc *cmdCtx, args []string) int {
	fs := flag.NewFlagSet("__mint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	envVar := fs.String("env", "", "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return failUsage(cc.stderr, err)
	}
	if err := requireArity(pos, 0, 0, "__mint"); err != nil || *envVar == "" {
		if err == nil {
			err = fmt.Errorf("__mint requires --env VAR")
		}
		return failUsage(cc.stderr, err)
	}
	if err := auth.RunMintChild(context.Background(), *envVar, cc.stdout); err != nil {
		fmt.Fprintf(cc.stderr, "mailbox __mint: %v\n", err)
		return 1
	}
	return 0
}
