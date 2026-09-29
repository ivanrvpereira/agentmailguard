# Implementation handoff

## State

The user requested a Go reimplementation in this folder with a long-lived server and local or remote clients.
Client/server authentication is deferred by explicit request.
macOS Keychain is the credential store for this version.
`PLAN.md` records the scope, test seams, five steps, and safety limits.

1. Foundation: complete in this commit. Separate review and parent verification passed.
2. Content and screening: pending.
3. Providers: pending.
4. Guard: pending.
5. Server and client: pending.

## Baseline and boundaries

- Python baseline at `a361007`: 1,190 tests passed in 13.26 seconds. Ruff checks, formatting checks, and type checks passed.
- Python working tree was clean. The Go target was empty.
- User shell: `/opt/homebrew/bin/fish`.
- Installed Go: `go1.27.1 darwin/arm64`, CGO enabled.
- No live mailbox access, Keychain access, provider login, or inference is authorized.
- Source documentation is stale in places. Current source and tests define implemented behavior.
- The exploration agent's estimates and some command/schema claims were incorrect. Do not use its summary as a specification.

## Verification

The parent ran `make -C /Users/ivanpereira/code/ivanrvpereira/agentmailguard-go` in fish after implementation. Formatting, vet, race tests, and package builds passed.
The independent native walkthrough `.verify-foundation.pcY5Dj/main.go` passed in fish with temporary state and a fake Keychain platform. It covers ordered config, rules, private cache persistence, independent markers, account isolation, stats, fixed-field audit, credential namespaces, and clear.
The four Go migration scripts produce the same final SQLite schema as the Python scripts. Their comments and formatting differ.
The user's global gitignore excludes `credentials/`. A narrow repository exception includes the Go source files, not credential artifacts.
Fish has a `cd` function that calls an unavailable `z` command. Use `builtin cd` or `make -C` in future walkthroughs.
The implementation worker recorded red/green coverage for rule validation, NaN score refusal, invalid schema versions, cancellation, and safe credential errors.
The separate reviewer fixed secret-format leakage, unsafe persistence metadata, invalid cached answers, and duplicate rule parsing.
Parent verification caught review regressions for `security:<verdict>`, fixed `failed`, and `render ValueError`. The reviewer added failing regressions and fixed them. The parent's unchanged walkthrough then passed.
Final parent fish checks passed: `make`, uncached `go test -race -count=1 ./...`, `go mod verify`, and the native foundation walkthrough. All six packages passed. The parent inspected all source/tests and the final staged file list.
Native Keychain integration remains unverified. The supported macOS build requires CGO.
Error persistence accepts safe type/status/code text only. Later adapters must generate these codes, never pass through `error.Error()` from network or renderer failures.
The parent generated `/tmp/agentmailguard-go-reference.json` from the unchanged Python implementation in fish.
It contains 89 synthetic content examples, three subject examples, and the exact Jev model/questions.
No email content, credentials, or network access was used.
The source confirms that both providers implement forward drafts. M365 uses `createForward`; Gmail creates a multipart draft with the original message attached.
