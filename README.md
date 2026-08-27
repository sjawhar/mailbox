# mailbox

A Gmail triage CLI and split-pane terminal UI for work and personal accounts.

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
| `mailbox read <ref> [--full]` | Render a thread; `--full` retains quoted history. |
| `mailbox open <ref>` | Open the newest HTML message in the system browser. |
| `mailbox archive <ref>...` | Remove `INBOX` from one or more threads. |
| `mailbox trash <ref>...` | Move one or more threads to Trash. |
| `mailbox mark read\|unread <ref>...` | Mark one or more threads read or unread. |
| `mailbox label add\|rm <name> <ref>...` | Add or remove a Gmail label. |
| `mailbox attachment <ref> [n] [-o path]` | List attachments, or download attachment `n`. |
| `mailbox status` | Report the selected account's authentication route, Gmail profile, and cache state. |

`inbox` and `search` assign numbered references for the selected account. A number resolves only against that account's most recent listing; a raw Gmail thread ID or message ID is always accepted. JSON listings expose the durable Gmail IDs, so automation should use IDs instead of numbered references.

`--account work|personal` selects an account and overrides `GWS_ACCOUNT`. When neither is set, mailbox uses `work`; `GWS_ACCOUNT` must be `work` or `personal`.

Every one-shot command accepts `--json`. It writes one JSON value to standard output and reserves standard error for diagnostics. A listing has this shape:

```json
[
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
```

## Authentication

Mailbox resolves credentials for the selected account in this order: `MAILBOX_TOKEN`, a valid local access-token cache, the work-account broker on Amazon EC2, then the selected OAuth credential.

| Environment variable | Contract |
| --- | --- |
| `MAILBOX_TOKEN` | Any bearer access token. Mailbox uses it verbatim, does not cache it, and gives it highest credential precedence. Use a token with `gmail.modify` for commands that change Gmail. |
| `GWS_WORK_MAIL_OAUTH` | An `authorized_user` OAuth credential for the work account. |
| `GWS_PERSONAL_MAIL_OAUTH` | An `authorized_user` OAuth credential for the personal account. |
| `MAILBOX_BROKER` | Optional path to the broker executable. It has highest precedence for broker discovery; otherwise mailbox looks for `google-user-token` on `PATH`. |
| `MAILBOX_CACHE_DIR` | Optional access-token cache directory; the default is `~/.cache/mailbox/`. |

Set an OAuth variable directly or have any secret manager inject it into the mailbox process. Google OAuth consent with the `https://www.googleapis.com/auth/gmail.modify` scope produces an `authorized_user` credential with this shape:

```json
{
  "type": "authorized_user",
  "client_id": "CLIENT_ID",
  "client_secret": "CLIENT_SECRET",
  "refresh_token": "REFRESH_TOKEN"
}
```

The broker is optional. Mailbox invokes it as `MAILBOX_BROKER --scopes gmail.modify` and expects it to print an access token to standard output. On Amazon EC2, mailbox automatically tries the broker route for the work account after checking `MAILBOX_TOKEN` and the cache; on other machines, it refreshes the selected OAuth credential instead.

## License

Apache-2.0 OR MIT
