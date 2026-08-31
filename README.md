# mailbox

A Gmail triage CLI and split-pane terminal UI for multiple accounts.

Use one-shot commands with `--json` in scripts, or launch `mailbox` in a terminal for the interactive split-pane TUI. Mailbox batches Gmail metadata and multi-thread changes for efficient large inboxes, and sanitizes mail-derived text before rendering it in a terminal.

## Install

Download the release asset for your platform, unpack it, then install the binary:

```bash
# Linux x86_64
curl -fLO https://github.com/sjawhar/mailbox/releases/latest/download/mailbox-linux-amd64.tar.gz

# macOS Apple Silicon
# curl -fLO https://github.com/sjawhar/mailbox/releases/latest/download/mailbox-darwin-arm64.tar.gz

tar -xzf mailbox-linux-amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 mailbox "$HOME/.local/bin/mailbox"
```

For the macOS asset, replace the archive name in the `tar` command with `mailbox-darwin-arm64.tar.gz`.

Or install the latest version with Go:

```bash
go install github.com/sjawhar/mailbox/cmd/mailbox@latest
```

## Usage

Run `mailbox` without a subcommand in a terminal to open the interactive TUI. For one-shot commands:

| Command | Description |
| --- | --- |
| `mailbox inbox [--unread] [--max N] [--filter NAME]` | List inbox threads. `--unread` limits results to unread threads; `--max` defaults to 25 (range 1–500). |
| `mailbox search <query> [--max N] [--filter NAME]` | Search with Gmail query syntax. |
| `mailbox read <ref> [--full]` | Render a thread newest first; `--full` retains quoted history. |
| `mailbox open <ref>` | Open the newest HTML message in the system browser. |
| `mailbox archive (<ref>... \| --filter NAME)` | Remove `INBOX` from one or more threads, or every matching inbox thread. |
| `mailbox trash (<ref>... \| --filter NAME)` | Move one or more threads to Trash, or every matching inbox thread. |
| `mailbox mark read\|unread (<ref>... \| --filter NAME)` | Mark one or more threads read or unread. |
| `mailbox label add\|rm <name> (<ref>... \| --filter NAME)` | Add or remove a Gmail label. |
| `mailbox attachment <message-id> [filename\|index] [-o PATH\|-o -]` | List message attachment parts, or fetch one without overwriting an existing file. |
| `mailbox status` | Report the selected account's authentication route, Gmail profile, and cache state. |
| `mailbox send [--to RECIPIENT] [--subject SUBJECT] [--reply THREAD] [--forward THREAD] --body BODY [--attach PATH]... [--save-draft\|--send]` | Preview a compose, reply, or forward envelope; `--save-draft` creates a Gmail draft and `--send` transmits it. |
| `mailbox send --draft <draft-id> [overrides] [--send --message=<id>]` | Resume a Gmail server-side draft through the send pipeline. |
| `mailbox drafts [--max N]` | List Gmail server-side drafts newest first. |

`inbox` and `search` assign numbered references for the selected account. A number resolves only against that account's most recent listing; every mailbox surface resolves identifiers to threads, and a raw message ID resolves to its parent thread. The two exceptions are `send --message`, which names a message within the thread, and `attachment`, which takes the literal message ID from `read` output. JSON listings expose the durable Gmail IDs, so automation should use IDs instead of numbered references.

`--filter NAME` is available on `inbox`, `search`, `archive`, `trash`, `mark`, and `label`. On listings it keeps only matching threads and reports the active filter. On actions it replaces explicit references—combining a filter with references is a usage error—and traverses the **entire inbox** before changing every matching thread, not merely the first page. A zero-match action is successful and changes nothing.

`--account NAME` selects a configured account and takes precedence over `MAILBOX_ACCOUNT`; when neither is set, mailbox uses `default_account`.

## TUI

Run `mailbox` in a terminal to open the split-pane TUI. Start in a named-filter view with `mailbox --filter github`; `f` then cycles none and each configured filter in declaration order, refetching the listing each time.

| Key | Action |
| --- | --- |
| `v` | Enter select mode. Selected rows display a marker column. |
| `space` | Toggle the current row while in select mode. |
| `a` | Select all listed rows while in select mode. |
| `esc` | Leave select mode and clear its selection. |
| `f` | Cycle the active filter. |
| `#` | Move the selection, or the cursor row when nothing is selected, to Trash. |
| `r` | Reply in the system editor. |
| `c` | Prompt for a recipient and subject, then compose in the system editor. |

`d` is unbound. Other action keys use the selection when it is non-empty and otherwise use the cursor row.

