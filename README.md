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
| `mailbox read <ref> [--full]` | Render a thread newest first; `--full` retains quoted history. |
| `mailbox open <ref>` | Open the newest HTML message in the system browser. |
| `mailbox archive <ref>...` | Remove `INBOX` from one or more threads. |
| `mailbox trash <ref>...` | Move one or more threads to Trash. |
| `mailbox mark read\|unread <ref>...` | Mark one or more threads read or unread. |
| `mailbox label add\|rm <name> <ref>...` | Add or remove a Gmail label. |
| `mailbox attachment <ref> [n] [-o path]` | List attachments, or download attachment `n`. |
| `mailbox status` | Report the selected account's authentication route, Gmail profile, and cache state. |

`inbox` and `search` assign numbered references for the selected account. A number resolves only against that account's most recent listing; a raw Gmail thread ID or message ID is always accepted. JSON listings expose the durable Gmail IDs, so automation should use IDs instead of numbered references.

`--account work|personal` selects an account and overrides `GWS_ACCOUNT`. When neither is set, mailbox uses `work`; `GWS_ACCOUNT` must be `work` or `personal`.

Every command in the table accepts `--json`. It writes one JSON value to standard output and reserves standard error for diagnostics. A listing has this shape:

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

## Authentication

Mailbox separates two credential classes per account (`work`, `personal`) and never mixes them:

| Class | Gmail scope | Resolution order | Tier |
| --- | --- | --- | --- |
| Read (`inbox`, `search`, `read`, `open`, `attachment`, `status`) | `gmail.readonly` | `MAILBOX_TOKEN` → valid local cache → work broker on Amazon EC2 → `GWS_{WORK,PERSONAL}_READ_OAUTH` (re-exec under `secrets` when unset) | agent |
| Mutation (`archive`, `trash`, `mark`, `label`) | `gmail.modify` | `MAILBOX_TOKEN` → `GWS_{WORK,PERSONAL}_MODIFY_OAUTH` | human (YubiKey) |

| Environment variable | Contract |
| --- | --- |
| `MAILBOX_TOKEN` | Any bearer access token. Used verbatim for both classes, never cached, highest precedence. If it lacks `gmail.modify`, mutations fail with a typed scope error — there is no fallback to another credential. |
| `GWS_WORK_READ_OAUTH` / `GWS_PERSONAL_READ_OAUTH` | `authorized_user` OAuth credentials with `gmail.readonly` for the read path. Agent tier. |
| `GWS_WORK_MODIFY_OAUTH` / `GWS_PERSONAL_MODIFY_OAUTH` | `authorized_user` OAuth credentials with `gmail.modify`. Human tier: acquiring one through `secrets` requires a watched YubiKey approval. |
| `MAILBOX_BROKER` | Optional path to the broker executable (work reads on EC2). Mailbox invokes it as `MAILBOX_BROKER --scopes gmail.readonly`; it prints an access token on stdout. |
| `MAILBOX_CACHE_DIR` | Optional access-token cache directory (read tokens ONLY — mutation tokens are never written to disk). Default: `$XDG_CACHE_HOME/mailbox`, else `~/.cache/mailbox` (Linux) or `~/Library/Caches/mailbox` (macOS). |

### Mutation credentials

The interactive TUI is the only surface that may trigger a `secrets` approval:
the first mutation keypress for an account mints a `gmail.modify` access token
through `secrets GWS_<ACCOUNT>_MODIFY_OAUTH -- mailbox __mint --account <a>`
(status line: `unlocking <account> mutations (GWS_<ACCOUNT>_MODIFY_OAUTH) —
touch your YubiKey if it blinks`). The minted token lives in process memory
only and expires after about an hour; the next mutation after expiry re-mints
on that keypress.

One-shot CLI mutations never invoke `secrets` for a modify key and never
re-exec for a read credential: with the credential in the environment they
refresh in-process, act, and exit — nothing cached, nothing spawned, even on
a cold cache (their internal reads ride the same `gmail.modify` token).
Without the credential they fail loudly with exit code 1 and the exact
remedy:

```
mailbox: mutation credentials for work are human-tier; run: secrets GWS_WORK_MODIFY_OAUTH -- mailbox archive 42
```

With `--json`, the same failure is a machine-readable envelope on stdout:

```json
{"error":{"code":"needs_mutation_credential","key":"GWS_WORK_MODIFY_OAUTH","command":"secrets GWS_WORK_MODIFY_OAUTH -- mailbox archive 42 --json"}}
```

`mailbox __mint` is internal: it is the short-lived child of the TUI's mint
flow described above. It refuses to run when `MAILBOX_TOKEN` is set, reads
only `GWS_<ACCOUNT>_MODIFY_OAUTH` from its own environment, prints a single
JSON object (`access_token`, RFC 3339 `expiry`) to stdout, and writes nothing
to disk.

### Read credentials

Set a READ OAuth variable directly or let mailbox re-exec itself under
`secrets GWS_<ACCOUNT>_READ_OAUTH --` when the variable is unset and a
`secrets` CLI is on `PATH`. Google OAuth consent with the
`https://www.googleapis.com/auth/gmail.readonly` scope produces the
`authorized_user` credential shape:

```json
{
  "type": "authorized_user",
  "client_id": "CLIENT_ID",
  "client_secret": "CLIENT_SECRET",
  "refresh_token": "REFRESH_TOKEN"
}
```

Exit codes: `0` success, `1` runtime or credential failure, `2` usage errors.

## License

Apache-2.0 OR MIT
