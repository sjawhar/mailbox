package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/refs"
)

func runInbox(cc *cmdCtx, args []string) int {
	fs, accountFlag, jsonOutput := cc.flags("inbox")
	unread := fs.Bool("unread", false, "only unread threads")
	max := fs.Int64("max", 25, "maximum threads (1..500)")
	pos, next, code := cc.parse(fs, accountFlag, jsonOutput, args)
	if code != 0 {
		return code
	}
	if err := requireArity(pos, 0, 0, "inbox"); err != nil {
		return failUsage(cc.stderr, err)
	}
	if *max < 1 || *max > 500 {
		return failUsage(cc.stderr, fmt.Errorf("--max must be in range 1..500"))
	}
	labels := []string{"INBOX"}
	if *unread {
		labels = append(labels, "UNREAD")
	}
	return runListing(next, gmail.ListOptions{LabelIDs: labels, MaxResults: *max})
}

func runSearch(cc *cmdCtx, args []string) int {
	fs, accountFlag, jsonOutput := cc.flags("search")
	max := fs.Int64("max", 25, "maximum threads (1..500)")
	pos, next, code := cc.parse(fs, accountFlag, jsonOutput, args)
	if code != 0 {
		return code
	}
	if err := requireArity(pos, 1, -1, "search"); err != nil {
		return failUsage(cc.stderr, err)
	}
	if *max < 1 || *max > 500 {
		return failUsage(cc.stderr, fmt.Errorf("--max must be in range 1..500"))
	}
	return runListing(next, gmail.ListOptions{Query: strings.Join(pos, " "), MaxResults: *max})
}

func runListing(cc *cmdCtx, options gmail.ListOptions) int {
	account, source, client, code := cc.start()
	if code != 0 {
		return code
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
		ids = ids[:0]
		for _, thread := range metadata {
			ids = append(ids, thread.ID)
		}
		rows = threadRows(metadata, listed.Threads)
	}
	if err := refs.Write(account, ids); err != nil {
		return cc.runtimeError(account, source, err)
	}
	if cc.json {
		if err := writeJSON(cc.stdout, struct {
			Account string      `json:"account"`
			Threads []threadRow `json:"threads"`
		}{Account: string(account), Threads: rows}); err != nil {
			return cc.runtimeError(account, source, wrapError("write JSON", err))
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(cc.stdout, "no threads")
		return 0
	}
	printThreads(cc.stdout, rows, isTerminal(cc.stdout))
	return 0
}

func threadRows(metadata, listed []*gmail.Thread) []threadRow {
	rows := make([]threadRow, 0, len(metadata))
	for index, thread := range metadata {
		row := threadRow{N: index + 1, ID: thread.ID, Snippet: thread.Snippet, Labels: []string{}}
		if index < len(listed) && row.Snippet == "" {
			row.Snippet = listed[index].Snippet
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
