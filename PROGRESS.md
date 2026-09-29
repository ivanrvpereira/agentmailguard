# Implementation handoff

## State

The user requested a Go reimplementation in this folder with a long-lived server and local or remote clients.
Client/server authentication is deferred by explicit request.
macOS Keychain is the credential store for this version.
`PLAN.md` records the scope, test seams, five steps, and safety limits.

1. Foundation: complete (`b29eef6`). Separate review and parent verification passed.
2. Content and screening: complete (`e45ad17`). Separate review, parent offline checks, and a live Jev smoke check passed.
3. Providers: complete (`a3d9158`). Four independent reviews, parent probes, and combined offline checks passed. Gmail live reads passed. M365 live reads remain blocked because a fresh restricted refresh still returns extra scopes; the token was refused.
4. Guard: complete (`d2be314`). Both reviews, all seven fixes, and final parent verification passed.
5. Server and client: complete. All seven review fixes and final parent verification passed. This commit completes the five-step offline implementation.

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

## Guard implementation (step 4)

- Parent reread all 776 lines of the current Python Guard. The approved behavior includes security-only ordinary reads, uncached drafts/events, missing-question classification internal to organization, and verdict-independent manual mutations/draft creation.
- Preserve cached hidden-body avoidance, cached-green render without Jev, error retry, exact half-open scans and fractional scan timestamps for ages, green-only business actions, categories before moves, and independent fixed security tags/completion markers.
- Preserve organizer audit normalization: requested IDs only, missing/duplicate results fail, fixed failure codes, whole-call organizer failure audited while ordinary whole-call failure is not. Tag eligibility excludes scan failures, not merely organization failures.
- Long-lived mutation serialization must cover organize/application without deadlocking when the organizer invokes mutation helpers. Avoid reacquiring the same per-account permit inside an already serialized organize call.
- Provider step committed as `a3d9158`. Worker `25f46381-9a5e-4f1` owns only `internal/guard/**` and is implementing the approved public Guard seam with synthetic adapters, temporary real cache/audit, and a clock. No staging, commits, shared-package edits, live services, or user state access are permitted.
- Parent generated `/tmp/agentmailguard-go-guard-reference.json` from the unchanged Python public Guard seam using `/tmp/agentmailguard-go-guard-reference.py`. All 29 synthetic oracle scenarios executed successfully in temporary state, with no network or credentials.
- The oracle covers cached preview/apply, repeated completion, green no-op, category thresholds, folder display names, missing targets, fractional age boundaries, missing/duplicate/unsolicited mutation results, action failures, fixed tags and retags, scan failures, half-open bounds, duplicate scan entries, empty rules, mixed verdicts, and the 50-ID batch boundary.
- Parent ran the unchanged independent Go consumer `.verify-guard.EYAAqD/main.go`. All 29 scenarios match the Python reports, external actions, metadata reads, cache markers, and content-free audit.
- Parent read all eight production files and nine test files in the completed Guard package. Its public signatures match the agreed contract, with the existing `views.CalendarView` result type. Source hardening rejects unexpected fetched IDs and ignores stale cached verdicts for known drafts.
- Parent created and ran `.verify-guard-http.EKQbEz/main.go` with the race detector. Synthetic Graph HTTP flows through the real M365 adapter, Guard, real Jev HTTP adapter, temporary cache, and guarded views. Five verdicts, cached hidden-body avoidance, cached-green reuse, error retries, missing-body refusal, uncached drafts/events, private-event body exclusion, secret links, summaries, and missing-question classification passed. Only loopback servers, synthetic credentials/data, and temporary files were used; no mailbox mutations or mutation audit occurred.
- Parent ran `make`, uncached Guard race tests, then uncached whole-tree race tests across twelve packages, module verification, and unchanged foundation/content/selector walkthroughs. All passed. Python working tree remains clean.
- Separate read-only reviews completed against the staged `internal/guard/**` addition at `a3d9158`: Standards `bd3a660f-446b-462` and Spec `74bb732b-ea70-4d2`. No Step 4 commit yet.

### Guard Spec review fixes

