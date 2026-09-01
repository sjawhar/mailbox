package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/send"
)

func runSend(cc *cmdCtx, args []string) int {
	cf := cc.flags("send")
	var (
		to, carbonCopy, blindCarbonCopy                                 []string
		attachPaths                                                     []string
		subject, body, reply, forward, message, draft                   string
		subjectSet, bodySet, replySet, forwardSet, messageSet, draftSet bool
	)
	cf.fs.Func("to", "recipient", func(value string) error {
		to = append(to, value)
		return nil
	})
	cf.fs.Func("cc", "recipient", func(value string) error {
		carbonCopy = append(carbonCopy, value)
		return nil
	})
	cf.fs.Func("bcc", "recipient", func(value string) error {
		blindCarbonCopy = append(blindCarbonCopy, value)
		return nil
	})
	cf.fs.Func("subject", "subject", func(value string) error {
		subject, subjectSet = value, true
		return nil
	})
	cf.fs.Func("body", "message body", func(value string) error {
		body, bodySet = value, true
		return nil
	})
	var bodyFile string
	cf.fs.Func("body-file", "read the body from a file (- for stdin)", func(value string) error {
		bodyFile = value
		return nil
	})
	cf.fs.Func("reply", "thread id", func(value string) error {
		reply, replySet = value, true
		return nil
	})
	cf.fs.Func("forward", "thread id", func(value string) error {
		forward, forwardSet = value, true
		return nil
	})
	cf.fs.Func("draft", "draft id", func(value string) error {
		draft, draftSet = value, true
		return nil
	})
	cf.fs.Func("message", "message id", func(value string) error {
		message, messageSet = value, true
		return nil
	})
	cf.fs.Func("attach", "file path (repeatable)", func(value string) error {
		attachPaths = append(attachPaths, value)
		return nil
	})
	sendNow := cf.fs.Bool("send", false, "transmit the resolved envelope")
	saveDraft := cf.fs.Bool("save-draft", false, "resolve fully, then store a Gmail draft instead of transmitting")

	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "send"); err != nil {
		return next.failUsage(err)
	}
	if replySet && forwardSet {
		return next.failUsage(fmt.Errorf("--reply and --forward are mutually exclusive"))
	}
	if draftSet && replySet {
		return next.failUsage(fmt.Errorf("--draft and --reply are mutually exclusive"))
	}
	if draftSet && forwardSet {
		return next.failUsage(fmt.Errorf("--draft and --forward are mutually exclusive"))
	}
	if *saveDraft && *sendNow {
		return next.failUsage(fmt.Errorf("--save-draft and --send are mutually exclusive"))
	}
	if draftSet && *saveDraft {
		return next.failUsage(fmt.Errorf("--draft and --save-draft are mutually exclusive"))
	}

	mode, threadRef := send.ModeCompose, ""
	switch {
	case replySet:
		mode, threadRef = send.ModeReply, reply
	case forwardSet:
		mode, threadRef = send.ModeForward, forward
	}
	if mode == send.ModeCompose && !subjectSet && !draftSet {
		return next.failUsage(fmt.Errorf("compose requires --subject"))
	}
	if mode != send.ModeCompose && subjectSet {
		return next.failUsage(fmt.Errorf("--subject is only valid for compose"))
	}
	if bodySet && bodyFile != "" {
		return next.failUsage(fmt.Errorf("--body and --body-file are mutually exclusive"))
	}
	if !bodySet && bodyFile == "" && !draftSet {
		return next.failUsage(fmt.Errorf("send requires --body or --body-file"))
	}
	if mode == send.ModeCompose && messageSet && !draftSet {
		return next.failUsage(fmt.Errorf("--message is only valid with --reply or --forward"))
	}
	if *sendNow && mode != send.ModeCompose && (!messageSet || message == "") {
		return next.failUsage(fmt.Errorf("--send requires --message=<id> on reply/forward: run the dry-run first and copy the message id it prints (target pinning)"))
	}
	if *sendNow && draftSet && (!messageSet || message == "") {
		return next.failUsage(fmt.Errorf("--send requires --message=<id> on --draft: run the dry-run first and copy the current message id it prints (draft pinning)"))
	}
	switch {
	case body == "-" && bodySet, bodyFile == "-":
		data, err := io.ReadAll(next.stdin)
		if err != nil {
			return next.runtimeError("", nil, wrapError("read body from stdin", err))
		}
		body = string(data)
	case bodyFile != "":
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			return next.runtimeError("", nil, wrapError("read --body-file", err))
		}
		body = string(data)
		bodySet = true
	}
	if draftSet {
		return runDraftSend(next, draft, draftSendOptions{
			to:          to,
			cc:          carbonCopy,
			bcc:         blindCarbonCopy,
			subject:     subject,
			body:        body,
			subjectSet:  subjectSet,
			bodySet:     bodySet,
			attachPaths: attachPaths,
			message:     message,
			sendNow:     *sendNow,
		})
	}

	ctx := context.Background()
	account, source, client, code := next.start()
	if code != 0 {
		return code
	}
	attachments, oversized, refusal := send.LoadAttachments(attachPaths)
	if refusal != nil {
		return next.renderSendRefusal(account, source, refusal)
	}
	profile, err := client.GetProfile(ctx)
	if err != nil {
		return next.runtimeError(account, source, err)
	}

	var (
		threadID string
		target   *gmail.Message
	)
	if mode != send.ModeCompose {
		threadID, err = resolveThreadRef(ctx, client, account, threadRef)
		if err != nil {
			return next.runtimeError(account, source, err)
		}
		if messageSet {
			target, err = client.GetMessage(ctx, message)
			if err != nil {
				return next.runtimeError(account, source, err)
			}
			if target.ThreadID != threadID {
				return next.renderSendRefusal(account, source, send.NotInThreadRefusal(message, threadID))
			}
		} else {
			thread, err := client.GetThread(ctx, threadID, "metadata")
			if err != nil {
				return next.runtimeError(account, source, err)
			}
			newest := gmail.LatestMessage(thread)
			if newest == nil {
				return next.runtimeError(account, source, fmt.Errorf("thread %s contains no messages", threadID))
			}
			target, err = client.GetMessage(ctx, newest.ID)
			if err != nil {
				return next.runtimeError(account, source, err)
			}
		}
	}

	request := send.Request{
		Mode:    mode,
		To:      to,
		Cc:      carbonCopy,
		Bcc:     blindCarbonCopy,
		Subject: subject,
		Body:    body,
		Self:    profile.EmailAddress,
	}
	if target != nil {
		request.Target = send.NewTargetHeaders(target)
	}
	envelope, refusal := send.Resolve(request)
	if refusal != nil {
		return next.renderSendRefusal(account, source, refusal)
	}
	if mode == send.ModeReply {
		envelope.ThreadID = threadID
		envelope.TargetMessageID = target.ID
	} else if mode == send.ModeForward {
		envelope.TargetMessageID = target.ID
	}

	var original []byte
	if mode == send.ModeForward {
		raw, err := client.GetMessageRaw(ctx, target.ID)
		if err != nil {
			return next.runtimeError(account, source, err)
		}
		original, err = raw.RawBytes()
		if err != nil {
			return next.runtimeError(account, source, err)
		}
	}
	if oversized != nil {
		sizeRefusal, err := send.OversizeRefusal(envelope, original, oversized)
		if err != nil {
			return next.runtimeError(account, source, err)
		}
		return next.renderSendRefusal(account, source, sizeRefusal)
	}
	envelope.Attachments = attachments
	outbound, refusal, err := send.Finalize(envelope, original, "")
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if refusal != nil {
		return next.renderSendRefusal(account, source, refusal)
	}
	if *saveDraft {
		writeClient, code := next.acquireWrite(source)
		if code != 0 {
			return code
		}
		draftThreadID := envelope.ThreadID
		if mode == send.ModeForward {
			draftThreadID = threadID
		}
		var draft *gmail.Draft
		if err := next.retryWrite(source, func() error {
			var createErr error
			draft, createErr = writeClient.CreateDraft(ctx, outbound, draftThreadID)
			return createErr
		}); err != nil {
			return next.writeRuntimeError(account, source, err)
		}
		return next.renderDraftSaved(account, source, envelope, len(original), draft.ID)
	}
	if !*sendNow {
		return next.renderSendResult(account, source, auth.ClassRead, envelope, len(original), nil, "", "")
	}

	_, sendSource, sendClient, code := next.startSend()
	if code != 0 {
		return code
	}
	var sent *gmail.SentMessage
	if err := next.retrySend(sendSource, func() error {
		var sendErr error
		sent, sendErr = sendClient.SendMessage(ctx, outbound, envelope.ThreadID)
		return sendErr
	}); err != nil {
		return next.sendRuntimeError(account, sendSource, err)
	}
	scope := sendSource.SendScope()
	warning := auth.SendScopeWarning(scope, next.acct.Send.ConfigKey)
	return next.renderSendResult(account, sendSource, auth.ClassSend, envelope, len(original), sent, scope, warning)
}

