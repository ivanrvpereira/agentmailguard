# Implementation handoff

## State

The user requested a Go reimplementation in this folder with a long-lived server and local or remote clients.
Client/server authentication is deferred by explicit request.
macOS Keychain is the credential store for this version.
`PLAN.md` records the scope, test seams, five steps, and safety limits.

1. Foundation: complete (`b29eef6`). Separate review and parent verification passed.
2. Content and screening: complete (`e45ad17`). Separate review, parent offline checks, and a live Jev smoke check passed.
3. Providers: complete in this commit. Four independent reviews, parent probes, and combined offline checks passed. Gmail live reads passed. M365 live reads remain blocked because a fresh restricted refresh still returns extra scopes; the token was refused.
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

## Step 3 verification log

The entries below record discovery and fixes chronologically. Final verification results follow the review sections.

- Gmail implementation added all mailbox, draft, calendar, OAuth, and refresh operations under `internal/providers/gmail`. Worker reports no stubs, synthetic red/green evidence, 86.2% coverage, and a passing `make`.
- Parent independently ran `go test -race -count=1 ./internal/providers/gmail` successfully. Separate read-only Standards and Spec reviewers are inspecting the staged Gmail change against `e45ad17` and current Python behavior.
- M365 implementation added all operations under `internal/providers/m365`. Parent independently ran combined `make` across eleven packages, uncached M365 race tests, and `go mod verify`; all passed. Separate Standards and Spec reviewers are checking its staged changes.
- Parent's synthetic public-auth probe `go run ./.verify-live-providers.om9RrU --probe-refresh-only` failed: M365 rejects a refresh-token-only credential before attempting a refresh (zero HTTP requests and zero stores). `credentials.Store` accepts this record shape. Reviewers were notified. Correcting this must preserve strict checks on the actual access token before use/store; no scope bypass is allowed.
- Parent prepared and built `.verify-live-providers.om9RrU` for both providers. It reads credentials through a private inherited pipe, forces a fresh token request into an in-memory store, allows only provider GETs and one refresh-token POST, limits requests and responses, and reports counts or fixed diagnostic enums only. It has not contacted live providers yet. The helper can distinguish overbroad refreshed grants from invalid/expired refresh credentials without exposing token responses.
- The live helper will check profile/scopes, current folders/categories, one Inbox message with metadata-only and fetched-body behavior, and a 24-hour calendar window. Empty mailboxes/calendars explicitly leave individual-item checks unexercised. No mailbox writes, cache writes, configuration changes, or Keychain writes are allowed.

### Gmail Standards review

- Reviewer read all 22 Gmail files and reported three findings: missing payload becomes fetched-empty (P1), bodyless trash retries after a zero-byte stale-connection failure (P2), and Status bypasses the eight-slot API limit (P2).
- Parent inspected the affected code and independently ran all three unchanged probes in `/tmp/gmail-standards.O8tihQ/internal/providers/gmail/standards_probe_test.go`. All failed as reported. The transport probe saw one failed write, a second connection, and one successful server-side POST; it did not show duplicate server-side writes.
- A dedicated fixer completed the three Gmail fixes. Parent inspected the changed admission/replay code and ran the original Standards probes against a snapshot of the updated production files; all three passed with the race detector. Genuine empty/attachment-only bodies and empty trash wire bodies remain supported.
- Missing top-level MIME payload validation is deliberate fail-closed hardening: Python's empty-payload fallback also accepts this malformed full response. Valid empty and attachment-only payloads keep their source behavior.
- Profile calls now share the eight-slot provider API/backoff path. Credential resolution happens before API admission, and token exchanges remain separately serialized; no nested token/API acquisition is added.
- A separate fixer owns the confirmed M365 refresh-only initialization issue, with tests for new-token scope checks and preservation of saved credentials after a failed refresh.
- Original staged provider snapshots remain unchanged while fixers edit the working tree. No live provider smoke check has run yet.
- Completed agent handles can disappear from the tool registry. The Gmail Standards report was recovered from its completed `.output` transcript after `get_subagent_result` returned not found. Other review results remain pending; do not infer them from the finished report.

### Gmail Spec review