- Spec reported two P2 issues. Batch classification, mail summaries, and calendar lists process independent screening serially, contrary to Python's gathered work and explicit peak-eight test. Fixed manual Move/Flag/empty-Categorize refusal reasons also collapse to bare `GuardError`.
- Parent read the full report and probes, confirmed every target Guard source/test matches the review snapshot, and reran `go test -race -count=1 ./internal/guard -run '^TestSpec' -v` in `/tmp/guard-spec.Pj6tEl`. All reported failures reproduced. Forty synthetic classification requests reached peak one and took 1.17 seconds; List/CalendarList reached peak one. The fresh partial-answer/private-event probe passed.
- Report: `/tmp/agentmailguard-guard-spec-review.md`. Original unchanged probes: `/tmp/guard-spec.Pj6tEl/internal/guard/{spec_probe_test.go,spec_read_parallel_probe_test.go}`. The reviewer also ran eight unchanged Python tests successfully in a disposable source copy.
- Fixer `e225db0a-ca8c-47c` owns only `internal/guard/**` working-tree changes. Restore bounded eight-worker screening with ordered results and cancellation/drain; preserve account mutation serialization and all fail-closed behavior. Preserve only trusted fixed refusal strings, not arbitrary provider text, and keep organizer audit codes unchanged.
- Original staged Guard snapshot stays fixed. Fixes/tests remain unstaged or untracked until final parent verification. The Standards reviewer used the fixed snapshot despite concurrent working-tree fixes.

### Guard Standards review fixes

- Parent reproduced four security/behavior findings with the unchanged original probes in `/tmp/guard-standards.ybGQvv`: Guard pointer/value formatting exposes a synthetic cached Jev key; malformed cached green with missing/.9 score exposes a body without screening; a concurrent read commits red during destination validation but organizer still moves; cancellation after a confirmed final archive drops completion and causes the next apply to repeat both writes.
- Parent independently applied both review mutants to the disposable snapshot: always pass Flagged; replace DraftUpdate body with empty text. The entire original Guard race suite still passed. Disposable source was restored; target production was never mutated. Tests need exact payload assertions at the external mailbox seam.
- Report and test inventory: `/tmp/agentmailguard-guard-standards-review.md`. Original probes: `/tmp/guard-standards.ybGQvv/internal/guard/{standards_probe_test.go,standards_format_probe_test.go}`. No real secrets were used or printed.
- Parent added `/tmp/guard-standards.ybGQvv/internal/guard/parent_security_probe_test.go` and reproduced two sibling paths: a confirmed Yellow tag loses its marker on cancellation and repeats; malformed cached green with saved organization answers authorizes business actions.
- The same active fixer `e225db0a-ca8c-47c` now owns all seven findings. Required changes: always-redacted value formatter, validation of every loaded cached screening state, coordinated security updates/final organizer authorization through provider writes, bounded cancellation-independent persistence for confirmed completion/tags, and payload-sensitive tests. Parent recommended a separate cancellable security-state gate acquired after destination/category validation, never around screening or nested acquisition of the mutation gate.
- Parent corrected a verification-fixture defect: the 29-case organizer oracle previously seeded every verdict with injection .1. Seeds now use green=.1, yellow=.35, red=.9, sealed=.1. Python oracle regeneration passed; every expected output is machine-verified identical to `/tmp/agentmailguard-go-guard-reference.before-scores.json`. Only fixture inputs and the ignored Go consumer changed; production validity rules must not be weakened for the old inconsistent fixtures.
- The fixer completed all seven findings. Parent inspected every final production change, all three new regression files, and the changes to existing tests.

### Final Guard verification

- Parent reran all unchanged Spec, Standards, and added parent probes against copies of the final source. Every probe passed. Classification, List, and CalendarList reached peak eight. Forty synthetic 25ms classification requests took 168ms, versus 1.17 seconds before the fix.
- Parent inspected the security-state gate. Cache security writes share it with final automatic authorization through the provider write. Destination/category validation and Jev screening remain outside it. Manual mutations remain verdict-independent. Current verdict checks also protect security tags and green no-op completion.
- Confirmed final writes and security tags save completion with a five-second cancellation-independent context. Partial success remains incomplete. Cancellation starts no further provider writes.
- Parent independently applied each payload mutant to a disposable copy. The constant-Flagged mutant failed the exact flag-state assertion. The empty-draft-body mutant failed the exact source/body assertion. Disposable source was restored. Target production was never mutated.
- Parent reran `make`, uncached whole-tree race tests, and 20 repetitions of concurrency/cancellation/final-authorization regressions. All passed across twelve packages. Module verification and unchanged foundation/content/selector walkthroughs passed.
- Both parent native Guard walkthroughs passed against final code: all 29 Python organizer cases and the synthetic Graph/Jev HTTP integration. Oracle assertions remain unchanged after the documented seed-score correction.
- Parent inspected the final staged file list and diff checks. Python remains unchanged. Step 5 must retain one Guard per account and one shared Jev adapter, keep classification results internal, and expose only guarded views or existing operation results.

