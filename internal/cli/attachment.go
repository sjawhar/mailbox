package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/render"
)

func runAttachment(cc *cmdCtx, args []string) int {
	cf := cc.flags("attachment")
	outputPath := cf.fs.String("o", "", "output file or directory")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 1, 2, "attachment"); err != nil {
		return next.failUsage(err)
	}
	account, source, client, code := next.start()
	if code != 0 {
		return code
	}
	threadID, err := resolveThreadRef(context.Background(), client, account, pos[0])
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	thread, err := client.GetThread(context.Background(), threadID, "full")
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	attachments, err := render.ThreadAttachments(thread)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if len(pos) == 1 {
		return next.attachmentList(account, source, threadID, attachments)
	}
	n, err := strconv.Atoi(pos[1])
	if err != nil || n < 1 {
		return next.failUsage(fmt.Errorf("attachment number must be a positive integer"))
	}
	if n > len(attachments) {
		return next.runtimeError(account, source, fmt.Errorf("attachment %d out of range: thread has %d attachments", n, len(attachments)))
	}
	attachment := attachments[n-1]
	contents, err := client.GetAttachment(context.Background(), attachment.MessageID, attachment.AttachmentID)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	path, overwrite, err := render.AttachmentDestination(*outputPath, attachment.Filename)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if err := render.WriteAttachment(path, contents, overwrite); err != nil {
		if !overwrite && os.IsExist(err) {
			return next.runtimeError(account, source, fmt.Errorf("%s exists — pass -o to choose a destination (overwrites)", render.SanitizeTerminal(attachment.Filename)))
		}
		return next.runtimeError(account, source, fmt.Errorf("write attachment %q: %w", path, err))
	}
	switch next.format() {
	case FormatText:
		fmt.Fprintf(next.stdout, "saved %s\n", render.SanitizeTerminal(path))
	default:
		output := attachmentSavePayload{Account: account, File: path, Filename: attachment.Filename, Size: attachment.Size}
		if err := next.writeMachine(output); err != nil {
			return next.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	next.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}

type attachmentSavePayload struct {
	Account  string `json:"account"`
	File     string `json:"file"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type attachmentListPayload struct {
	Account     string              `json:"account"`
	ThreadID    string              `json:"threadId"`
	Attachments []render.Attachment `json:"attachments"`
}

func (cc *cmdCtx) attachmentList(account string, source *auth.Source, threadID string, attachments []render.Attachment) int {
	switch cc.format() {
	case FormatText:
		fmt.Fprintln(cc.stdout, "n\tfilename\tmime\tsize")
		for _, attachment := range attachments {
			fmt.Fprintf(cc.stdout, "%d\t%s\t%s\t%d\n", attachment.N, render.SanitizeTerminal(attachment.Filename), render.SanitizeTerminal(attachment.MimeType), attachment.Size)
		}
	default:
		attachments = normalizeAttachments(attachments)
		output := attachmentListPayload{Account: account, ThreadID: threadID, Attachments: attachments}
		if err := cc.writeMachine(output); err != nil {
			return cc.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}
