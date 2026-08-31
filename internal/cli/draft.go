package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/send"
)

type draftSendOptions struct {
	to, cc, bcc         []string
	subject, body       string
	subjectSet, bodySet bool
	attachPaths         []string
	message             string
	sendNow             bool
}

type draftChangedPayload struct {
	Error struct {
		Code    string               `json:"code"`
		Account string               `json:"account"`
		Message string               `json:"message"`
		Pinned  string               `json:"pinned"`
		Current string               `json:"current"`
		Fresh   send.EnvelopePayload `json:"fresh"`
	} `json:"error"`
}

func runDraftSend(cc *cmdCtx, draftID string, opts draftSendOptions) int {
	ctx := context.Background()
	account, source, client, code := cc.start()
	if code != 0 {
		return code
	}
	fresh, oversized, refusal := send.LoadAttachments(opts.attachPaths)
	if refusal != nil {
		return cc.renderSendRefusal(account, source, refusal)
	}
	draft, err := client.GetDraft(ctx, draftID, "full")
	if err != nil {
		if gmail.IsNotFound(err) {
			return cc.commandError(account, source, auth.ClassRead, "draft_not_found", fmt.Sprintf("draft %s not found (see 'mailbox drafts')", draftID))
		}
		return cc.runtimeError(account, source, err)
	}
	request, threading, carried, refusal, err := reconstructDraft(ctx, client, draft)
	if err != nil {
		return cc.runtimeError(account, source, err)
	}
	if refusal != nil {
		return cc.renderSendRefusal(account, source, refusal)
	}
	applyDraftOverrides(&request, opts)
	envelope, refusal := send.ResolveDraft(request, threading)
	if refusal != nil {
		return cc.renderSendRefusal(account, source, refusal)
	}
	envelope.Attachments = carried
	if oversized != nil {
		if refusal := send.CanonicalizeAttachments(envelope.Attachments); refusal != nil {
			return cc.renderSendRefusal(account, source, refusal)
		}
		if refusal := send.CanonicalizeAttachmentMeta(oversized, len(envelope.Attachments)); refusal != nil {
			return cc.renderSendRefusal(account, source, refusal)
		}
		sizeRefusal, sizeErr := send.OversizeRefusal(envelope, nil, oversized)
		if sizeErr != nil {
			return cc.runtimeError(account, source, sizeErr)
		}
		return cc.renderSendRefusal(account, source, sizeRefusal)
	}
	envelope.Attachments = append(envelope.Attachments, fresh...)
	if refusal := send.CanonicalizeAttachments(envelope.Attachments); refusal != nil {
		return cc.renderSendRefusal(account, source, refusal)
	}
	envelope.TargetMessageID = draft.Message.ID
	return cc.finishDraftSend(ctx, account, source, envelope, draftID, opts)
}

func reconstructDraft(ctx context.Context, client *gmail.Client, draft *gmail.Draft) (send.Request, send.DraftThreading, []send.Attachment, *send.Refusal, error) {
	if draft == nil || draft.Message == nil {
		return send.Request{}, send.DraftThreading{}, nil, nil, errors.New("gmail: draft response omitted its message")
	}
	content, err := render.ExtractContent(draft.Message)
	if err != nil {
		return send.Request{}, send.DraftThreading{}, nil, nil, err
	}

	body := content.Text
	if body == "" {
		rendered, renderErr := render.RenderBody(content, render.Options{}, 1)
		if renderErr != nil {
			return send.Request{}, send.DraftThreading{}, nil, nil, renderErr
		}
		body = rendered.Markdown
	}
	request := send.Request{
		To:      draftRecipients(draft.Message, "To"),
		Cc:      draftRecipients(draft.Message, "Cc"),
		Bcc:     draftRecipients(draft.Message, "Bcc"),
		Subject: draftHeader(draft.Message, "Subject"),
		Body:    body,
	}
	threading := send.DraftThreading{
		ThreadID:   draft.Message.ThreadID,
		InReplyTo:  draftHeader(draft.Message, "In-Reply-To"),
		References: draftHeader(draft.Message, "References"),
	}
	carried := make([]send.Attachment, 0, len(content.Attachments))
	for index, attachment := range content.Attachments {
		contents, attachmentErr := render.ResolveAttachmentBytes(ctx, client, attachment)
		if attachmentErr != nil {
			return send.Request{}, send.DraftThreading{}, nil, nil, attachmentErr
		}
		carriedAttachment, attachmentRefusal := send.NewCarriedAttachment(attachment.Filename, index, contents)
		if attachmentRefusal != nil {
			return send.Request{}, send.DraftThreading{}, nil, attachmentRefusal, nil
		}
		carried = append(carried, carriedAttachment)
	}
	return request, threading, carried, nil, nil
}

func draftRecipients(message *gmail.Message, name string) []string {
	value := draftHeader(message, name)
	if value == "" {
		return nil
	}
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return []string{value}
	}
	out := make([]string, len(addresses))
	for index, address := range addresses {
		out[index] = address.String()
	}
	return out
}

func applyDraftOverrides(request *send.Request, opts draftSendOptions) {
	if opts.to != nil {
		request.To = opts.to
	}
	if opts.cc != nil {
		request.Cc = opts.cc
	}
	if opts.bcc != nil {
		request.Bcc = opts.bcc
	}
	if opts.subjectSet {
		request.Subject = opts.subject
	}
	if opts.bodySet {
		request.Body = opts.body
	}
}

