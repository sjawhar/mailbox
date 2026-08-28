package cli

import (
	"context"
	"fmt"

	"github.com/sjawhar/mailbox/internal/auth"
)

// runMint implements the hidden `mailbox __mint --account <work|personal>`
// subcommand (spec §2): the short-lived child that secrets wraps so the
// human-tier refresh JSON never enters the TUI parent's environment.
func runMint(cc *cmdCtx, args []string) int {
	fs, accountFlag, jsonOutput := cc.flags("__mint")
	pos, next, code := cc.parse(fs, accountFlag, jsonOutput, args)
	if code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "__mint"); err != nil {
		return failUsage(cc.stderr, err)
	}
	account, err := auth.ResolveAccount(next.accountFlag)
	if err != nil {
		return next.runtimeError("", nil, err)
	}
	if err := auth.RunMintChild(context.Background(), account, next.stdout); err != nil {
		fmt.Fprintf(next.stderr, "mailbox __mint: %v\n", err)
		return 1
	}
	return 0
}
