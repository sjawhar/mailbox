package cli

import (
	"context"
	"fmt"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/render"
)

type openPayload struct {
	Account   string `json:"account"`
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
	File      string `json:"file"`
}

func runOpen(cc *cmdCtx, args []string) int {
	cf := cc.flags("open")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 1, 1, "open"); err != nil {
		return failUsage(cc.stderr, err)
	}
	account, source, client, code := next.start()
	if code != 0 {
		return code
	}
	ctx := context.Background()
	threadID, err := resolveThreadRef(ctx, client, account, pos[0])
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	thread, err := client.GetThread(ctx, threadID, "full")
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	messageID, path, err := render.WriteHTMLBackstop(ctx, thread, client.GetAttachment)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if err := render.OpenURL(path, auth.ScrubbedEnviron(next.cfg)); err != nil {
		return next.runtimeError(account, source, fmt.Errorf("open HTML file: %w", err))
	}
	fmt.Fprintf(next.stderr, "handed to opener: %s\n", path)
	if next.format() != FormatText {
		output := openPayload{Account: account, ThreadID: threadID, MessageID: messageID, File: path}
		if err := next.writeMachine(output); err != nil {
			return next.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	next.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}