For `r` and `c`, mailbox selects `$VISUAL`, then `$EDITOR`, then `vi`. It parses that value as POSIX shell words (quoting and backslash escaping only) and executes the resulting argv directly, never through a shell. The editor opens a private draft with informational envelope metadata above this exact scissors line:

```text
# ------------------------ >8 ------------------------
```

Mailbox sends only the bytes after the first exact scissors line; edits above it do not alter the envelope. A missing line refuses the send. From the confirmation screen, `Esc` opens `d` discard · `s` save to Gmail drafts · `e` keep editing; `Esc` or `enter` discards by default, while an empty draft discards silently.

## Output formats

TOON is the default for agents and pipes. `--json` is the stable opt-in for machine-readable JSON, and `--text` forces human output. Every command in the table accepts these output flags. A listing rendered with `--json` has this shape:

Attachment downloads with `-o -` are the exception: they stream raw bytes to standard output and write status to standard error without TOON or JSON wrapping.

```json
{
  "account": "work",
  "threads": [
    {
      "n": 1,
      "id": "GMAIL_THREAD_ID",
      "subject": "Subject",
      "from": "Sender <sender@example.com>",
      "date": "2026-08-27T01:02:03Z",
      "snippet": "Preview text",
      "unread": true,
      "labels": ["INBOX", "UNREAD"]
    }
  ]
}
```

Filtered listings add `"filter": "NAME"` to their machine output. Filter-driven bulk actions emit one receipt document containing `account`, `action`, `filter`, `matched`, `attempted`, `succeeded`, `failed`, and `ok`. A failed receipt includes its thread ID, HTTP status, and reason, so partial success is visible even when the command exits nonzero:

```json
{
  "account": "work",
  "action": "archive",
  "filter": "github",
  "matched": 2,
  "attempted": 2,
  "succeeded": ["thread-01"],
  "failed": [{"id": "thread-02", "status": 403, "reason": "insufficientPermissions"}],
  "ok": false
}
```

Usage errors with a machine output format also emit one structured error document to standard output and retain exit status 2. With `--json`, the envelope has the form `{"error":{"code":"usage","message":"..."}}`; diagnostic help remains on standard error.

## Configuration

Mailbox reads TOML from `$XDG_CONFIG_HOME/mailbox/config.toml`; when
`XDG_CONFIG_HOME` is unset, the default is `~/.config/mailbox/config.toml`.
Set `MAILBOX_CONFIG` to use a different path. The configuration file must be
a regular file owned by the current user and must not be group- or
world-writable.

