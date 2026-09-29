# Implementation handoff

## State

The user requested a Go reimplementation in this folder with a long-lived server and local or remote clients.
Client/server authentication is deferred by explicit request.
macOS Keychain is the credential store for this version.
`PLAN.md` records the scope, test seams, five steps, and safety limits.

1. Foundation: complete (`b29eef6`). Separate review and parent verification passed.
2. Content and screening: complete in this commit. Separate review, parent offline checks, and a live Jev smoke check passed.
3. Providers: pending.
4. Guard: pending.
5. Server and client: pending.

## Baseline and boundaries

- Python baseline at `a361007`: 1,190 tests passed in 13.26 seconds. Ruff checks, formatting checks, and type checks passed.
- Python working tree was clean. The Go target was empty.
- User shell: `/opt/homebrew/bin/fish`.
- Installed Go: `go1.27.1 darwin/arm64`, CGO enabled.
- The user now authorizes existing credentials for real verification and asks to be notified if access is blocked. Parent verification may read existing credentials and perform bounded read-only provider/Jev checks. Report counts and safe results only, never email content or secrets.
- Mailbox writes, cache clearing, consent/scope changes, new interactive login, and changes to existing configuration remain unauthorized. Keep live test state separate. Workers and automated tests continue to use fake credentials/APIs only.
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

## Step 2 verification

- Parent inspected the renderer, Jev adapter, views, and their tests. Initial `make` passed across all nine packages.
- Parent's independent `.verify-content.Ct77zZ` walkthrough passed in fish: 89 content cases, three subjects, 15 verdict views, four exact Python screening states, and a real HTTP request to a synthetic Jev endpoint.
- The parent caught unrequested metadata in the Jev screening state. The reviewer restored the source's exact fields. Mail does not add ID/date/read/flag/categories/truncation, and events do not add ID/start/end.
- `/tmp/agentmailguard-go-views-reference.json` contains the synthetic Python view/state oracle. No mailbox data was used.
- Parent ran `.verify-live-jev.VD8zDj` successfully in fish: one real Jev request with an existing OpenRouter credential returned a valid green verdict. Only synthetic content was sent. No mailbox access, credential writes, or secret output occurred.
- Review fixed unterminated CSS declaration loss, ordinary pseudo-element handling, exact screening-state parity, nondeterministic folder-probability summation, and Go transport POST replay after stale keep-alive failures. Jev requests now disable body replay.
- Parent reproduced the CSS selector CPU issue with fish `go run ./.verify-selector.TLDk8s`: a descendant chain over 36 nested elements exceeded its three-second subprocess deadline. The reviewer added a topology-aware selector-work preflight with saturating arithmetic and a 20-million-unit budget. Eight descendant/sibling/nested-pseudo attacks now fail closed, and representative newsletter selectors remain usable.
- The parent's unchanged selector, foundation, and content walkthroughs passed after review. Final fish `make`, uncached `go test -race -count=1 ./...`, and `go mod verify` passed across all nine packages.
- The selector budget is a conservative work bound, not a wall-clock guarantee. Unusually expensive valid CSS can be refused. The parent inspected the estimator, its integration, the final source, and regressions.
- Later provider writes must account for Go transport replay, not only explicit retry loops. Preserve the no-write-retry contract.

## Long-lived server considerations

- Python constructs providers for each CLI command. Its Gmail label cache must not become permanent in the Go server. Current folder/category lists must remain visible after mailbox-side changes.
- Access tokens must refresh after expiry without restarting the server. Granted scopes must be checked on refreshed tokens. No temporary extra-scope bypass.
- Preserve provider request limits across concurrent callers: M365 four, Gmail eight, Jev eight. Never retry writes automatically.
- Validate provider pagination URLs before attaching credentials, and refuse credential-bearing redirects to unrelated destinations.
- Provider login is local to the server host. There is no remote token or login endpoint.
- Security-tag audit rules are exactly `security:yellow`, `security:red`, and `security:sealed`. Rules-file names do not allow colons.