## Server and client implementation (step 5)

- Worker `d058bc90-5f7c-47b` owns new `internal/server/**`, `internal/client/**`, `internal/cli/**`, `cmd/agentmailguard-go/**`, README, and required build/ignore changes. Existing shared packages, plans, instructions, progress, Python repositories, and verification helpers are outside its ownership. No staging or commits.
- One binary, `bin/agentmailguard-go`, supplies `serve`, a thin HTTP CLI, and local-only `auth login`. Clients default to `http://127.0.0.1:8787` and can use `--server` or `AGENTMAILGUARD_GO_SERVER`. Only server/local-login commands read the Go home. Remote clients never load provider credentials or local account configuration.
- The closed transport is JSON `POST /v1/call` with operation, optional account, and typed params. It exposes only current CLI operations, not classification state, raw provider models, tokens, keys, rules instructions, or login. Request validation precedes provider work. Browser Origin requests and CORS are refused; CLI/API clients remain unauthenticated as requested.
- Parent specified the server verification seam: `server.New(ctx, home, Options)`, implementing `http.Handler` and `Close`. Options inject credentials, screener, existing provider options, and clock for synthetic tests only. Production has one provider Client and one Guard per account, plus one shared Jev adapter.
- Parent reread all 461 Python CLI lines before briefing the worker. Existing command/options, JSON results, date semantics, and exit codes remain the source contract. A configurable client timeout, no replay/redirects, bounded HTTP input, graceful shutdown, and secret-safe errors/formatting are required.
- The worker must build and exercise the real CLI against the real server with synthetic provider/Jev HTTP and temporary state. Parent will independently verify the built client/server workflow. No live mailbox writes, new consent/login, or user configuration changes are authorized.
- The user was asked whether to approve a separate least-privilege M365 app/login. No approval arrived. Existing consent remains untouched and M365 live verification stays pending.
- Implementation completed with the agreed public seams. Parent inspected server/client/CLI code and README, ran `make`, uncached whole-tree race tests, module verification, and built-binary help. All passed. The 20-file Step 5 addition was staged as a fixed review snapshot; `PROGRESS.md` remains separate until final verification.
- Separate read-only Standards (`3c210222-3268-4f0`) and Spec (`9f2097e3-960a-49b`) reviewers own independent disposable snapshots of the staged change at `d2be314`. The target tree/index remain untouched by reviewers.
- Parent's independent `.verify-server.z9S0fZ/main.go` first passed with 49 built-CLI commands, then passed with 52 after adding exact read/unread and nonempty/empty draft-update assertions plus native serve/SIGTERM. It runs with the race detector against one long-lived server, real Graph/Jev adapters, two synthetic accounts, and temporary configuration/cache/audit. It checks five verdicts, cached hidden-body avoidance, error retry, uncached drafts/private events, secret links, list/search, rules reload, server month-clamping, account isolation, preview/apply, concurrent completion/tag suppression, exact write payloads, private audit, cache clear, and server close. No real credentials, providers, or user state were accessed.

### Server Standards review fixes

