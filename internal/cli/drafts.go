package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/send"
)

type draftsPayload struct {
	Account string     `json:"account"`
	Drafts  []draftRow `json:"drafts"`
}

type draftRow struct {
	DraftID  string `json:"draft_id"`
	ThreadID string `json:"thread_id"`
	To       string `json:"to"`
	Subject  string `json:"subject"`
	Updated  string `json:"updated"`
}

type listedDraft struct {
	row          draftRow
	internalDate int64
}

func runDrafts(cc *cmdCtx, args []string) int {
	cf := cc.flags("drafts")
	max := cf.fs.Int64("max", 25, "maximum drafts (1..500)")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "drafts"); err != nil {
		return next.failUsage(err)
	}
	if *max < 1 || *max > 500 {
		return next.failUsage(fmt.Errorf("--max must be in range 1..500"))
	}

	account, source, client, code := next.start()
	if code != 0 {
		return code
	}
	ctx := context.Background()
	drafts, err := client.ListDrafts(ctx, *max)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	listed := make([]listedDraft, 0, len(drafts))
	for _, draft := range drafts {
		metadata, err := client.GetDraft(ctx, draft.ID, "metadata")
		if err != nil {
			return next.runtimeError(account, source, err)
		}
		listed = append(listed, listedDraft{
			row: draftRow{
				DraftID:  draft.ID,
				ThreadID: metadata.Message.ThreadID,
				To:       draftHeader(metadata.Message, "To"),
				Subject:  draftHeader(metadata.Message, "Subject"),
				Updated:  time.UnixMilli(metadata.Message.InternalDate).UTC().Format(time.RFC3339),
			},
			internalDate: metadata.Message.InternalDate,
		})
	}
	sort.Slice(listed, func(i, j int) bool {
		return listed[i].internalDate > listed[j].internalDate
	})
	rows := make([]draftRow, len(listed))
	for i, draft := range listed {
		rows[i] = draft.row
	}

	switch next.format() {
	case FormatText:
		printDraftRows(next.stdout, rows)
	default:
		if err := next.writeMachine(draftsPayload{Account: account, Drafts: rows}); err != nil {
			return next.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	next.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}

// draftHeader preserves raw CR/LF so text mode can make them visible rather
// than allowing Gmail's header unfolding to erase the hostile input.
func draftHeader(message *gmail.Message, name string) string {
	for _, header := range message.Payload.Headers {
		if strings.EqualFold(header.Name, name) && strings.ContainsAny(header.Value, "\r\n") {
			return header.Value
		}
	}
	return message.Header(name)
}

func printDraftRows(output io.Writer, rows []draftRow) {
	fmt.Fprintln(output, "draft_id\tthread_id\tto\tsubject\tupdated")
	for _, row := range rows {
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n",
			send.VisibleOneLine(row.DraftID),
			send.VisibleOneLine(row.ThreadID),
			send.VisibleOneLine(row.To),
			send.VisibleOneLine(row.Subject),
			send.VisibleOneLine(row.Updated),
		)
	}
}
