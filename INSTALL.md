# Install mailbox (instructions for an agent)

You are a coding agent setting up mailbox for the person you work for. Run the steps in order. Each step ends with a check; move on only when it passes.

The goal: `mailbox inbox` lists the person's Gmail inbox from any shell or agent session, with no sign-in. mailbox gets its Gmail access tokens from [gws-accounts](https://github.com/sjawhar/gws-accounts), which keeps one Google login per named account (`GWS_ACCOUNT=<name> gws …`) and renews it on its own. The person answers the questions in step 1 and, if gws-accounts has no login for an account yet, clicks through Google's consent page once.

mailbox publishes release binaries for Linux x86_64 and macOS on Apple silicon. On other platforms, step 3 builds it with Go.

Never print a credential: no client secret, refresh token or access token, and never the output of `gws auth export`, the contents of `~/.config/gws/<name>/`, or what a credential command writes to stdout. When you check a command that prints a token, pipe it through `jq` and print only its other fields.

The [README](README.md) is the reference for mailbox's commands and its configuration file.

## 1. Ask once

First look at what is already here. Print no credential, and from the config file only its account names and default:

```bash
command -v mailbox && mailbox --version
cfg="${MAILBOX_CONFIG-${XDG_CONFIG_HOME:-$HOME/.config}/mailbox/config.toml}"
case "$cfg" in "") echo "MAILBOX_CONFIG is set but empty: ask the person to unset it or name a file" ;; /*) ;; *) cfg="$PWD/$cfg" ;; esac
echo "mailbox config: $cfg"; ls -l "$cfg" 2>/dev/null && grep -E '^(default_account|\[accounts\.)' "$cfg"
h=~/.local/bin/mailbox-gws-token; [ -e "$h" ] && { [ "$(sed -n 2p "$h")" = "# mailbox-gws-token: written by mailbox's INSTALL.md" ] && echo "helper: ours" || echo "helper: someone else's"; }
type -a gws
jq -r 'to_entries[] | "\(.key)\t\(.value.description // "")\t\(if .value | has("tiers") then "tiers: \(.value.tiers | keys | join(","))" else "login" end)"' ~/.config/gws/accounts.json 2>/dev/null
```