- Standards reported two P2 findings in `/tmp/agentmailguard-server-standards-review.md`. JSON/pretty stdout errors are ignored, so a built CLI with a read-only output descriptor exits 0 without delivering its result. Native mutation tests do not associate requested read/unread intent with exact payloads and exercise only empty draft-update text.
- Parent reran the unchanged original probes in `/tmp/server-standards.GgVCwz`: the stdout failure reproduced; hostile JSON, admission/disconnect, and exact HTTP-to-provider payload probes passed.
- Parent independently applied both disposable mutants. Inverted read/unread dispatch and unconditional empty draft updates each pass all original server/client/CLI race suites, while the independent intent/body probes fail. Both disposable mutations were restored. Target production was not mutated.
- Fixer `796ce95c-875f-467` owns Step 5 working-tree fixes only. Check stdout errors without repeating HTTP operations and add payload-sensitive native regressions with nonempty and explicit empty draft bodies. Original staged snapshot stays fixed. Spec review remains pending.
- Parent noticed response-write deadlines are installed only for successful results. The Standards follow-up confirmed a third P2 finding. Parent independently reproduced it in `/tmp/server-lifecycle.hWx6RU`: error-response TCP writes remained blocked for 51.141 seconds despite a 15-second ReadTimeout; successful-response writes timed out at 30.001 seconds. Fast public-handler probes fail for validation, operation, admission, closed-server, and panic errors. Report: `/tmp/agentmailguard-server-lifecycle-review.md`.
- The same fixer owns the central `fail()` response-deadline correction and public-handler regressions. Install the 30-second deadline only when emitting the response, never a handler-entry or global whole-operation timeout. Parent retains the original observational probe and added a separate positive real-TCP regression in `internal/server/parent_deadline_probe_test.go` in that disposable snapshot.

### Server Spec review fixes

- Spec reported four findings in `/tmp/agentmailguard-server-spec-review.md`. An explicit empty account falls back to the default and can mutate the wrong mailbox (P1). Unknown link syntax aborts valid sibling results (P2). Auth status drops safe scope-refusal diagnostics (P2). Existing `--no-pretty` and `--no-device-code` flags are missing (P3).
- Parent read the full report and original probes, compared the current Python status/source contracts, and reran `go test -race -count=1 ./internal/server -run '^TestSpec' -v` in `/tmp/server-spec.PwxPzr`. All four failures reproduced. The empty-account probe performed one synthetic provider mutation. The reviewer also ran 16 unchanged/reference Python cases successfully in a disposable copy.
- The same fixer now owns all confirmed Step 5 findings. Empty/blank explicit accounts must fail before HTTP or local login; omitted accounts still select the configured default. Unknown bounded link IDs must reach Guard's per-link handling. Negative boolean aliases must retain last-option-wins behavior.
- Auth diagnostics require a narrow extension to the provider error contract. Parent expanded fixer ownership only to both provider `auth.go` files and focused new public-auth tests. Provider-private typed scope errors carry defensive-copy diagnostics while preserving fixed Error strings and redacted formatting. The server may expose bounded conventional scope labels, never raw token/response/error text. This must not alter scope acceptance, refresh/persistence rules, or the M365 consent blocker.

### Final server and client verification

- Parent inspected all production fixes and the new regression tests. Blank explicit accounts fail before state access. Unknown bounded link IDs retain per-link results. Both negative boolean aliases work. Output errors return exit 1 without another HTTP request.
- Scope diagnostics retain the existing provider error strings and acceptance policies. Server output contains bounded conventional scope labels only. Malformed labels remain refused and are not shown. No refused credential is saved or used for mailbox access.
- Parent reran every unchanged Standards and Spec probe against final source copies. All passed. The empty-account probe now reports zero provider writes. The built CLI returns an error for a failed stdout descriptor.
- Parent added six independent scope-diagnostic cases in `/tmp/server-spec.PwxPzr/internal/server/parent_scope_probe_test.go`. All failed before the fix and pass afterwards. They cover saved and refreshed forbidden grants for both providers, malformed labels, zero credential saves, and zero mailbox/profile requests.
- Parent reran all unchanged fast lifecycle probes. Validation, operation, admission, closed-server, and panic responses now have deadlines. The separate positive real-TCP probe also passed: both blocked success and error writes end with timeouts. The original pre-fix observational probe remains unchanged.
- Parent independently applied both payload mutants to a disposable snapshot after the fixes. Inverted read/unread fails the exact per-command boolean/ID assertion. An erased replacement body fails the nonempty draft-update assertion. Both disposable mutations were restored. Target production was never mutated.
- Final parent checks passed: `make`, uncached whole-tree race tests across 15 tested packages, binary build, and module verification. All foundation/content/selector and Guard walkthroughs passed unchanged. This includes 89 content cases, 15 verdict views, four exact screening states, and 29 Python organizer cases.
- The final independent native walkthrough passed 52 built-CLI commands. It used two synthetic accounts, 13 Jev requests, and 14 provider writes against temporary loopback services. Server-owned state, account isolation, guarded views, concurrent organization, completion markers, exact payloads, private audit, cache clear, and native serve/SIGTERM passed.
- Parent checked README claims and added the minimum Go version, blank-account refusal, negative flags, and bounded scope-diagnostic behavior. Python remains unchanged. No dependencies, scope allowlists, Jev questions, or verdict thresholds changed.
- Offline implementation is complete. Live Gmail reads and synthetic-content Jev screening passed earlier. M365 live access remains blocked by extra grants. Real browser/device login, native Go Keychain integration, provider acceptance of generated drafts, and an individual Gmail event read remain unverified. No new consent or live mailbox-write approval arrived.

