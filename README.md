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
| `mailbox inbox [--unread] [--max N]` | List inbox threads. `--unread` limits results to unread threads; `--max` defaults to 25 (range 1–500). |
| `mailbox search <query> [--max N]` | Search with Gmail query syntax. |
| `mailbox read <ref> [--full]` | Render a thread newest first; `--full` retains quoted history. |
| `mailbox open <ref>` | Open the newest HTML message in the system browser. |
| `mailbox archive <ref>...` | Remove `INBOX` from one or more threads. |
| `mailbox trash <ref>...` | Move one or more threads to Trash. |
| `mailbox mark read\|unread <ref>...` | Mark one or more threads read or unread. |
| `mailbox label add\|rm <name> <ref>...` | Add or remove a Gmail label. |
| `mailbox attachment <ref> [n] [-o path]` | List attachments, or download attachment `n`. |
| `mailbox status` | Report the selected account's authentication route, Gmail profile, and cache state. |
| `mailbox send [--to RECIPIENT] [--subject SUBJECT] [--reply THREAD] [--forward THREAD] --body BODY [--send]` | Preview a compose, reply, or forward envelope; add `--send` only to transmit it. |

`inbox` and `search` assign numbered references for the selected account. A number resolves only against that account's most recent listing; every mailbox surface resolves identifiers to threads, and a raw message ID resolves to its parent thread. The one exception is `send --message`, which names a message within the thread. JSON listings expose the durable Gmail IDs, so automation should use IDs instead of numbered references.

`--account NAME` selects a configured account and takes precedence over `MAILBOX_ACCOUNT`; when neither is set, mailbox uses `default_account`.

## Output formats

TOON is the default for agents and pipes. `--json` is the stable opt-in for machine-readable JSON, and `--text` forces human output. Every command in the table accepts these output flags. A listing rendered with `--json` has this shape:

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
credential_env_passthrough = ["ACME_SESSION_FILE"]

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
| `read_interactive`, `write_interactive`, `send_interactive` | Applies only to a command source. Read defaults to `false`; write and send default to `true`. Batch surfaces refuse interactive sources; only the TUI executes them. |
| `write_label`, `send_label` | Optional label shown by the TUI while a credential command is awaiting approval. |
| `credential_env_passthrough` | Per-account allow-list restored only for that account's credential command after scrubbing. It cannot restore a credential variable or an unconditionally denied value. |

Mailbox scrubs `MAILBOX_TOKEN`, `MAILBOX_TOKEN_URL`, `MAILBOX_CONFIG`, every
configured credential variable, and the names selected by `scrub_env` and
`scrub_env_patterns` from child environments. A credential command receives
only its account's declared `credential_env_passthrough` values in addition to
that scrubbed environment.

Without a configuration file, mailbox provides one implicit `default` account
that is usable only with `MAILBOX_TOKEN`.

## Authentication

> **Breaking change in v0.4.0:** every use other than `MAILBOX_TOKEN` requires
> a configuration file.

Mailbox keeps credentials separate by class:

| Class | Commands | Gmail scope |
| --- | --- | --- |
| Read | `inbox`, `search`, `read`, `open`, `attachment`, `status` | `gmail.readonly` |
| Write | `archive`, `trash`, `mark`, `label` | `gmail.modify` |
| Send | `send --send` | `gmail.send` |

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
mailbox: account "primary" has no usable write credential: interactive source; this surface cannot prompt — accounts.primary.write_credential_cmd (config: /path/to/config.toml)
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
