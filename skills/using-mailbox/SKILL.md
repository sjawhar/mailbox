---
name: mailbox
description: Gmail triage CLI — one-shot commands with TOON/JSON machine output
---

# mailbox

Use `mailbox` for one-shot Gmail triage commands. Run `mailbox <command> --help` for the same command help shown below.

## Commands

| Command | Usage | Description |
| --- | --- | --- |
| `inbox` | `mailbox inbox [--unread] [--max N] [--filter NAME] [--text\|--json]` | list inbox threads |
| `search` | `mailbox search [--max N] [--filter NAME] [--text\|--json] <query...>` | search threads |
| `read` | `mailbox read [--full] [--text\|--json] <thread>` | read a thread |
| `open` | `mailbox open [--text\|--json] <thread>` | open thread HTML in a browser |
| `archive` | `mailbox archive [--filter NAME] [--text\|--json] [<thread>...]` | archive threads |
| `trash` | `mailbox trash [--filter NAME] [--text\|--json] [<thread>...]` | move threads to trash |
| `mark` | `mailbox mark [--filter NAME] [--text\|--json] <read\|unread> [<thread>...]` | mark threads read or unread |
| `label` | `mailbox label [--filter NAME] [--text\|--json] <add\|rm> <label> [<thread>...]` | add or remove a label |
| `attachment` | `mailbox attachment [-o PATH\|-o -] [--text\|--json] <message-id> [filename\|index]` | list or fetch message attachments |
| `drafts` | `mailbox drafts [--max N] [--text\|--json]` | list Gmail drafts |
| `status` | `mailbox status [--text\|--json]` | show configured account status |
| `send` | `mailbox send [--attach PATH]... [--save-draft\|--send] [options]` | compose, reply, or forward mail (dry-run by default) |

### `inbox`

Lists inbox threads. It takes no positional arguments; --unread restricts results to unread threads, --max sets 1–500 rows (default 25), and --filter restricts rows to a named config filter.

### `search`

Searches threads with one or more query terms; --max sets 1–500 rows (default 25) and --filter restricts rows to a named config filter. Gmail query operators pass through verbatim: from: to: cc: bcc: subject: label: is: has: in: filename: after: before: older_than: newer_than: deliveredto: list: (see Gmail search syntax).

### `read`

Reads one thread. Messages print newest first. --full keeps quoted history.

### `open`

Renders the newest HTML message from one thread and hands it to the system browser.

### `archive`

Removes the INBOX label from one or more threads, or every inbox thread matching --filter.

### `trash`

Moves one or more threads to Trash, or every inbox thread matching --filter.

### `mark`

Marks one or more threads read or unread, or every inbox thread matching --filter.

### `label`

Adds or removes one Gmail label on one or more threads, or every inbox thread matching --filter.

### `attachment`

Lists a message's attachments from 'read' output, or fetches one. Listings use zero-based indexes and sanitized filenames; select an exact listed filename or zero-based index. Downloads never overwrite existing files (attachment_exists); use -o to choose another file or directory. -o - streams raw bytes to stdout and writes status to stderr, without machine-format wrapping.

### `drafts`

Lists Gmail server-side drafts newest-first: draft_id, thread_id, to, subject, updated. --max sets 1–500 rows (default 25). Listing is read-class (no unlock). Resume one with 'mailbox send --draft <draft_id>'.

### `status`

Reports configured account authentication routes, Gmail profiles, and read-cache state.

### `send`

Compose:
  mailbox send --to a@x [--cc b@y] [--bcc c@z] --subject S --body TEXT      # compose
  mailbox send --reply=<thread-id>  --body TEXT [--message=<id>] [--to ...] # reply
  mailbox send --forward=<thread-id> --to a@x --body TEXT [--message=<id>]  # forward

The body comes from exactly one of: --body TEXT, --body - (stdin), or --body-file PATH (- for stdin) — file input suits agent-drafted content.

Attachments:
  --attach PATH is repeatable on compose, reply, and forward, including --save-draft and --draft resume. The dry-run reports each part's filename, size, mime_type, and sha256. The final message is capped at 25,000,000 bytes.

Drafts:
  --save-draft resolves recipients and refusals, renders markdown MIME and attachments, then creates a Gmail draft. It costs a write unlock and is mutually exclusive with --send.
  --draft <draft-id> resumes a Gmail draft through the same validation and fresh attachment serialization. Its dry-run prints the draft's CURRENT message id; --send --message=<id> pins that id. A server-side edit refuses draft_changed and prints a fresh preview. On decoded success mailbox transmits through messages.send (send class), then deletes the draft (write class). A repeated 401 after reminting is a concrete send credential rejection; only an indeterminate send reports draft_send_unknown and leaves the draft intact. mailbox never calls drafts.send.

A dry-run is the default: resolve the envelope first. Start with the dry run, copy its --message value, then add --send to transmit that exact target. Reply and forward previews select the newest message unless --message selects one; --send requires --message so it pins the exact message within the named thread.

Refusal rules:
  R1 (empty_recipients): No recipients remain after resolution.
  R2 (self_only_recipients): A reply's recipients contain only the account's primary address after self-subtraction.
  R3 (invalid_address): A recipient does not parse as an email address.
  R4 (header_injection): A subject or recipient contains CR or LF.
  R5 (empty_body): The message body is empty.
  R6 (needs_explicit_recipient): Reply-To differs from From; provide --to or --cc.
  R-A1 (attachment_unreadable): An --attach path does not exist or cannot be read.
  R-A2 (attachment_empty): An --attach file is empty.
  R-A3 (attachment_too_large): The final MIME message exceeds 25,000,000 bytes.

## Id semantics

ids: mailbox ids are THREAD ids everywhere; the exceptions are 'send --message' and 'attachment', which take message ids (message ids appear in 'read' output). All-digit arguments are refs into the last 'inbox'/'search' listing.

## Output formats

TOON is the default for agents and pipes. `--json` is the stable opt-in. `--text` forces human output.

## Credential helpers

Every surface executes configured credential commands. `*_interactive` passes caller standard input only when it is a real terminal; otherwise helpers receive `/dev/null`.

## Send workflow

Start with the dry run, copy its `--message` value, then add `--send` to transmit that exact target.

## Refusal rules

| Rule | Code | Refusal |
| --- | --- | --- |
| R1 | empty_recipients | No recipients remain after resolution. |
| R2 | self_only_recipients | A reply's recipients contain only the account's primary address after self-subtraction. |
| R3 | invalid_address | A recipient does not parse as an email address. |
| R4 | header_injection | A subject or recipient contains CR or LF. |
| R5 | empty_body | The message body is empty. |
| R6 | needs_explicit_recipient | Reply-To differs from From; provide --to or --cc. |
| R-A1 | attachment_unreadable | An --attach path does not exist or cannot be read. |
| R-A2 | attachment_empty | An --attach file is empty. |
| R-A3 | attachment_too_large | The final MIME message exceeds 25,000,000 bytes. |
