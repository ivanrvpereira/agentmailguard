# Coding standards

Read this file before you write or review a diff. It holds the judgement calls that no linter makes for this repository.

- Skip anything that `make` or `.github/workflows/ci.yml` already enforces. The tests in `internal/standards` check the mechanical rules.
- Review the code the diff adds or changes. Code that predates a rule is not a finding, and a diff does not rewrite it only to comply.
- [AGENTS.md § Safety](AGENTS.md#safety) states the product rules. This file states how the code keeps them.
- Where a rule here endorses something that Fowler's code smells flag, the rule wins. See [Smell overrides](#smell-overrides).
- Sections marked **hard** protect the safety boundary: a breach is a finding. The other sections are judgement calls: report a breach as possible and quote the hunk.
- Each section starts with the diffs it applies to. Apply every section whose trigger matches.

## Untrusted text (hard)

Applies when the diff changes output, a view, the renderer, screening input, or provider parsing.

- **Views only.** Agent-facing data leaves the server only through explicit DTOs in `internal/views` or the server's `*DTO` types. `model.Message` and `model.Event` stay internal.
- **Clean each string.** Every email-derived string in a view passes through `content.CleanSubject` or `content.RenderBody`: names, addresses, subjects, folder names, categories, attachment names, and event fields.
- **Verdict-limited.** A new view field states which verdicts show it, and matches the verdict tables in `README.md` and `skills/agentmailguard/SKILL.md`.
- **Under-hide.** The renderer removes text only on a specific hiding signal backed by evidence. Text it keeps reaches screening; text it removes escapes screening. When in doubt, keep the text. The current signals are inline hides on one element.
- **One tree.** The renderer decides what to hide and writes Markdown from the same parsed tree that `balancedHTML` returns. Parsing can change content, so a decision made on one parse holds only for output built from that parse ([`x/net/html` security considerations](https://pkg.go.dev/golang.org/x/net/html#hdr-Security_Considerations)).
- **Escape into markup.** User text that a provider renders as HTML is escaped, as `model.NewEvent.HTMLBody` escapes the invite body with `html.EscapeString`.
- **Terminal-safe.** Server text that the CLI prints in a human format passes through `terminalSafe`. `ansi.Strip` alone keeps carriage return, backspace, BEL, and C1 controls, so the rune filter after it stays.
- **Confined paths.** Files under the server home have fixed names. A path built from request or mail data opens through `os.Root`.
- **Provider data is untrusted.** Adapters limit response size (`io.LimitReader`), page count, and loop count, and follow a next-page link only on the configured origin and path. The guard checks adapter results against the interface contract (requested IDs only, no duplicates, expected order and count) and turns a mismatch into `ValueError`, `failed`, or `unknown`.

## Errors and records (hard)

Applies when the diff creates, returns, persists, or prints an error, or writes the cache, the audit log, or stderr.

- **Fixed text.** An error that can reach a client, the cache, or the audit log carries fixed text or a safe code: a `GuardError` constant, a `model.MailboxError` built from a status code or a fixed name, or a type name that `model.ValidateFailureText` accepts. The guard converts other errors with `safeErr`. Provider response bodies, email text, and `err.Error()` of an external error stay out. Config and rules errors may name operator-owned paths and account names.
- **Identity, not text.** Branch on errors with `errors.Is`, `errors.As`, or a fixed sentinel, never on standard-library or provider error text, which changes between Go releases.
- **Named public refusals.** A refusal that users should read is a named `GuardError` constant, as in `internal/guard/calendar_writes.go`. Every allowlist on its path (`Guard.audited` for mail mutations, `server.publicError`, `client.Call`) lists the exact string; a test checks that the three agree. Review which refusals become public: each one is text that the agent reads.
- **Redacted types.** A type that holds a token, key, password, credential store, client options, or an adapter implements `Format` or `String` and prints `Name{redacted}`, as `guard.Guard` and `credentials.OAuth` do.
- **Content-free records.** Cache rows, audit records, and stderr carry IDs, verdicts, scores, safe codes, and operation settings only. Audit options come from validated mailbox values (folder IDs, listed categories, rule names), not from message text.

## Fail closed (hard)

Applies when the diff adds a decision, a default, a lookup, or a result type.

- **Zero is not success.** A zero value, nil, missing entry, or failed lookup becomes `error`, `unknown`, or `failed`, never green, free, or OK. Go zero-fills every struct, map miss, and unread field, so a default that means success turns any dropped value into an approval. Follow `model.Decide` (an invalid score is `Error`), `guard.normalize` (a missing or duplicate result fails), and `guard.CalendarAvailability` (every slot starts `unknown`).
- **Validate before access.** Decode the complete typed request (`params` into a struct, then `idValid`, `listValid`, `validString`, and explicit bounds) before loading an account, reading credentials, or calling a provider, as `server.accountCall` does. Keep the token walk in `strict`: `encoding/json` accepts duplicate keys.
- **Strict browser refusal.** The server refuses every request that carries an `Origin` header. `http.CrossOriginProtection` does not replace this check: it allows GET, HEAD, OPTIONS, requests without browser headers, and an `Origin` that matches `Host`, which a DNS-rebinding page can satisfy.
- **Guard re-checks.** The guard validates every input it depends on, even when the server already did. The guard is the trust seam; the server is one of its callers.
- **Bounded.** Every request, response, list, page walk, loop, and fan-out has an explicit limit. A new client-visible limit goes into "HTTP interface and limits" in `README.md`.

## Writes (hard)

Applies when the diff adds or changes a mailbox, calendar, or draft write, or an outbound HTTP request.

- **One write path.** Mail changes go through `Guard.mutationCall` with the mutation gate held (as `Guard.manual` does), drafts and sends through `Guard.compose`, and calendar writes through `Guard.calendarWrite`. These paths validate, normalize results, and audit under the mutation gate. Put a new mail write refusal in `mutationCall` so organizer rule actions (`internal/guard/apply.go`) get it too.
- **Audit survives cancellation.** After a provider returns a write result, record the audit with `context.WithoutCancel(ctx)`. The mailbox has already changed by then; a client that disconnects must not leave that change without a record. An audit failure returns `AuditError`.
- **At most once.** A write goes out once. Only reads retry, as the Gmail client retries rate-limited `GET` requests. A test checks the mechanical parts (no redirects, no rewindable body, no `Idempotency-Key`).

## Concurrency and cancellation (hard)

Applies when the diff adds a goroutine, a gate, a lock, or a context use.

- **Cancellation wins.** Check `ctx.Err()` after each external call before acting on its result, and return `ctx.Err()` itself on cancellation, as `safeErr` does.
- **Gate rules.** New waits follow the rules written in comments at `guard.Guard` (the `security` gate covers cache writes and final authorization, never network screening), `Guard.authorizedWrite` (take `mutation` before `security`), and `m365.Client.requestToken` (wait for auth only without an API slot). A new gate or lock states its rule in a comment the same way.
- **Bounded fan-out.** Per-item work in the guard uses `parallelItems`: at most 8 workers, each worker writes only its own slot, and siblings drain before an error returns.

## Design

Applies to every diff.

- **One definition.** A domain rule that more than one layer checks (time ranges, meeting responses, availability limits) is defined once, in `model` when layers share it, and each layer calls it. Calling it again at each trust seam is intended. Copying its condition or its literal limits is Duplicated Code.
- **Optional capabilities.** A capability that only some providers have is an optional `model` interface, like `model.CalendarWriter` and `model.CalendarAvailability`. The guard type-asserts it and returns a named `…Unsupported` refusal. Provider differences stay inside the adapter: the guard, views, and content have no provider-name branches.
- **Contracts in comments.** A new interface, method, gate, or limit states its contract in one line: what callers and adapters must guarantee and what nil or zero means, as `model.Mailbox` and `model.Screener` do. Comments say why or what must hold, not what the next line does.
- **Match the file.** New code follows the density and naming of its neighbours: early returns, small helpers, validation as one boolean expression, and short local names in short functions.

## Tests

Applies when the diff adds or changes tests.

- **Each layer owns its assertions.** Provider tests assert exact Graph or Gmail requests and response mapping. Guard tests assert verdicts, refusals, gates, and audit records. Server tests assert parameter mapping, response shape, public refusals, and audit writes. Native tests build the real binary and run whole workflows against fake external endpoints. A test that asserts another layer's payloads again is duplication.
- **Fakes at external seams.** Use `httptest` servers for Graph, Gmail, and Jev, a fake `credentials.Platform`, an injected `Now`, and temporary homes. Use the real guard, cache, audit log, and views. Test-cost hooks are exported variables set in `TestMain`, as `credentials.FileWorkFactor` lowers scrypt work; an `export_test.go` may hold such hooks and exposes no internals. Keep injected `Now` clocks: `testing/synctest` fits only pure-Go gate and fan-out tests, because real sockets and cgo calls do not block durably inside its bubble.
- **Stable assertions.** Tests assert error identity or this project's fixed text, never standard-library error text, which changes between Go releases.
- **Synthetic data.** Tokens and keys are visibly synthetic. A test checks that addresses use reserved domains.
- **Safety regressions fail.** Each changed safety behavior (refusal, fail-closed default, redaction, audit record, limit) has a test that fails when its check is removed. Prove it with a mutation probe: remove the check, see the test fail, restore the check.
- **One helper per fixture.** A fixture that several tests in a package use (JWTs, Graph recorders, guard builders, scope sets) has one helper.

## Docs

Applies when the diff changes a command, flag, operation, limit, refusal, scope, view field, screened field, or provider capability.

- **Docs follow behavior.** Update every surface that states the changed fact: `README.md`, `skills/agentmailguard/SKILL.md`, CLI help (including `notes` in `internal/cli/cli.go`), `SECURITY.md` for boundaries, and `docs/setup.md` for scopes and setup. "Privacy and external screening" in `README.md` lists every field sent to the screening service.
- **Honest verification.** "Verification and limits" in `README.md` names what each claim was verified against (fake Graph endpoint, fake Keychain `Platform`, live call). A new capability adds what remains unverified live.

## Dependencies and toolchain

Applies when the diff changes `go.mod`, `go.sum`, build tags, the SQLite DSN, or `.github/workflows`.

- **Supported Go.** The `go` directive names the oldest Go release that upstream still supports (the two newest major releases), and CI tests that release and `stable`. A dependency bump that raises the `go` directive is a toolchain change: review it as one.
- **Bundled SQLite.** govulncheck does not cover the SQLite that `mattn/go-sqlite3` bundles. A go-sqlite3 change states the bundled SQLite version in its commit body. Enabling WAL mode or an optional SQLite feature by build tag (such as `sqlite_fts5`) needs a bundled version without known bugs in that feature ([SQLite CVEs](https://www.sqlite.org/cves.html)).

## Smell overrides

Fowler's code smells yield to these conventions:

- **Repeated checks at trust seams** (server, then guard) are not Duplicated Code, provided the rule has one definition.
- **Closed allowlists** of exact strings in the guard, server, and client are not Repeated Switches. They are the safety boundary.
- **DTOs that mirror `model` types** in `views` and `server` are not Duplicated Code or Data Clumps. The copy is the output boundary.
- **Guard methods** that validate, delegate to an adapter, and normalize the result are the trust seam, not Middle Man.
- **Short local names** (`e`, `p`, `a`, `r`) in short functions are not Mysterious Name. The smell still applies to exported and package-level names.

## Not yet automated

These rules are mechanical. Check them until a test or CI job enforces each one, then delete its line. The tool in brackets is the one that can enforce it. Repository-wide checks go in `internal/standards`.

- Files under the server home are created `0600` in `0700` directories, with an explicit `Chmod` after open, as in `cache` and `audit`. Modes are literals at the call site, because gosec reads only literals. [`gosec` G301 set to `0700`, G302, G306]
