package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

func runBulk(cc *cmdCtx, action string, args []string) int {
	cf := cc.flags(action)
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if next.filterFlag != "" {
		if len(pos) > 0 {
			return next.failUsage(fmt.Errorf("--filter and thread ids are mutually exclusive"))
		}
		account, source, client, f, code := next.startBulkFilter()
		if code != 0 {
			return code
		}
		if action == "archive" {
			return runBulkFilter(next, account, source, client, action, "archived", f, nil, []string{"INBOX"}, false)
		}
		return runBulkFilter(next, account, source, client, action, "trashed", f, nil, nil, true)
	}
	if err := requireArity(pos, 1, -1, action); err != nil {
		return next.failUsage(err)
	}
	account, source, client, code := next.startWrite()
	if code != 0 {
		return code
	}
	ctx := context.Background()
	ids, err := resolveThreadRefs(ctx, client, account, pos)
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	if action == "archive" {
		err = next.retryWrite(source, func() error {
			return client.ModifyThreads(ctx, ids, nil, []string{"INBOX"})
		})
	} else {
		err = next.retryWrite(source, func() error {
			return client.TrashThreads(ctx, ids)
		})
	}
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	verb := "trashed"
	if action == "archive" {
		verb = "archived"
	}
	return next.actionResult(account, source, action, verb, ids)
}

func runMark(cc *cmdCtx, args []string) int {
	cf := cc.flags("mark")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if next.filterFlag != "" {
		if len(pos) > 1 {
			return next.failUsage(fmt.Errorf("--filter and thread ids are mutually exclusive"))
		}
		if err := requireArity(pos, 1, 1, "mark"); err != nil {
			return next.failUsage(err)
		}
		mode := pos[0]
		if mode != "read" && mode != "unread" {
			return next.failUsage(fmt.Errorf("mark mode must be read or unread"))
		}
		var add, remove []string
		if mode == "read" {
			remove = []string{"UNREAD"}
		} else {
			add = []string{"UNREAD"}
		}
		account, source, client, f, code := next.startBulkFilter()
		if code != 0 {
			return code
		}
		return runBulkFilter(next, account, source, client, "mark", "marked "+mode, f, add, remove, false)
	}
	if err := requireArity(pos, 2, -1, "mark"); err != nil {
		return next.failUsage(err)
	}
	mode := pos[0]
	if mode != "read" && mode != "unread" {
		return next.failUsage(fmt.Errorf("mark mode must be read or unread"))
	}
	account, source, client, code := next.startWrite()
	if code != 0 {
		return code
	}
	ids, err := resolveThreadRefs(context.Background(), client, account, pos[1:])
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	var add, remove []string
	if mode == "read" {
		remove = []string{"UNREAD"}
	} else {
		add = []string{"UNREAD"}
	}
	if err := next.retryWrite(source, func() error {
		return client.ModifyThreads(context.Background(), ids, add, remove)
	}); err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	return next.actionResult(account, source, "mark", "marked "+mode, ids)
}

func runLabel(cc *cmdCtx, args []string) int {
	cf := cc.flags("label")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if next.filterFlag != "" {
		if len(pos) > 2 {
			return next.failUsage(fmt.Errorf("--filter and thread ids are mutually exclusive"))
		}
		if err := requireArity(pos, 2, 2, "label"); err != nil {
			return next.failUsage(err)
		}
		mode := pos[0]
		if mode != "add" && mode != "rm" {
			return next.failUsage(fmt.Errorf("label mode must be add or rm"))
		}
		account, source, client, f, code := next.startBulkFilter()
		if code != 0 {
			return code
		}
		label, err := resolveLabel(context.Background(), client, pos[1])
		if err != nil {
			return next.writeRuntimeError(account, source, err)
		}
		var add, remove []string
		if mode == "add" {
			add = []string{label.ID}
		} else {
			remove = []string{label.ID}
		}
		verb := "labeled"
		if mode == "rm" {
			verb = "unlabeled"
		}
		return runBulkFilter(next, account, source, client, "label", verb, f, add, remove, false)
	}
	if err := requireArity(pos, 3, -1, "label"); err != nil {
		return next.failUsage(err)
	}
	mode := pos[0]
	if mode != "add" && mode != "rm" {
		return next.failUsage(fmt.Errorf("label mode must be add or rm"))
	}
	account, source, client, code := next.startWrite()
	if code != 0 {
		return code
	}
	label, err := resolveLabel(context.Background(), client, pos[1])
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	ids, err := resolveThreadRefs(context.Background(), client, account, pos[2:])
	if err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	var add, remove []string
	if mode == "add" {
		add = []string{label.ID}
	} else {
		remove = []string{label.ID}
	}
	if err := next.retryWrite(source, func() error {
		return client.ModifyThreads(context.Background(), ids, add, remove)
	}); err != nil {
		return next.writeRuntimeError(account, source, err)
	}
	verb := "labeled"
	if mode == "rm" {
		verb = "unlabeled"
	}
	return next.actionResult(account, source, "label", verb, ids)
}

type actionPayload struct {
	Account   string   `json:"account"`
	Action    string   `json:"action"`
	ThreadIDs []string `json:"threadIds"`
	OK        bool     `json:"ok"`
}

func (cc *cmdCtx) actionResult(account string, source *auth.Source, action, verb string, ids []string) int {
	switch cc.format() {
	case FormatText:
		fmt.Fprintf(cc.stdout, "%s %d thread(s)\n", verb, len(ids))
	default:
		output := actionPayload{Account: account, Action: action, ThreadIDs: ids, OK: true}
		if err := cc.writeMachine(output); err != nil {
			return cc.writeRuntimeError(account, source, wrapError("write JSON", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassWrite)
	return 0
}

func resolveLabel(ctx context.Context, client *gmail.Client, name string) (gmail.Label, error) {
	labels, err := client.ListLabels(ctx)
	if err != nil {
		return gmail.Label{}, err
	}
	for _, label := range labels {
		if label.Name == name || label.ID == name {
			return label, nil
		}
	}
	matches := make([]gmail.Label, 0, len(labels))
	for _, label := range labels {
		if strings.EqualFold(label.Name, name) || strings.EqualFold(label.ID, name) {
			matches = append(matches, label)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return gmail.Label{}, fmt.Errorf("no label %q; available labels: %s", name, labelNames(labels))
	default:
		return gmail.Label{}, fmt.Errorf("ambiguous label %q; candidates: %s", name, labelNames(matches))
	}
}

func labelNames(labels []gmail.Label) string {
	names := make([]string, len(labels))
	for index, label := range labels {
		names[index] = render.SanitizeTerminal(label.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