- The `mailbox config:` line names the file mailbox reads (`MAILBOX_CONFIG` when set, else under `XDG_CONFIG_HOME`, else `~/.config/mailbox/config.toml`), as an absolute path. Later blocks write it in place of `<config path>`. An existing file is the person's: step 5 adds the account tables it lacks and changes nothing else in it. If it already has an `[accounts.<name>]` table for an account the person is about to name, or a `default_account` other than the one they choose, ask in the same message whether to keep it as it is or have you replace it by hand in step 5.
- `helper: someone else's` means `~/.local/bin/mailbox-gws-token` is not this guide's: ask the person in the same message for another absolute path to put the helper at. Later blocks write the helper's absolute path (`$HOME/.local/bin/mailbox-gws-token` expanded, or the one the person gave) in place of `<helper path>`.
- gws-accounts is installed when `type -a gws` lists a file under `.local/share/gws-accounts/bin` first and `~/.config/gws/accounts.json` exists. Its accounts are the lines the `jq` command prints. An account marked `login` runs on a login gws keeps in its directory; one marked `tiers` gets its credentials from the commands its tiers name (the gws-accounts README's Tiers section).

Then ask the person these questions in one message, and work without asking again until step 2 or 4 needs their browser, or step 3 finds no release for this platform and needs their approval to install Go:

1. Which Gmail accounts should mailbox use? For each: the gws-accounts account it already has (list them), or, for one gws-accounts does not have yet, a short name such as `work`, its email address, and a few words on who it is (such as "Example Corp Workspace" or "personal Gmail"). If there are several, which one does `mailbox` use when no account is named?
2. What may agents do with each account through mailbox?
   - **Read only** (recommended): list, search and read mail, and list drafts.
   - **Read and organize**: also archive, trash, label and mark threads, and save drafts. This needs Google's `gmail.modify` scope, which Google also lets send a saved draft. mailbox never does that, but any program that holds the token can, so tell the person that this choice lets an agent send mail if it goes around mailbox.
   - **Read, organize and send**: also send mail with `mailbox send --send`. Tell the person that nothing then stands between an agent and sending as them except mailbox's dry-run default; the README's Send section explains it.

Do not ask anything you can check yourself: the operating system, installed tools, or whether there is a display.

**Check:** for every account you have its gws-accounts name (and, for a new one, its email and description), its choice from question 2, and, when there is more than one account, the default.

## 2. gws-accounts

If step 1 found gws-accounts, and it has every account the person named, skip to the check. If it is installed but lacks an account, run its guide's steps 5 to 8 for that account only. Otherwise install it by following its own guide, <https://raw.githubusercontent.com/sjawhar/gws-accounts/main/INSTALL.md>, for the accounts the person named, with the answers from step 1: a new account's name, email and description are its question 1. Answer its question 3 from question 2 here:

- **Read only** here: choose **Read only** there.
- Either other answer here: choose **Read and write** there, but in its step 7 log in with the scope flags below instead of the ones it lists. mailbox narrows each token to one Gmail scope, and Google narrows a token only to scopes the login holds, so these add `gmail.readonly`, and `gmail.send` only when the person chose to send.

  **Read and organize:**

  ```text
  --scopes 'https://www.googleapis.com/auth/drive,https://www.googleapis.com/auth/spreadsheets,https://www.googleapis.com/auth/gmail.readonly,https://www.googleapis.com/auth/gmail.modify,https://www.googleapis.com/auth/calendar,https://www.googleapis.com/auth/documents,https://www.googleapis.com/auth/presentations,https://www.googleapis.com/auth/tasks'
  ```

  **Read, organize and send:**

  ```text
  --scopes 'https://www.googleapis.com/auth/drive,https://www.googleapis.com/auth/spreadsheets,https://www.googleapis.com/auth/gmail.readonly,https://www.googleapis.com/auth/gmail.modify,https://www.googleapis.com/auth/gmail.send,https://www.googleapis.com/auth/calendar,https://www.googleapis.com/auth/documents,https://www.googleapis.com/auth/presentations,https://www.googleapis.com/auth/tasks'
  ```

From here on, start every command that runs gws or mailbox with `export PATH="$HOME/.local/share/gws-accounts/bin:$HOME/.local/bin:$PATH";`: your shell may have started before gws-accounts changed the startup files.

**Check**, for every account marked `login`:

```bash
GWS_ACCOUNT=<gws account> gws gmail users getProfile --params '{"userId":"me"}' | jq -r .emailAddress
```

It prints the account's email address. An account marked `tiers` is checked in step 4.

## 3. The mailbox binary

Install the latest release into `~/.local/bin`, checking it against the release's `SHA256SUMS`:

```bash
(
  set -e
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) asset=mailbox-linux-amd64.tar.gz ;;
    Darwin-arm64) asset=mailbox-darwin-arm64.tar.gz ;;
    *) echo "no mailbox release for $(uname -sm)"; exit 1 ;;
  esac
  base=https://github.com/sjawhar/mailbox/releases/latest/download
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  cd "$tmp"
  curl -fsSLO "$base/$asset"
  curl -fsSLO "$base/SHA256SUMS"
  grep " $asset\$" SHA256SUMS >"$asset.sha256"
  if command -v sha256sum >/dev/null; then sha256sum -c "$asset.sha256"; else shasum -a 256 -c "$asset.sha256"; fi
  tar -xzf "$asset" mailbox
  mkdir -p "$HOME/.local/bin"
  install -m 0755 mailbox "$HOME/.local/bin/mailbox"
)
```

If it prints `no mailbox release`, build it instead with Go 1.27 or later (ask the person before installing Go): `GOBIN="$HOME/.local/bin" go install github.com/sjawhar/mailbox/cmd/mailbox@latest`. That build reports its version as `dev`.

**Check:** the checksum line says `OK` (or `go install` succeeded), `~/.local/bin/mailbox --version` prints `mailbox <version>`, and a new login shell finds it:

```bash
env -i HOME="$HOME" USER="$USER" TERM=dumb "$SHELL" -lc 'command -v mailbox'
```

prints `~/.local/bin/mailbox` expanded. gws-accounts' PATH line puts that directory on PATH.

## 4. Scopes

mailbox gives each class of command its own token with one Gmail scope: `gmail.readonly` to read, `gmail.modify` to organize, `gmail.send` to send. Each account needs those the person chose for it in question 2:

- **Read only** needs `gmail.readonly`.
- **Read and organize** needs `gmail.readonly` and `gmail.modify`.
- **Read, organize and send** needs all three.

**An account marked `login`:** list the Gmail scopes its login holds.

```bash
GWS_ACCOUNT=<gws account> gws auth status 2>/dev/null | jq -r '.scopes[]? | select(startswith("https://www.googleapis.com/auth/gmail"))'
```

If one it needs is missing, the account was logged in with other scopes: gws-accounts' own **Read and write** grants `gmail.modify` alone. Log it in again by following gws-accounts' step 7 (`~/.local/share/gws-accounts/INSTALL.md`) with the matching `--scopes` from step 2 above; the new login replaces the old one in the account's directory.

**An account marked `tiers`:** it has no login of its own. Its tier runner can
release authorized-user JSON, which `mailbox __mint` refreshes, or a fresh bare
access token, which `__mint` passes through with a 50-minute expiry. mailbox
cannot narrow either value's token scopes, so each class the person chose needs
a tier of its own whose only Gmail scope is that class's: `gmail.readonly` for
read, `gmail.modify` for write, `gmail.send` for send. Pick a candidate for
each from the tier descriptions in `accounts.json`, then query Google's
token-info endpoint for the released token's Gmail scopes. The token stays in
the pipeline: it is not printed or placed in an argument.

```bash
gws_account='<gws account>'
tier='<tier>'
eval "$(jq -r --arg a "$gws_account" --arg t "$tier" '
  def tok: ascii_upcase | gsub("[^A-Z0-9]"; "_");
  (.[$a].tiers[$t].command // error("no tier \($t) on \($a)")) + [env.HOME + "/.local/bin/mailbox", "__mint", "--env", "GWS_\($a | tok)_\($t | tok)_OAUTH"] | @sh' ~/.config/gws/accounts.json)" |
  jq -jer '.access_token' |
  curl -fsS -G --data-urlencode 'access_token@-' https://oauth2.googleapis.com/tokeninfo |
  jq -er '.scope | split(" ") | map(select(startswith("https://www.googleapis.com/auth/gmail.") or . == "https://mail.google.com/")) | join(" ")'
```

When no tier's list is exactly one class's scope, tell the person which class lacks a tier and ask whether to add one (the gws-accounts skill's "Mint a JSON credential for a tier" shows how) or to drop that class.

**Check:** every `login` account's list holds every scope it needs, and every class of a `tiers` account has a tier whose list is exactly that class's one scope.

## 5. The token helper and config.toml

**The helper** (only for accounts marked `login`) asks gws-accounts for the account's login and trades it at Google for an access token narrowed to one scope. It prints the `{"access_token", "expiry", "scope"}` object that mailbox reads from a credential command. The login passes only through pipes: never argv, a file, or the output.

Write it at `<helper path>`. A file there that this guide did not write stops the block:

```bash
helper='<helper path>'
if [ -e "$helper" ] && [ "$(sed -n 2p "$helper")" != "# mailbox-gws-token: written by mailbox's INSTALL.md" ]; then echo "$helper is someone else's: ask the person for another path" >&2; false; else
mkdir -p "$(dirname "$helper")"
cat >"$helper" <<'EOF'
#!/usr/bin/env bash
# mailbox-gws-token: written by mailbox's INSTALL.md
# mailbox-gws-token ACCOUNT SCOPE: print an access token for the gws-accounts
# account ACCOUNT, narrowed to one Gmail scope (gmail.readonly, gmail.modify or
# gmail.send), as the JSON object mailbox reads from a credential command.
set -euo pipefail
[[ $# -eq 2 ]] || { echo "usage: mailbox-gws-token ACCOUNT SCOPE" >&2; exit 2; }
export PATH="$HOME/.local/share/gws-accounts/bin:$HOME/.local/bin:$PATH"
err="$(mktemp)"
trap 'rm -f "$err"' EXIT
response="$(
  GWS_ACCOUNT="$1" gws auth export --unmasked 2>"$err" |
    jq -r --arg scope "https://www.googleapis.com/auth/$2" \
      '@uri "grant_type=refresh_token&client_id=\(.client_id)&client_secret=\(.client_secret)&refresh_token=\(.refresh_token)&scope=\($scope)"' |
    curl -sS --data @- https://oauth2.googleapis.com/token
)" || { grep -v '^Using keyring backend' "$err" >&2; echo "mailbox-gws-token: could not read the gws login for account $1" >&2; exit 1; }
jq -ce 'select(.access_token) | {access_token, expiry: ((now | floor) + .expires_in | todate), scope}' <<<"$response" ||
  { echo "mailbox-gws-token: Google refused a $2 token for account $1: $(jq -r '.error // "no access_token"' <<<"$response")" >&2; exit 1; }
EOF
chmod 0755 "$helper"
fi
```

**Check**, for every `login` account and every scope it needs, printing only the fields that are not the token:

```bash
'<helper path>' <gws account> gmail.readonly | jq -c '{token: (.access_token | length > 0), expiry, scope}'
```

It prints `"token":true`, an expiry about an hour away, and exactly the scope asked for. `invalid_scope` means the login lacks that scope: go back to step 4.

**The config file.** mailbox refuses a config file that is not a regular file owned by the person, or that is group- or world-writable. Create it, or check the existing one:

```bash
cfg='<config path>'
mkdir -p "$(dirname "$cfg")"
if [ -e "$cfg" ]; then
  [ -f "$cfg" ] && [ -O "$cfg" ] && chmod go-w "$cfg" && echo "existing: $cfg"
else
  (umask 077 && : >"$cfg") && echo "created: $cfg"
fi
```

It prints `existing:` or `created:`. If it prints nothing, `$cfg` is not a regular file the person owns: stop and tell them.

Add a table for each account, once per account. A mailbox account name must match `[A-Za-z0-9_-]+`, and two may not differ only in case: use the gws-accounts name when it fits, and otherwise a short name such as `personal` (for a gws account named `alice@gmail.com`). Set `access` to `read`, `organize` or `send` from question 2. For an account marked `tiers`, also set the tiers step 4 chose for its classes; the block ignores them for a `login` account, whose commands run the helper.

```bash
cfg='<config path>'
helper='<helper path>'
mailbox_name='<mailbox account>'
gws_account='<gws account>'
access='<read|organize|send>'
read_tier='<tiers only: read tier>' write_tier='<tiers only: write tier>' send_tier='<tiers only: send tier>'
cmd() {  # cmd SCOPE TIER: one credential command as a TOML array
  jq -ce --arg a "$gws_account" --arg t "$2" --arg s "$1" --arg h "$helper" --arg m "$HOME/.local/bin/mailbox" '
    def tok: ascii_upcase | gsub("[^A-Z0-9]"; "_");
    if .[$a] | has("tiers") then (.[$a].tiers[$t].command // error("no tier \($t) on \($a)")) + [$m, "__mint", "--env", "GWS_\($a | tok)_\($t | tok)_OAUTH"]
    else [$h, $a, $s] end' ~/.config/gws/accounts.json
}
table="[accounts.$mailbox_name]"
add() { local v; v="$(cmd "$2" "$3")" && table="$table
$1 = $v"; }
add read_credential_cmd gmail.readonly "$read_tier" && case "$access" in
  read) ;;
  organize) add write_credential_cmd gmail.modify "$write_tier" ;;
  send) add write_credential_cmd gmail.modify "$write_tier" && add send_credential_cmd gmail.send "$send_tier" ;;
  *) echo "access must be read, organize or send" >&2; false ;;
esac || table=""
existing="$(awk -v h="[accounts.$mailbox_name]" '$0 == h {on = 1; print; next} /^\[/ {on = 0} on && NF' "$cfg")"
if ! printf '%s' "$mailbox_name" | grep -qE '^[A-Za-z0-9_-]+$'; then
  echo "'$mailbox_name' is not a valid mailbox account name"
elif [ -z "$table" ]; then
  echo "could not build the table: fix what the error above names"
elif [ "$existing" = "$table" ]; then
  echo "[accounts.$mailbox_name] is already as it should be"
elif [ -n "$existing" ]; then
  echo "[accounts.$mailbox_name] exists and differs: keep it or replace it by hand, as the person said in step 1"
elif grep -qixF "[accounts.$mailbox_name]" "$cfg"; then
  echo "another account's name differs from '$mailbox_name' only in case: choose another name"
else
  printf '\n%s\n' "$table" >>"$cfg" && echo "added [accounts.$mailbox_name] ($access)"
fi
```

It prints `added`, or `already as it should be` on a second run. On `exists and
differs`, do what the person chose in step 1: leave the table, or replace its
lines with the ones the block would write (`printf '%s\n' "$table"` shows them;
they hold no secret). A `tiers` account's commands run its tier's command around
`mailbox __mint`, which accepts either the JSON login or bare access token that
the tier releases.

Last, with more than one account in the file, a `default_account` line must come before the first table. If the file has none, add the one the person chose:

```bash
cfg='<config path>'
default='<default mailbox account>'
if grep -qE '^default_account[[:space:]]*=' "$cfg"; then grep -E '^default_account[[:space:]]*=' "$cfg"
elif grep -qxF "[accounts.$default]" "$cfg"; then
  printf 'default_account = "%s"\n' "$default" | cat - "$cfg" >"$cfg.new" && cat "$cfg.new" >"$cfg" && rm "$cfg.new" && echo "default_account = \"$default\" added"
else echo "no [accounts.$default] table in $cfg"; fi
```

It prints the `default_account` line the file now has. If that is not the person's choice, do what they said in step 1: leave it, or change its value by hand.

mailbox does not expand `~` or `$HOME` in a command, which is why the tables hold absolute paths. For one read-only account, the file looks like:

```toml
default_account = "work"

[accounts.work]
read_credential_cmd = ["/home/alice/.local/bin/mailbox-gws-token","work","gmail.readonly"]
```

**Check:** `ls -l '<config path>'` shows the file not group- or world-writable (mode `-rw-------` for a new one), `grep -c '^\[accounts\.' '<config path>'` counts every account, and `mailbox status --text </dev/null` names every account, each with its profile email and no error.

## 6. Verify

When this step's check passes, tell the person, in one line, how they use it: `mailbox` in a terminal opens the inbox; agents run one-shot commands such as `mailbox inbox --json` and `mailbox search 'from:alice newer_than:7d'`, and `mailbox send` is a dry run until `--send`.

Run the commands an agent will, with no terminal attached, for every account:

```bash
mailbox --account <mailbox account> status --json </dev/null | jq -r '.accounts[] | "\(.name): \(.profile.email) \(.error)"'
mailbox --account <mailbox account> inbox --max 5 --json </dev/null | jq -r '"\(.threads | length) threads", (.threads[] | .subject)'
```

**Check:** the first command prints the account's email with no error after it, and the second prints a thread count and up to five subject lines from the person's inbox. A `needs_read_credential` error names the config key to fix; a message from `mailbox-gws-token` says what it could not do.
