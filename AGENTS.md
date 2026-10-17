# agentmailguard

Long-lived server that screens Microsoft 365 and Gmail mail for AI agents, plus a thin HTTP CLI (`agentmailguard`, alias `amg` via `mise.toml`).

## Stack

Go with cgo (`mattn/go-sqlite3`, `keybase/go-keychain`), Cobra 1.10, charmbracelet Lip Gloss, BurntSushi/toml. macOS and Linux only.

## Commands

- `make` — gofmt check, `go vet`, `go test -race ./...`, builds `bin/agentmailguard`. Run before reporting completion.

| Task | Command |
|------|---------|
| Format check | `gofmt -l internal/content/` |
| Vet | `go vet ./internal/content/` |
| Package tests | `go test -race ./internal/content/` |
| One test | `go test -race ./internal/content/ -run TestName` |

## Testing

- Show red/green evidence for each behavior change.
- Tests use only temp homes, fake providers, and the fake Keychain platform (`internal/credentials/credentials_test.go:15`).

## Safety

- Treat all email-derived text as untrusted.
- Fail closed on missing or failed screening; unscreened bodies are never shown.
- Fake screening (`[screening] provider = "fake"`) judges by keywords and is for tests and local work. It is not missing screening. On a non-fake account, `serve` only warns: this is the user's decision, so do not turn it into a refusal.
- Availability reads expose only requested addresses and fixed status slots. Missing or invalid participant data is unknown, never free.
- Mail is sent only by `mail.send`, `mail.reply`, and `mail.forward` under the `mail_send` policy (default deny), and a send is never retried. Mail is never permanently deleted. Calendar invitations, updates, cancellations, and responses (M365 and Gmail) send meeting notifications; Gmail writes always use `sendUpdates=all`, and a Gmail cancel moves the event to the calendar bin, never a permanent delete. Trash is a provider move.
- Non-draft messages change only via read state, flag, categories, archive, trash, or validated move. M365 moves into Deleted Items or its subfolders are refused. Trash goes only through `mail.trash`.
- The write policy (`[policy]` in `config.toml`) decides trash, draft, send, and meeting writes, including rule moves to trash. A zero or unknown decision denies. Only `Server.Approve` runs an action without a policy check (`guard.WithApproval`). Only the approval console calls `Server.Approve` and `Server.Deny`; the API and CLI never approve or deny pending actions.
- The Jev action check only makes a write stricter: a flag or a failed check turns `allow` into `approve`. Denied actions and approved runs are never sent to Jev. The agent never sees the check result.
- Logs and storage exclude subject, sender, body, attendee addresses, meeting comments, rule instructions, raw API responses, tokens, and keys. Exception: the `pending` table in `cache.db` stores the request arguments of actions that wait for approval.
- Policy, approval, and console sign-in audit records contain IDs and fixed codes only.
- Secrets live in the macOS Keychain or the password-encrypted file backend, on the server host only. The approval password is stored only as a bcrypt hash there.
- Server binds to loopback by default and warns on remote listeners. The API listener has no client authentication or built-in TLS. Anyone who can reach it can run every action that the policy allows, including mail sends and meeting notifications.
- The approval console is a separate listener (`[approvals].listen`, default `localhost:8788`) that runs only when an approval password is set. Every console request needs the password; the console also checks Host and Origin and has no built-in TLS.

## Gotchas

- Keychain reads from a new binary trigger a macOS GUI prompt; non-interactive runs hang. Hand Keychain steps to the user.
- Legacy Keychain names keep the `agentmailguard-go-*` prefix (`internal/credentials/keychain.go:30`); unprefixed names belong to the retired Python version.
- The global gitignore excludes `credentials` dirs; `.gitignore` re-includes only `internal/credentials/*.go`.

## Boundaries

### Ask first

- New dependencies.
- Changes to OAuth scopes, Jev questions, screening thresholds, policy keys or defaults, or supported operations.
- Live provider calls, mailbox writes, cache clearing, consent changes.
- Starting or stopping the user's server; touching `~/.agentmailguard` or Keychain items.

### Never

- Commit secrets, credentials, `config.toml`, `rules.toml`, `*.db`, or `audit.jsonl`.