func (cc *cmdCtx) finishDraftSend(ctx context.Context, account string, source *auth.Source, envelope *send.Envelope, draftID string, opts draftSendOptions) int {
	outbound, err := send.BuildMIME(envelope, nil, "")
	if err != nil {
		return cc.runtimeError(account, source, err)
	}
	if refusal := send.OutboundSizeRefusal(outbound, envelope.Attachments); refusal != nil {
		return cc.renderSendRefusal(account, source, refusal)
	}
	if !opts.sendNow {
		return cc.renderDraftDryRun(account, source, envelope, draftID)
	}
	if opts.message != envelope.TargetMessageID {
		return cc.renderDraftChanged(account, source, envelope, draftID, opts.message, envelope.TargetMessageID)
	}
	writeClient, code := cc.acquireWrite(source)
	if code != 0 {
		return code
	}
	_, sendSource, sendClient, code := cc.startSend()
	if code != 0 {
		return code
	}
	var sent *gmail.SentMessage
	sendErr, knownNotAccepted := cc.retryDraftSend(sendSource, func() error {
		var innerErr error
		sent, innerErr = sendClient.SendMessage(ctx, outbound, envelope.ThreadID)
		return innerErr
	})
	if sendErr != nil {
		if knownNotAccepted {
			return cc.sendRuntimeError(account, sendSource, sendErr)
		}
		if gmail.IsStillUnauthorized(sendErr) {
			return cc.needsCredential(&auth.NeedsCredentialError{
				Account:    account,
				Class:      auth.ClassSend,
				ConfigKey:  cc.acct.Send.ConfigKey,
				ConfigPath: cc.cfg.Path,
				Reason:     auth.ReasonRejected,
			})
		}
		var apiErr *gmail.APIError
		if errors.As(sendErr, &apiErr) && apiErr.Status >= http.StatusBadRequest && apiErr.Status < http.StatusInternalServerError {
			return cc.sendRuntimeError(account, sendSource, sendErr)
		}
		return cc.commandError(account, sendSource, auth.ClassSend, "draft_send_unknown",
			fmt.Sprintf("send outcome unknown for draft %s: %v — the draft is intact; verify before retrying", draftID, sendErr))
	}
	warning := ""
	if err := cc.retryWrite(source, func() error { return writeClient.DeleteDraft(ctx, draftID) }); err != nil {
		warning = fmt.Sprintf("sent, but deleting draft %s failed: %v — delete it manually", draftID, err)
		fmt.Fprintf(cc.stderr, "warning: %s\n", send.VisibleOneLine(warning))
	}
	return cc.renderDraftSent(account, sendSource, envelope, draftID, sent, warning)
}

// retryDraftSend re-acquires a send token only after Gmail's explicit 401
// rejection. That rejection proves non-acceptance, so one retry is safe; a
// second 401 is concrete credential rejection, while all other send failures
// remain indeterminate.
func (cc *cmdCtx) retryDraftSend(source *auth.Source, action func() error) (error, bool) {
	err := action()
	if !errors.Is(err, auth.ErrExpiredSendToken) {
		return err, false
	}
	source.InvalidateSend()
	if _, err = source.SendToken(context.Background(), auth.BatchAcquirer(cc.cfg, cc.acct, auth.ClassSend)); err != nil {
		return err, true
	}
	if err := action(); errors.Is(err, auth.ErrExpiredSendToken) {
		return gmail.ErrStillUnauthorized, false
	} else {
		return err, false
	}
}

func (cc *cmdCtx) renderDraftDryRun(account string, source *auth.Source, envelope *send.Envelope, draftID string) int {
	if cc.format() == FormatText {
		renderDraftPreview(cc.stdout, account, envelope, draftID)
	} else {
		payload := send.Payload(account, envelope, 0)
		payload.DraftID = draftID
		if err := cc.writeMachine(payload); err != nil {
			return cc.runtimeError(account, source, wrapError("write draft preview", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}

func (cc *cmdCtx) renderDraftChanged(account string, source *auth.Source, envelope *send.Envelope, draftID, pinned, current string) int {
	if cc.format() == FormatText {
		fmt.Fprintf(cc.stderr, "(draft_changed) draft %s changed since the dry-run: pinned %s, current %s\n",
			send.VisibleOneLine(draftID), send.VisibleOneLine(pinned), send.VisibleOneLine(current))
		renderDraftPreview(cc.stdout, account, envelope, draftID)
	} else {
		payload := draftChangedPayload{}
		payload.Error.Code = "draft_changed"
		payload.Error.Account = account
		payload.Error.Message = fmt.Sprintf("draft %s changed since the dry-run; run a fresh preview before sending", draftID)
		payload.Error.Pinned = pinned
		payload.Error.Current = current
		payload.Error.Fresh = send.Payload(account, envelope, 0)
		payload.Error.Fresh.DraftID = draftID
		if err := cc.writeMachine(payload); err != nil {
			return cc.runtimeError(account, source, wrapError("write draft changed", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 1
}

func (cc *cmdCtx) renderDraftSent(account string, source *auth.Source, envelope *send.Envelope, draftID string, sent *gmail.SentMessage, warning string) int {
	if cc.format() == FormatText {
		renderDraftPreview(cc.stdout, account, envelope, draftID)
	} else {
		payload := send.Payload(account, envelope, 0)
		payload.DraftID = draftID
		payload.Sent = &send.SentPayload{ID: sent.ID, ThreadID: sent.ThreadID}
		payload.Warning = warning
		if err := cc.writeMachine(payload); err != nil {
			return cc.sendRuntimeError(account, source, wrapError("write draft send result", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassSend)
	return 0
}

func renderDraftPreview(output interface{ Write([]byte) (int, error) }, account string, envelope *send.Envelope, draftID string) {
	send.RenderText(output, account, envelope, 0)
	fmt.Fprintf(output, "draft: %s\n", send.VisibleOneLine(draftID))
}
