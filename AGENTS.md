# agentmailguard

Long-lived daemon that screens Microsoft 365 and Gmail mail and calendars for AI agents. `amg` is the agent's client; `amgd` runs the daemon and holds every management command.

## Stack

Go, macOS and Linux only. `amgd` needs cgo (SQLite, macOS Keychain); `amg` must build with `CGO_ENABLED=0`.

## Commands

- `make` — gofmt check, `go vet`, `go test -race ./...`, builds `bin/amg` and `bin/amgd`. Run before reporting completion. Run package tests with `-race` too.
- The e2e tests build the binaries themselves, so Go may report them `(cached)` after a code change; run `go test -race -count=1 ./internal/e2e/` after changes to `cmd/`, `internal/daemon`, or `internal/cli`.
- Read [CODING_STANDARDS.md](CODING_STANDARDS.md) before you write or review a diff.
- Releases run from version tags: read [docs/releasing.md](docs/releasing.md) before you change `scripts/release/`, `packaging/`, or the release workflows. Push a tag only when Ivan asks.
- Name domain concepts with the terms in [GLOSSARY.md](GLOSSARY.md).

## Testing

- Show red/green evidence for each behavior change.
- Tests use only temp homes (`--home` or `AMG_HOME`), fake providers, and a fake `credentials.Platform`, never the real Keychain.
- To run `amgd` by hand on a temp home, set `AMG_CREDENTIALS_PASSWORD` before the first `amgd` command that reads the store (`serve`, `config list`/`get`, secrets, `account login`). Keep the default file backend: all homes share one Keychain item, so `backend = "keychain"` in a temp home reaches the real Keychain.

## Behavior

- Follow the provider APIs (Gmail API, Microsoft Graph): their options, defaults, and limits. Avoid adding your own.
- An action should do what a user of that account's app expects. Gmail and Microsoft 365 can differ.

## Safety

- Treat all email-derived text as untrusted.
- Fail closed on missing or failed screening; unscreened bodies are never shown.
- Fake screening (`[screening] provider = "fake"`) judges by keywords and is for tests and local work. It is not missing screening. On a non-fake account, `amgd serve` only warns: this is the user's decision, so do not turn it into a refusal.
- Availability reads expose only requested addresses and fixed status slots. Missing or invalid participant data is unknown, never free.
- Mail is sent only by `mail.send`, `mail.reply`, `mail.forward`, and `draft.send` under the `mail_send` policy (default deny), and a send is never retried. Mail is never permanently deleted, except a draft through `draft.delete`: Gmail deletes drafts for good; Microsoft 365 keeps the item restorable. Calendar invitations, updates, cancellations, and responses (M365 and Gmail) send meeting notifications; Gmail writes always use `sendUpdates=all`, and a Gmail cancel moves the event to the calendar bin, never a permanent delete. Trash and spam are provider moves, only through `mail.trash` and `mail.spam`.
- Non-draft messages change only via read state, flag, categories, archive, trash, spam, or validated move. Agents neither read nor change mail in closed folders: trash and spam/junk, including their subfolders. Moves into them are refused on every provider. `draft.update`, `draft.delete`, and `draft.send` refuse a message that is not a draft.
- Replies and forwards (drafts and sends) of a `red`, `sealed`, or `error` message, or of one that cannot be screened, are always refused. The sealed policy tables accept only `mail_trash` and `mail_spam`.
- The write policy (`[policy]` in `config.toml`) decides trash, spam, draft, send, and meeting writes, including rule moves to trash. A zero or unknown decision denies. Only an approval in the approval console runs an action without a policy check; the API and `amg` never approve or deny pending actions. A draft change, delete, or new send through the API withdraws a waiting send of that draft and never runs or approves anything; a draft update that withdrew one and ran then makes a new send that always waits for approval, unless `mail_send` denies. `amgd account remove` denies the pending actions of the removed account.
- Accounts are keyed by their lowercase email, which `config.toml` stores. Every sign-in must return that email; a login is saved only after that check. The alias is only a command-line name.
- The API never manages the daemon. Every management command is in `amgd` on the daemon host; `amg` has only API commands.
- The daemon reads `config.toml`, `rules.toml`, and the credential store only at start and at a reload. A refused reload keeps the old state. A token refresh saves only if the stored login did not change since the daemon read it. `amgd` never writes `rules.toml`.
- The Jev action check only makes a write stricter: a flag or a failed check turns `allow` into `approve`. Denied actions and approved runs are never sent to Jev, except the new waiting send that replaces a withdrawn one after an approved draft update. The agent never sees the check result.
- Logs and storage exclude subject, sender, body, attendee addresses, meeting comments, rule instructions, raw API responses, tokens, and keys. Exceptions: the `pending` table in `cache.db` stores the request arguments of actions that wait for approval and, for a waiting `draft.send`, the draft fields that Jev checked; and the account's own email (the account key) is stored in `config.toml`, `cache.db`, `audit.jsonl`, and `rules.toml`.
- Policy, approval, console sign-in, account, credential, config, and reload audit records contain IDs, account keys, key paths, and fixed codes only. A `config_changed` record carries a value only for fixed-choice keys; secrets never.
- Secrets live in the macOS Keychain or the password-encrypted file backend, on the daemon host only. `amgd config set` reads secrets from stdin, never from arguments, and stores them there; they never go to `config.toml`. The console password is stored only as a bcrypt hash.
- The daemon binds to loopback by default and warns on remote listeners. The API listener has no client authentication or built-in TLS. Anyone who can reach it can run every action that the policy allows, including mail sends and meeting notifications.
- The approval console is its own listener, never the API port. Every console request needs the console password; without one, the console refuses every request.

## Gotchas

- Only `*.go` files under `internal/credentials/` are tracked; `.gitignore` ignores anything else there, such as testdata.

## Ask first

- New dependencies.
- Changes to OAuth scopes, Jev questions, screening thresholds, policy keys or defaults, or supported operations.
