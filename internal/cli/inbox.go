package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/filter"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/refs"
)

func runInbox(cc *cmdCtx, args []string) int {
	cf := cc.flags("inbox")
	unread := cf.fs.Bool("unread", false, "only unread threads")
	max := cf.fs.Int64("max", 25, "maximum threads (1..500)")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "inbox"); err != nil {
		return next.failUsage(err)
	}
	if *max < 1 || *max > 500 {
		return next.failUsage(fmt.Errorf("--max must be in range 1..500"))
	}
	labels := []string{"INBOX"}
	if *unread {
		labels = append(labels, "UNREAD")
	}
	return runListing(next, gmail.ListOptions{LabelIDs: labels, MaxResults: *max})
}

func runSearch(cc *cmdCtx, args []string) int {
	cf := cc.flags("search")
	max := cf.fs.Int64("max", 25, "maximum threads (1..500)")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 1, -1, "search"); err != nil {
		return next.failUsage(err)
	}
	if *max < 1 || *max > 500 {
		return next.failUsage(fmt.Errorf("--max must be in range 1..500"))
	}
	return runListing(next, gmail.ListOptions{Query: strings.Join(pos, " "), MaxResults: *max})
}

type listingPayload struct {
	Account string      `json:"account"`
	Filter  string      `json:"filter,omitempty"`
	Threads []threadRow `json:"threads"`
}

func runListing(cc *cmdCtx, options gmail.ListOptions) int {
	account, source, client, code := cc.start()
	if code != 0 {
		return code
	}
	f, err := cc.resolveFilter()
	if err != nil {
		return cc.runtimeError(account, source, err)
	}
	ctx := context.Background()
	listed, err := client.ListThreads(ctx, options)
	if err != nil {
		return cc.runtimeError(account, source, err)
	}
	ids := make([]string, len(listed.Threads))
	for index, thread := range listed.Threads {
		ids[index] = thread.ID
	}
	rows := make([]threadRow, 0, len(ids))
	if len(ids) > 0 {
		metadata, err := client.GetThreadsMetadata(ctx, ids)
		if err != nil {
			return cc.runtimeError(account, source, err)
		}
		if hasInboxLabel(options.LabelIDs) {
			metadata = gmail.FilterThreadsWithLabel(metadata, "INBOX")
		}
		metadata = filter.FilterThreads(f, metadata)
		ids = ids[:0]
		for _, thread := range metadata {
			ids = append(ids, thread.ID)
		}
		rows = threadRows(metadata, listed.Threads)
	}
	if err := refs.Write(account, ids); err != nil {
		return cc.runtimeError(account, source, err)
	}
	switch cc.format() {
	case FormatText:
		if f != nil {
			fmt.Fprintf(cc.stdout, "filter: %s\n", f.Name)
		}
		if len(rows) == 0 {
			fmt.Fprintln(cc.stdout, "no threads")
		} else {
			printThreads(cc.stdout, rows, isTerminal(cc.stdout))
		}
	default:
		if err := cc.writeMachine(listingPayload{Account: string(account), Filter: filterName(f), Threads: rows}); err != nil {
			return cc.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}

func threadRows(metadata, listed []*gmail.Thread) []threadRow {
	rows := make([]threadRow, 0, len(metadata))
	var listedByID map[string]*gmail.Thread
	for index, thread := range metadata {
		row := threadRow{N: index + 1, ID: thread.ID, Snippet: thread.Snippet, Labels: []string{}}
		if row.Snippet == "" {
			if listedByID == nil {
				listedByID = make(map[string]*gmail.Thread, len(listed))
				for _, listedThread := range listed {
					if listedThread != nil {
						listedByID[listedThread.ID] = listedThread
					}
				}
			}
			if listedThread := listedByID[thread.ID]; listedThread != nil {
				row.Snippet = listedThread.Snippet
			}
		}
		if message := gmail.LatestMessage(thread); message != nil {
			row.Subject = message.Header("Subject")
			row.From = message.Header("From")
			row.Date = time.UnixMilli(message.InternalDate).UTC().Format(time.RFC3339)
			row.Unread = message.HasLabel("UNREAD")
			row.Labels = append(row.Labels, message.LabelIDs...)
		}
		rows = append(rows, row)
	}
	return rows
}

func hasInboxLabel(labelIDs []string) bool {
	for _, labelID := range labelIDs {
		if labelID == "INBOX" {
			return true
		}
	}
	return false
}