```toml
# ~/.config/mailbox/config.toml
default_account = "primary"              # required when more than one account exists
scrub_env = ["ACME_SESSION_FILE"]        # exact names removed from child environments
scrub_env_patterns = ["ACME_*_OAUTH"]    # glob patterns removed from child environments
credential_timeout_secs = 120            # command deadline; the default is 120

[accounts.primary]
# Each class has exactly one source: _credential_env or _credential_cmd.
read_credential_cmd = ["my-token-helper", "--scopes", "gmail.readonly"]
read_interactive = false                 # false by default for a command source
write_credential_cmd = ["my-approver", "--", "mailbox", "__mint", "--env", "ACME_OAUTH_JSON"]
write_interactive = true                 # true by default for a command source
write_label = "Approval required"        # optional text shown while the TUI waits
send_credential_cmd = ["my-send-helper"]
send_interactive = true                  # true by default for a command source
send_label = "hardware key touch"        # optional text shown while the TUI waits
credential_env_passthrough = ["ACME_BROKER_SESSION"]      # shared by this account's credential classes
send_credential_env_passthrough = ["ACME_SEND_SESSION"]   # available only to its send helper

# Instead of either command above, its class can read a value directly:
# read_credential_env = "ACME_OAUTH_JSON"
# write_credential_env = "ACME_WRITE_OAUTH_JSON"
# send_credential_env = "ACME_SEND_OAUTH_JSON"

Each `[accounts.NAME]` table needs a read source. Write and send sources are optional.
For each class, choose exactly one of `_credential_env` and
`_credential_cmd`; an environment source holds either an `authorized_user`
JSON value or a bare access token. A command source is an argv array resolved
when the configuration loads. Its stdout is either the strict
`{"access_token": ..., "expiry": ...}` object emitted by `__mint`, or a bare
token: a leading `{` selects the JSON form, and invalid JSON is an error rather
than a fallback to a bare token.

| Key | Meaning |
| --- | --- |
| `default_account` | Account selected when neither `--account` nor `MAILBOX_ACCOUNT` is set. It is required when more than one account is configured. |
| `accounts.<name>` | Named account table. Names may contain letters, numbers, hyphens, and underscores. |
| `scrub_env` | Exact environment-variable names removed from every mailbox child process. |
| `scrub_env_patterns` | Shell-style glob patterns for additional names removed from every mailbox child process. |
| `credential_timeout_secs` | Positive command timeout in seconds. The default is 120 seconds. Timeout cancels the credential helper's process group. |
| `read_credential_env`, `write_credential_env`, `send_credential_env` | The environment variable containing an `authorized_user` JSON value or bare token for that class. |
| `read_interactive`, `write_interactive`, `send_interactive` | Applies only to a command source. Read defaults to `false`; write and send default to `true`. Command sources execute from every surface. An interactive source inherits caller standard input only when it is a real terminal; otherwise the helper receives `/dev/null`. |
| `write_label`, `send_label` | Optional label shown by the TUI while a credential command is awaiting approval. |
| `credential_env_passthrough` | Per-account allow-list restored to every credential class for that account after scrubbing. Use it only for genuinely shared helper material. |
| `read_credential_env_passthrough`, `write_credential_env_passthrough`, `send_credential_env_passthrough` | Per-account, class-private allow-lists restored only to that class's credential command. A name cannot appear in the shared list and a class list, or in two class lists. |

The configured credential helper, not the invoking surface, is responsible for
any approval step.

Mailbox scrubs `MAILBOX_TOKEN`, `MAILBOX_TOKEN_URL`, `MAILBOX_CONFIG`, every
configured credential variable, every class-private passthrough variable, and
the names selected by `scrub_env` and `scrub_env_patterns` from credential-child
environments. A credential command receives only its account's shared
`credential_env_passthrough` values plus its own class-private passthrough
values. No passthrough list can restore a credential variable or an
unconditionally denied value.

Without a configuration file, mailbox provides one implicit `default` account:
read and write use it only with `MAILBOX_TOKEN`; send requires a configured
`send_credential_env` or `send_credential_cmd`.

### Filters

Define named filters with TOML tables. This neutral filter keeps matching GitHub notifications:

```toml
[filters.github]
from = "notifications@github\\.com"
subject = "(?i)ci"
```

Each `[filters.NAME]` table needs at least one rule. Names match `[a-z0-9][a-z0-9-]*`, and rule fields are `from`, `to`, `cc`, `subject`, and `list` (`List-ID`). Rules use Go RE2 syntax and are compiled while configuration loads, so an invalid name, field, or expression is a configuration error.

Rules in one filter must all match one message; a thread matches if any of its messages satisfies those rules. Matching uses decoded, unfolded header values. A value larger than 8 KiB does not match and is never truncated. Filter tables retain declaration order: that order controls `f` in the TUI and is the order used when listing configured filters. An undefined `--filter NAME` is a hard error that names the configured filters; it is never treated as an empty result.

## Authentication

> **Breaking change in v0.4.0:** every use other than `MAILBOX_TOKEN` requires
> a configuration file.

Mailbox keeps credentials separate by class:

| Class | Commands | Gmail scope |
| --- | --- | --- |
| Read | `inbox`, `search`, `read`, `open`, `attachment`, `drafts`, `send --draft` dry runs, `status` | `gmail.readonly` |
| Write | `archive`, `trash`, `mark`, `label`, `send --save-draft`, post-send draft deletion, TUI draft save | `gmail.modify` |
| Send | `send --send`, `send --draft ... --send` | `gmail.send` |

Read and write resolution is `MAILBOX_TOKEN`, then a valid read cache entry (read only), then the configured source. `MAILBOX_TOKEN` is an override for those two classes; if its scope is insufficient for a write, mailbox reports that error rather than trying another source. The read cache stores only expiring tokens. Every entry is bound to its configured source, so changing that source invalidates its cache entry.

Send tokens are resolved only from the configured send source, kept in memory only, and never cached or satisfied by `MAILBOX_TOKEN`.

| Environment variable | Contract |
| --- | --- |
| `MAILBOX_TOKEN` | Bearer token override for both classes. It is never cached. |
| `MAILBOX_CONFIG` | Overrides the configuration-file path. |
| `MAILBOX_ACCOUNT` | Selects a configured account unless `--account` is present. |
| `MAILBOX_CACHE_DIR` | Overrides the read-token cache directory. Cache entries are readable only by the current user. |
| `MAILBOX_TOKEN_URL` | Loopback-only test and debugging override for the token endpoint. It is scrubbed from every child process. |

When a one-shot command needs an unavailable write credential, it exits with
status 1 and names the configuration key and file:

```text
mailbox: account "primary" has no usable write credential: no credential source configured — accounts.primary.write_credential_cmd (config: /path/to/config.toml)
```

With `--json`, the same condition writes this envelope to standard output:

```json
{"error":{"code":"needs_write_credential","account":"primary","config_key":"accounts.primary.write_credential_cmd","config":"/path/to/config.toml"}}
```

The read analogue uses `needs_read_credential` and the configured read key.

The send analogue uses `needs_send_credential` and the configured send key.

`mailbox __mint --env VAR` is an internal credential-helper child: it refuses
`MAILBOX_TOKEN`, reads only `VAR`, prints one JSON object to standard output,
writes nothing to disk, and does not load configuration.

## Send

`mailbox send` is a dry run by default: it resolves and prints the envelope with the read credential, without touching the send credential. Add `--send` only after inspecting that preview.

Every body source—`--body`, standard input, `--body-file`, and editor compose—is markdown. Mailbox sends it as `multipart/alternative`: a raw markdown `text/plain` leaf and a sanitized rendered `text/html` leaf. Raw HTML is omitted from the HTML leaf. Link and image destinations allow only `https`, `http`, `mailto`, and empty or fragment destinations; other schemes are removed before rendering.

`--attach PATH` is repeatable on compose, reply, and forward sends, including `--save-draft` and `--draft` resume. With one or more attachments, mailbox nests the existing `multipart/alternative` body inside `multipart/mixed`; zero attachments retain the existing MIME shape. The fixed 25,000,000-byte cap measures the final RFC 5322/MIME message, including headers, boundaries, body leaves, forwarded original, and carried or new attachments.

For replies and forwards, `--message` selects the message within the named thread. `--send` requires that pin so the inspected envelope and sent message cannot diverge; without `--message`, a preview selects the newest message.

For replies, mailbox derives recipients from `Reply-To` or `From`, plus the original `To` and `Cc`, then subtracts the account primary address from every final `To`, `Cc`, and `Bcc` set before evaluating refusals. Primary-address comparison is case-insensitive on the addr-spec; aliases, plus-tags, and dot variants are intentionally not treated as self.

| Rule | Refusal |
| --- | --- |
| R1 | No recipients remain after resolution. |
| R2 | A reply's recipients contain only the account primary address after self-subtraction. |
| R3 | A recipient does not parse as an email address. |
| R4 | A subject or recipient contains a carriage return or line feed. |
| R5 | The message body is empty. |
| R6 | `Reply-To` differs from `From`; provide explicit `--to` or `--cc`. |
| R-A1 | An attachment path cannot be read. |
| R-A2 | An attachment file is empty. |
| R-A3 | The final RFC 5322/MIME message exceeds 25,000,000 bytes. |

`--save-draft` completes the same recipient resolution, refusal checks, and MIME assembly as a send, then creates a Gmail draft instead of transmitting it. It is mutually exclusive with `--send`; reply and forward drafts retain their thread.

`mailbox send --draft <draft-id>` resumes a Gmail draft through the same resolver and is a dry run by default. The preview prints the draft's current message ID; `mailbox send --draft <draft-id> --send --message=<id>` pins that ID before sending. A server-side edit changes the ID and refuses with `draft_changed`, including a fresh preview. On decoded send success, mailbox sends through `messages.send` and then deletes the draft. An indeterminate send reports `draft_send_unknown` and leaves the draft intact.

## Drafts

`mailbox drafts [--max N]` lists Gmail drafts newest first with `draft_id`, `thread_id`, recipients, subject, and update time. Draft listing and draft resolution use the read class without an unlock; creating or deleting a draft uses the write class.

Google's scope design makes `gmail.modify` send-capable via `drafts.send`; every holder of a modify credential has this today, independent of mailbox. mailbox never calls `drafts.send` — its transport has no such operation — and transmits resumed drafts only through `messages.send` under the send credential. The credential-level exposure is Google's design and is documented rather than silently accepted.

## Migrating from v0.3.0 and earlier

Move account and credential choices into `config.toml`:

| Previous setting | Configuration equivalent |
| --- | --- |
| `--account` or an account-selection environment variable | Configured account names and `default_account`; use `--account NAME` or `MAILBOX_ACCOUNT` to select one for an invocation. |
| Per-account read credential environment variable | `accounts.<name>.read_credential_env` |
| Token-broker command or its executable-location environment variable | `accounts.<name>.read_credential_cmd` |
| Per-account write credential environment variable | `accounts.<name>.write_credential_cmd` when an approval helper supplies the credential, or `accounts.<name>.write_credential_env` when it is already present. |
| Former re-exec and hosting-detection controls | No direct replacement. Put the required behavior in an explicit credential command. |

Any variable the previous version scrubbed by pattern must now be listed in
`scrub_env` or `scrub_env_patterns`. Mailbox no longer re-executes itself under
a credential manager and no longer auto-detects a hosting environment; model
both behaviors as explicit credential commands in the account configuration.

## License

Apache-2.0 OR MIT
