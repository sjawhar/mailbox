package cli

import (
	"context"
	"io"
	"os"

	"github.com/sjawhar/mailbox/internal/render"
	"golang.org/x/term"
)

func runRead(cc *cmdCtx, args []string) int {
	fs, accountFlag, jsonOutput := cc.flags("read")
	full := fs.Bool("full", false, "keep quoted history")
	pos, next, code := cc.parse(fs, accountFlag, jsonOutput, args)
	if code != 0 {
		return code
	}
	if err := requireArity(pos, 1, 1, "read"); err != nil {
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
	rendered, err := render.RenderThread(thread, render.Options{KeepQuotes: *full})
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if next.json {
		normalizeRenderedThread(rendered)
		output := struct {
			Account string `json:"account"`
			*render.RenderedThread
		}{Account: string(account), RenderedThread: rendered}
		if err := writeJSON(next.stdout, output); err != nil {
			return next.runtimeError(account, source, wrapError("write JSON", err))
		}
		return 0
	}
	markdown := rendered.Markdown()
	if !isTerminal(next.stdout) {
		if _, err := io.WriteString(next.stdout, markdown); err != nil {
			return next.runtimeError(account, source, wrapError("write markdown", err))
		}
		return 0
	}
	pretty, err := renderMarkdown(next.stdout, markdown)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	if _, err := io.WriteString(next.stdout, pretty); err != nil {
		return next.runtimeError(account, source, wrapError("write terminal output", err))
	}
	return 0
}

func isTTY(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

func renderMarkdown(output io.Writer, markdown string) (string, error) {
	width := 100
	if file, ok := output.(*os.File); ok {
		if columns, _, err := term.GetSize(int(file.Fd())); err == nil {
			width = columns
		}
	}
	return render.RenderTerminalMarkdown(markdown, width, "")
}