## Follow-up: safe Microsoft 365 login diagnostics

- The user explicitly approved copying `~/.agentmailguard-dev/config.toml` into the separate Go home. Parent verified an identical copy with mode 0600. No credentials or database were copied.
- The user reported a browser callback followed by the generic login error. A device-code screenshot showed a Microsoft organization access-policy refusal. The exact browser-login failure remains unconfirmed.
- The user approved safe login-stage diagnostics and explicitly prohibited commits. This follow-up stays uncommitted. No new live login, consent change, or credential access is authorized by this diagnostic task.
- Parent reproduced the missing diagnostic through exported `cli.Run`, real synthetic OAuth/Graph HTTP, the loopback browser callback, and an injected credential store. The scope-refusal regression first failed with the old generic message, then passed with the fixed stage message.
- Subsequent red/green cycles cover token-exchange HTTP/JSON/OAuth failures, profile failures, credential-save failure, malformed/expired/application tokens, browser failure/refusal, cancellation, timeout, and device-code failures. Canary values remain absent from output. Refused tokens never reach profile or credential storage.
- Provider errors retain the fixed `AuthError` text. A concrete typed diagnostic supplies only locally defined CLI messages. The credential-save distinction retains no backend error or unwrap chain. Scope rules, provider operations, and successful login output remained unchanged in this follow-up; the next follow-up changed the scope rules.
- Initial parent `make`, uncached whole-tree race tests, and diff checks passed. The CLI binary was rebuilt. Focused Standards and Spec reviews are in progress before final verification. Database-copy validation remains pending.
- Later review: refresh save failures returned `credentialSaveError`, so mailbox results reported that type name instead of `MailboxError`. `credential()` now maps it back to `authError`; only login reports the save stage. A regression test failed before the fix.

## Follow-up: extra grants warn instead of block

- The user decided extra grants are their choice: a usable token proceeds with a warning; only unusable tokens are refused. The user dropped the server-start warning.
- `m365.ExtraScopes` and `gmail.ExtraScopes` list unrequested grants. M365 application roles are accepted and reported with a `role:` prefix. Malformed tokens and malformed `roles` claims remain refused. Gmail still refuses tokens missing the mail or calendar scope and reports `missing_scopes`.
- `auth login` and `auth status` print one quoted stderr warning. `auth status` returns `ok: true`, requested `scopes`, and bounded `extra_scopes` labels.
- Refusal tests were changed to acceptance-with-warning tests. Two mutants (M365 and Gmail refusing extras again) fail the new CLI, server, and provider tests.
- Live check with user approval: browser login for `work` succeeded and warned about `Calendars.ReadWrite`, `Contacts.Read`, `IMAP.AccessAsUser.All`, `Mail.Read`, and `Mail.Send`. `auth status` returned `ok: true`. Device-code login may still be blocked by the Microsoft organization access policy recorded above.
- With user approval, the Python cache was copied read-only with SQLite `.backup` into `~/.agentmailguard-go/cache.db` (identical schema, integrity check passed; the empty Go cache was backed up to `/tmp`). `stats` reads the copied history. `rules.toml` was copied with user approval.
- The organize preview first failed screening with `ConfigError` because the Go Jev key was absent. After the user saved the key, a 7-day preview screened 26 messages green and planned 18 moves/categories. The preview made no mailbox writes; the user runs `--apply`.

