package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/filter"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

// resolveFilter maps --filter to a compiled config filter. Callers reach it
// only after loadConfig has succeeded (via start, startWrite, or loadConfig).
func (cc *cmdCtx) resolveFilter() (*filter.Filter, error) {
	if cc.filterFlag == "" {
		return nil, nil
	}
	if f, ok := cc.cfg.Filter(cc.filterFlag); ok {
		return f, nil
	}
	if names := cc.cfg.FilterNames(); len(names) > 0 {
		return nil, fmt.Errorf("unknown filter %q; defined filters: %s", cc.filterFlag, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("unknown filter %q; no filters are defined (config: %s)", cc.filterFlag, cc.cfg.DisplayPath())
}

func filterName(f *filter.Filter) string {
	if f == nil {
		return ""
	}
	return f.Name
}

// startBulkFilter resolves the requested filter before spending the write
// credential. Keeping this order centralized prevents unknown filter names
// from invoking a write helper.
func (cc *cmdCtx) startBulkFilter() (string, *auth.Source, *gmail.Client, *filter.Filter, int) {
	if err := cc.loadConfig(); err != nil {
		return "", nil, nil, nil, cc.runtimeError("", nil, err)
	}
	f, err := cc.resolveFilter()
	if err != nil {
		return "", nil, nil, nil, cc.runtimeError("", nil, err)
	}
	account, source, client, code := cc.startWrite()
	if code != 0 {
		return "", nil, nil, nil, code
	}
	return account, source, client, f, 0
}

// collectFilteredInbox completes one ordered inbox traversal and hydrates its
// deduplicated selection before any mutation is sent.
func collectFilteredInbox(ctx context.Context, client *gmail.Client, f *filter.Filter) ([]string, error) {
	var ids []string
	seen := make(map[string]struct{})
	token := ""
	for {
		listed, err := client.ListThreads(ctx, gmail.ListOptions{
			LabelIDs:   []string{"INBOX"},
			MaxResults: 500,
			PageToken:  token,
		})
		if err != nil {
			return nil, wrapError("list inbox page", err)
		}
		for _, thread := range listed.Threads {
			if _, duplicate := seen[thread.ID]; duplicate {
				continue
			}
			seen[thread.ID] = struct{}{}
			ids = append(ids, thread.ID)
		}
		if listed.NextPageToken == "" {
			break
		}
		token = listed.NextPageToken
	}
	if len(ids) == 0 {
		return nil, nil
	}
	metadata, err := client.GetThreadsMetadata(ctx, ids)
	if err != nil {
		return nil, wrapError("hydrate inbox threads", err)
	}
	metadata = gmail.FilterThreadsWithLabel(metadata, "INBOX")
	matched := make([]string, 0, len(metadata))
	for _, thread := range metadata {
		if filter.MatchesThread(f, thread) {
			matched = append(matched, thread.ID)
		}
	}
	return matched, nil
}

type filterActionPayload struct {
	Account   string                `json:"account"`
	Action    string                `json:"action"`
	Filter    string                `json:"filter"`
	Matched   int                   `json:"matched"`
	Attempted int                   `json:"attempted"`
	Succeeded []string              `json:"succeeded"`
	Failed    []filterActionFailure `json:"failed"`
	OK        bool                  `json:"ok"`
}

type filterActionFailure struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
}

func runBulkFilter(next *cmdCtx, account string, source *auth.Source, client *gmail.Client, action, verb string, f *filter.Filter, add, remove []string, trash bool) int {
	ctx := context.Background()
	matched, err := collectFilteredInbox(ctx, client, f)
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	var receipts gmail.WriteReceipts
	if len(matched) > 0 {
		writeErr := next.retryWrite(source, func() error {
			var callErr error
			if trash {
				receipts, callErr = client.TrashThreadsReceipts(ctx, matched)
			} else {
				receipts, callErr = client.ModifyThreadsReceipts(ctx, matched, add, remove)
			}
			return callErr
		})
		return next.renderBulkFilter(account, source, action, verb, f.Name, matched, receipts, writeErr)
	}
	return next.renderBulkFilter(account, source, action, verb, f.Name, matched, receipts, nil)
}

func (cc *cmdCtx) renderBulkFilter(account string, source *auth.Source, action, verb, filterName string, matched []string, receipts gmail.WriteReceipts, writeErr error) int {
	failed := make([]filterActionFailure, len(receipts.Failed))
	for index, receipt := range receipts.Failed {
		failed[index] = filterActionFailure{ID: receipt.ID, Status: receipt.Status, Reason: receipt.Reason}
	}
	output := filterActionPayload{
		Account:   account,
		Action:    action,
		Filter:    filterName,
		Matched:   len(matched),
		Succeeded: normalizeStrings(receipts.Succeeded),
		Failed:    normalizeFailures(failed),
	}
	output.Attempted = len(output.Succeeded) + len(output.Failed)
	output.OK = len(output.Failed) == 0 && output.Attempted == output.Matched

	if cc.format() == FormatText {
		if output.Matched == 0 {
			fmt.Fprintf(cc.stdout, "matched 0 thread(s) (filter: %s)\n", render.SanitizeTerminal(output.Filter))
		} else {
			fmt.Fprintf(cc.stdout, "%s %d of %d matched thread(s) (filter: %s)\n", render.SanitizeTerminal(verb), len(output.Succeeded), output.Matched, render.SanitizeTerminal(output.Filter))
		}
		if len(output.Failed) > 0 {
			fmt.Fprintf(cc.stdout, "failed: %s\n", formatFilterFailures(output.Failed))
		}
	} else if err := cc.writeMachine(output); err != nil {
		fmt.Fprintf(cc.stderr, "mailbox: write machine output: %s\n", render.SanitizeTerminal(err.Error()))
		return 1
	}

	scopeErr := writeErr
	if !gmail.IsInsufficientScope(scopeErr) {
		for _, receipt := range receipts.Failed {
			if gmail.IsInsufficientScope(receipt.Err) {
				scopeErr = receipt.Err
				break
			}
		}
	}

	cc.emitCredentialDiagnostic(source, auth.ClassWrite)
	if writeErr != nil {
		fmt.Fprintf(cc.stderr, "mailbox: %s\n", render.SanitizeTerminal(writeErr.Error()))
		cc.emitScopeHint(source, scopeErr, auth.ClassWrite)
		return 1
	}
	if len(output.Failed) > 0 {
		fmt.Fprintf(cc.stderr, "mailbox: %s\n", formatFilterFailures(output.Failed))
		cc.emitScopeHint(source, scopeErr, auth.ClassWrite)
		return 1
	}
	return 0
}

func formatFilterFailures(failures []filterActionFailure) string {
	details := make([]string, len(failures))
	for index, failure := range failures {
		detail := fmt.Sprintf("%s (%d", render.SanitizeTerminal(failure.ID), failure.Status)
		if reason := render.SanitizeTerminal(failure.Reason); reason != "" {
			detail += " " + reason
		}
		details[index] = detail + ")"
	}
	return strings.Join(details, ", ")
}