- Reviewer reported two P2 findings: charset decoding parity and the already-known Status concurrency issue. No other proven Spec findings were reported; all Gmail production/test files and current Python references were read.
- Parent inspected Python's declared-charset decode behavior and independently ran `TestSpecPythonCharsetParity` in `/tmp/gmail-spec.UdhpFC`. Of ten synthetic cases, `cp1252`, `utf-32`, and `utf-32le` failed exactly as reported. The other seven Latin/UTF16/CJK cases passed.
- The Gmail fixer completed this fourth distinct issue using existing x/text codecs, common aliases, and UTF32 BOM/endian policies. Parent inspected the production diff and reran the unchanged ten-case charset oracle with the race detector; all passed. ISO-8859 semantics and unknown-charset UTF8 fallback remain intact. This does not establish universal Python codec parity.
- Original Spec fixtures/probes remain in `/tmp/gmail-spec.UdhpFC/{oracle.json,internal/providers/gmail/spec_probe_test.go}` for independent post-fix verification. The completed report was recovered from its transcript into `/tmp/agentmailguard-gmail-spec-review.md`.

### M365 Spec review and queued expiry

- M365 Spec reported refresh-only initialization (already assigned) and token expiry while waiting for an API slot (P2). Parent independently ran the disposable probes: all 22 Python message mappings, 10 events, and 7 KQL queries passed; the queued-request probe failed with Graph 401 and zero refreshes.
- The original probe is `/tmp/m365-spec-review.dUwxy8/spec_probe_test.go.txt`; the parent copied it to `internal/providers/m365/parent_spec_probe_test.go` in that disposable snapshot. The report is `/tmp/agentmailguard-m365-spec-review.md`.
- Parent tested the analogous Gmail path after its admission fix. `/tmp/gmail-standards.O8tihQ/internal/providers/gmail/parent_queue_expiry_test.go` failed with Gmail 401 and zero refreshes: a queued Status request used a token that expired during its wait.
- A new Gmail worker owns this regression. Carry token expiry through admission, recheck before transmission and GET retries, and release the API permit before refreshing. Preserve Login lock ordering and never retry a transmitted mutation.
- M365 needs the same admission correction, with the original token acquisition deadline preserved across Login profile work. A steering attempt to its original auth fixer failed because the handle was unavailable; wait for that completion notification before reassigning its files.
- Completed Gmail handles also disappeared after result retrieval, so the queued-expiry fix uses a fresh worker. No live provider checks have run.

### M365 Standards review and formatting

- M365 Standards reported credential exposure through Client formatting (P1) and missing unread-value assertions (P2). Parent independently reproduced both findings in `/tmp/m365-standards.TMYuw9`.
- The original formatting probe fails for `%s` and `%q` after credential caching, exposing synthetic access/refresh canaries. No real credentials were used or printed. Probe source: `/tmp/m365-standards.TMYuw9/review_probe_test.go.txt`.
- Parent changed only the disposable copy's MarkRead payload to constant `true`; the M365 race suite still passed when the separately failing formatting probe was excluded. Parent restored that disposable production change. Durable tests must assert both requested boolean values, not only mutation success.
- Parent also found Gmail's pointer-only Formatter does not protect exported Client values. `/tmp/gmail-standards.O8tihQ/internal/providers/gmail/parent_format_probe_test.go` confirmed synthetic credential leakage for value formatting. The active Gmail queue-expiry worker was notified to use a value receiver and test both values and pointers.
- M365 needs an always-redacted value-receiver Formatter too. The auth fixer's completion notification has now arrived. Parent inspected its auth.go/auth_test.go diff and independently reran the unchanged refresh-only helper and M365 race suite; both passed. A fresh M365 worker now owns queued-expiry, formatting, and unread-test fixes, preserving the verified auth change.
- M365 Standards found no security objection to refreshing missing/expired access tokens when requested scopes remain restricted and the new JWT is checked before persistence/use. Its full completed report is `/tmp/agentmailguard-m365-standards-review.md`.

### Final Gmail verification

- Parent inspected final expiry/admission, Login, and value-formatting changes and their new public-seam regressions. Expired tokens release API permits before refresh, then reacquire/recheck; no transmitted mutation retries. Transient Login tokens cannot switch to a stored identity or extend their acquisition expiry.
- Parent reran the original Standards probes, queued-expiry probe, value/pointer formatting probe, both original Spec probes, and the current uncached Gmail race suite. All passed. The final live helper was rebuilt from this source.
- Parent ran `/tmp/agentmailguard-go-live-providers-check gmail` with existing credentials and a fresh restricted token. Live read-only checks passed: 32 folders, 19 categories, one Inbox message, metadata/body distinction, and a 24-hour calendar query with zero events. Individual event Get was not exercised because the window was empty.
- The live run made 13 HTTP requests, including one token refresh. Refreshed credentials were saved only in the in-memory test store. No mailbox, cache, configuration, or Keychain writes occurred; no email content, IDs, usernames, tokens, or keys were printed or stored.
- At this point M365 live verification still waited for its final review fixes and parent checks. Real browser/device login and Keychain write/read roundtrip remain unverified.