## Follow-up: Jev key onboarding command

- The user approved a local `auth set-jev-key` command. It reads stdin (terminal: prompt with `/bin/stty -echo`, no new dependency), validates one printable line, and saves through `SaveOpenRouterKey`. It refuses remote targets, `--account`, and `--home`, and never prints the key.
- CLI tests cover the saved trimmed key, empty/EOF/space/control/multi-line/too-long input, remote flag and environment refusals, flag refusals, and a Keychain failure without leaking the key or backend detail. The tests failed to build before `cli.Options.Stdin` existed.

## Follow-up: credentials load at server start

- The user asked that `serve` request credential-backend access at start, because an operator is present then (a vault could fill this role on automated hosts). Decisions: refuse to start on any missing, denied, or unreadable item; prompt for a missing Jev key on a terminal; rewrite each OAuth record unchanged at start so the Keychain modify prompt also appears then.
- `server.Server.Preload` reads every configured account's OAuth record and the Jev key (skipped when `OPENROUTER_API_KEY` is set), write-probes each OAuth record, and returns fixed `CredentialCheck` items. A `memoryStore` passes through before preload; after success, loads come from memory only and saves reach the backend before memory. Accounts added after preload return the restart-required error. `serve` prints the checks before `server listening` and refuses to start on failure or missing configuration.
- Before this change, M365 reread the Keychain at each access-token expiry and Gmail/M365 wrote after each refresh. Runtime reads are now gone. Refresh writes remain; the startup write probe asks for that permission early.
- Red/green: new server tests failed to build before `Preload`, `CredentialCheck`, and `Options.LookupEnv` existed. The serve test failed ("listener did not start") before it had configuration and credentials. Five mutants (loads pass through, saves skip memory, no write probe, late accounts allowed, failures ignored) each fail the new tests.
- The terminal key prompt shares `readJevKey` with `auth set-jev-key`. Native Keychain prompt behavior was not exercised live.
- User follow-up: an account that is not logged in must not block the start. Its check reads "not logged in; commands for this account fail until auth login and a server restart", it is not write-probed, `auth status` reports `logged_in: false`, and its mail commands fail. Denied, unreadable, write-failed, missing-key, and missing-configuration results still refuse the start. Red/green: the new server test and the serve test with a missing login both failed before the change.

## Live credential preparation

- Parent read only account metadata and existing credentials from `~/.agentmailguard-dev`. It contains `work` (M365) and `gmail`; both auth records exist. `~/.agentmailguard` has no accounts.
- `/tmp/agentmailguard-go-credential-bridge.py` can provide existing credentials to a parent verification process through an inherited pipe descriptor. It never prints secrets, refreshes tokens, writes Keychain, or changes the Python cache. Default output is availability/scope metadata only.
- Gmail has an existing refresh token. M365 has an expired access token and a refresh token. The cached M365 token contains extra `Calendars.ReadWrite`, `Contacts.Read`, `IMAP.AccessAsUser.All`, `Mail.Read`, and `Mail.Send` grants.
- The expired token is not suitable for Go access. The parent attempted a fresh restricted refresh after all provider fixes. Extra grants persisted, so live M365 verification is blocked. Restricted consent needs separate user approval. Superseded: the user later decided extra grants are accepted with a warning (see the follow-up above).
- Parent live provider checks will use an in-memory credential store populated through the private pipe. Refreshed test tokens remain in memory, with no writes to existing credentials.

## Long-lived server considerations

- Python constructs providers for each CLI command. Its Gmail label cache must not become permanent in the Go server. Current folder/category lists must remain visible after mailbox-side changes.
- Access tokens must refresh after expiry without restarting the server. Granted scopes must be checked on refreshed tokens. No temporary extra-scope bypass.
- Preserve provider request limits across concurrent callers: M365 four, Gmail eight, Jev eight. Never retry writes automatically.
- Validate provider pagination URLs before attaching credentials, and refuse credential-bearing redirects to unrelated destinations.
- Provider login is local to the server host. There is no remote token or login endpoint.
- Security-tag audit rules are exactly `security:yellow`, `security:red`, and `security:sealed`. Rules-file names do not allow colons.