func (cc *cmdCtx) renderSendRefusal(account string, source *auth.Source, refusal *send.Refusal) int {
	if cc.format() == FormatText {
		send.RenderRefusalText(cc.stderr, refusal)
	} else if err := cc.writeMachine(send.RefusalOf(account, refusal)); err != nil {
		return cc.runtimeError(account, source, wrapError("write send refusal", err))
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 1
}

func (cc *cmdCtx) renderSendResult(account string, source *auth.Source, class auth.Class, envelope *send.Envelope, forwardBytes int, sent *gmail.SentMessage, scope, warning string) int {
	if cc.format() == FormatText {
		send.RenderText(cc.stdout, account, envelope, forwardBytes)
		if warning != "" {
			fmt.Fprintf(cc.stdout, "warning: %s\n", send.VisibleOneLine(warning))
		}
	} else {
		payload := send.Payload(account, envelope, forwardBytes)
		if sent != nil {
			payload.Sent = &send.SentPayload{ID: sent.ID, ThreadID: sent.ThreadID}
			payload.Scope = scope
			payload.Warning = warning
		}
		if err := cc.writeMachine(payload); err != nil {
			return cc.runtimeErrorForClass(account, source, wrapError("write send result", err), class)
		}
	}
	cc.emitCredentialDiagnostic(source, class)
	return 0
}

func (cc *cmdCtx) renderDraftSaved(account string, source *auth.Source, envelope *send.Envelope, forwardBytes int, draftID string) int {
	if cc.format() == FormatText {
		send.RenderText(cc.stdout, account, envelope, forwardBytes)
		fmt.Fprintf(cc.stdout, "draft: %s\n", send.VisibleOneLine(draftID))
	} else {
		payload := send.Payload(account, envelope, forwardBytes)
		payload.DraftID = draftID
		if err := cc.writeMachine(payload); err != nil {
			return cc.writeRuntimeError(account, source, wrapError("write draft result", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassWrite)
	return 0
}
