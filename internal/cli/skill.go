package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/mailbox/internal/send"
)

// WriteAgentSkill renders the agent reference from the same command metadata
// and shared help strings that back the CLI help surface.
func WriteAgentSkill(w io.Writer) error {
	var err error
	writef := func(format string, args ...any) {
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(w, format, args...)
	}

	writef("---\nname: mailbox\ndescription: Gmail triage CLI — one-shot commands with TOON/JSON machine output\n---\n\n")
	writef("# mailbox\n\nUse `mailbox` for one-shot Gmail triage commands. Run `mailbox <command> --help` for the same command help shown below.\n\n")
	writef("## Commands\n\n| Command | Usage | Description |\n| --- | --- | --- |\n")
	for _, command := range commandSpecs() {
		writef("| `%s` | `%s` | %s |\n", command.name, markdownTableCell(command.usage), markdownTableCell(command.description))
	}
	writef("\n")
	for _, command := range commandSpecs() {
		writef("### `%s`\n\n%s\n\n", command.name, command.help)
	}

	writef("## Id semantics\n\n%s\n\n", idSemantics)
	writef("## Output formats\n\n%s\n\n", markdownFlags(outputFormats))
	writef("## Credential helpers\n\nEvery surface executes configured credential commands. `*_interactive` passes caller standard input only when it is a real terminal; otherwise helpers receive `/dev/null`.\n\n")
	writef("## Send workflow\n\n%s\n\n", markdownFlags(sendWorkflow))
	writef("## Refusal rules\n\n| Rule | Code | Refusal |\n| --- | --- | --- |\n")
	for _, rule := range send.RuleDocs() {
		writef("| %s | %s | %s |\n", rule.Rule, rule.Code, rule.Doc)
	}
	return err
}

func markdownTableCell(value string) string {
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "\n", "<br>").Replace(value)
}

func markdownFlags(value string) string {
	return strings.NewReplacer("--json", "`--json`", "--text", "`--text`", "--message", "`--message`", "--send", "`--send`").Replace(value)
}