### Final M365 and combined verification

- Parent inspected the final auth, admission, Login-deadline, formatter, and regression changes. Expired requests release API permits before authentication and reacquire/recheck afterward. Transient Login identity cannot be replaced with stored credentials. Both provider value/pointer formatters redact cached secrets.
- Parent reran the unchanged M365 formatting and queued-expiry probes and the Python mapping oracle: 22 messages, 10 events, and seven KQL queries passed. The unchanged refresh-only public-auth helper also passed.
- Parent independently changed only a disposable snapshot's MarkRead payload to constant true, then constant false. The new public-seam test failed for unread, then read, respectively. The disposable source was restored; target production was never mutated.
- Final parent fish checks passed: `make`, `go test -race -count=1 ./...`, `go mod verify`, and all unchanged foundation/content/selector/refresh-only walkthroughs. All eleven packages passed. The live provider helper was rebuilt from the final source.
- Parent ran the bounded M365 live check. The token endpoint returned HTTP 200, but the fresh token still contained extra scopes. Strict validation refused it before Graph access or credential persistence. No scope bypass, mailbox access, consent change, or existing credential/configuration write occurred.
- The user was notified of the M365 live blocker. Restricted consent or a separate least-privilege app/login requires user approval. Continue Guard/server work offline; do not retry live M365 checks without resolving the grants safely.
- Gmail live reads and synthetic Jev screening passed. M365 Graph endpoints, real browser/device login, native Go Keychain integration, and provider acceptance of generated drafts remain unverified.

## Guard preparation (step 4, not started)

- Parent reread all 776 lines of the current Python Guard. The approved behavior includes security-only ordinary reads, uncached drafts/events, missing-question classification internal to organization, and verdict-independent manual mutations/draft creation.
- Preserve cached hidden-body avoidance, cached-green render without Jev, error retry, exact half-open scans and fractional scan timestamps for ages, green-only business actions, categories before moves, and independent fixed security tags/completion markers.
- Preserve organizer audit normalization: requested IDs only, missing/duplicate results fail, fixed failure codes, whole-call organizer failure audited while ordinary whole-call failure is not. Tag eligibility excludes scan failures, not merely organization failures.
- Long-lived mutation serialization must cover organize/application without deadlocking when the organizer invokes mutation helpers. Avoid reacquiring the same per-account permit inside an already serialized organize call.
- No Guard source or tests have been written yet. Provider offline verification is complete; start Guard after the step 3 commit.

## Live credential preparation

- Parent read only account metadata and existing credentials from `~/.agentmailguard-dev`. It contains `work` (M365) and `gmail`; both auth records exist. `~/.agentmailguard` has no accounts.
- `/tmp/agentmailguard-go-credential-bridge.py` can provide existing credentials to a parent verification process through an inherited pipe descriptor. It never prints secrets, refreshes tokens, writes Keychain, or changes the Python cache. Default output is availability/scope metadata only.
- Gmail has an existing refresh token. M365 has an expired access token and a refresh token. The cached M365 token contains extra `Calendars.ReadWrite`, `Contacts.Read`, `IMAP.AccessAsUser.All`, `Mail.Read`, and `Mail.Send` grants.
- The expired token is not suitable for Go access. The parent attempted a fresh restricted refresh after all provider fixes. Extra grants persisted, so live M365 verification is blocked. Restricted consent needs separate user approval; never enable the old extra-scope bypass.
- Parent live provider checks will use an in-memory credential store populated through the private pipe. Refreshed test tokens remain in memory, with no writes to existing credentials.

## Long-lived server considerations

- Python constructs providers for each CLI command. Its Gmail label cache must not become permanent in the Go server. Current folder/category lists must remain visible after mailbox-side changes.
- Access tokens must refresh after expiry without restarting the server. Granted scopes must be checked on refreshed tokens. No temporary extra-scope bypass.
- Preserve provider request limits across concurrent callers: M365 four, Gmail eight, Jev eight. Never retry writes automatically.
- Validate provider pagination URLs before attaching credentials, and refuse credential-bearing redirects to unrelated destinations.
- Provider login is local to the server host. There is no remote token or login endpoint.
- Security-tag audit rules are exactly `security:yellow`, `security:red`, and `security:sealed`. Rules-file names do not allow colons.
