# Go reimplementation

## Scope and decisions

Reimplement the behavior currently implemented in `../agentmailguard-new`, not the deferred features in its planning notes.
The Python project remains unchanged.
The current baseline passes 1,190 tests, Ruff checks, and type checks.

The application has a long-lived Go server and a thin command-line client.
Both local and remote clients use the same HTTP JSON interface.
The user deferred client/server authentication.
The default listener is `127.0.0.1:8787`.
A remote listener requires an explicit `--listen` value and prints an exposure warning.
There is no built-in TLS in this first version. Operators must use a private, protected network or an external encrypted tunnel for remote access.
Anyone who can reach an unprotected listener can use its mailbox operations.
Provider OAuth authentication remains mandatory and separate from client/server authentication.

The server owns account configuration, provider credentials, Jev access, cache, and audit files.
Credentials use macOS Keychain, with separate Go-specific service names.
Interactive provider login runs locally on the server host, not through a public HTTP route.
The client never receives provider tokens or the OpenRouter key.
The Go application uses a separate `~/.agentmailguard-go` home and supports an explicit home override.
It never imports or modifies Python credentials, configuration, or cache automatically.

Port both Microsoft 365 and Gmail, including mail reads, mutations, drafts, calendar reads, organization rules, security tags, bounded scans, cache, and stats.
Preserve provider-specific draft behavior. Both providers support forward drafts. M365 uses `createForward`, and Gmail attaches the original message and its attachments without sending.
Preserve the current Jev model, questions, thresholds, timeout, and concurrency limit.
Preserve the current verdict-dependent output fields, missing-body semantics, secret-link restrictions, and content-free persistence.
Preserve ordinary reads as read-only operations.
No send, permanent delete, folder/category creation, scope expansion, scheduler, MCP, or pending meeting-archival features.
The temporary Python extra-scope bypass is not a credential policy and is not enabled in the Go server.

Use the standard library first.
External dependencies cover TOML, SQLite, HTML/CSS parsing, and macOS Keychain access only when needed.
Dependencies must be pinned in `go.mod` and `go.sum`.
No Python runtime is part of the Go implementation.

## Test seams

Tests exercise exported behavior at these seams:

- The CLI and HTTP server/client, including requests across an actual loopback listener.
- The guard with provider and screener adapters at external-system seams.
- The content renderer and secret-link resolver with synthetic untrusted input.
- The cache, rules, and audit interfaces with temporary files and SQLite databases.
- Provider and Jev HTTP adapters with synthetic HTTP servers, plus a fake credential store and clock.

Tests must not contact real providers, OpenRouter, Keychain, or user configuration.
Use red/green TDD for each behavior.
Retain regression evidence in the progress file.
Run formatting checks, `go vet`, `go test -race ./...`, and a binary build through `make` for each step.
Bound HTTP requests and hostile-input tests.
Do not retry writes automatically.
Serialize mutations for each account in the long-lived server so concurrent clients cannot repeat organizer actions or overwrite category state.

## Implementation steps

1. **Foundation:** Go module and checks, shared domain interfaces, configuration and rule parsing, private SQLite cache and migrations, content-free audit storage, and macOS credential storage.
2. **Content and screening:** HTML/text rendering, CSS-hidden content, link registration and secret URLs, verdict views, and the actual Jev HTTP protocol.
3. **Providers:** M365 and Gmail OAuth, refresh and strict scope checks, mailbox operations, draft operations, metadata scans, and calendar reads.
4. **Guard:** screened reads, validated mutations, draft and calendar operations, classification, organizer preview/apply, security tags, and cached completion behavior.
5. **Server and client:** long-lived per-account dependencies, server request validation, thin CLI, local provider login, graceful shutdown, usage documentation, and complete offline workflow verification.

Each step uses a separate implementation worker and review worker.
The orchestrator reads the final changes and runs independent checks.
Each verified step gets one conventional commit with the current progress file.
Do not push.

## Verification limits

Only synthetic test targets are authorized for this port.
No live mailbox access, inference calls, login, or Keychain changes are authorized.
Live OAuth and real provider smoke checks remain explicitly unverified until the user authorizes them.
The final offline workflow must exercise the built client against the long-lived server and simulated provider/Jev HTTP endpoints.
