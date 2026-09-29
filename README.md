# agentmailguard-go

Guarded Microsoft 365 and Gmail access for AI agents, with a long-lived server and a thin CLI.
The guard limits what an agent sees in mail and calendar events.
It does not protect a human from fraud.

**Exposure warning:** the client/server connection has no authentication and no built-in TLS.
Anyone who can reach the listener can read guarded mailbox data, change mail, create drafts, and clear cached verdicts.
The default listener is `127.0.0.1:8787`.
A non-loopback listener requires an explicit `--listen` option and prints a warning.
Browser requests with an `Origin` header are refused, and the server does not enable CORS.
These restrictions are not a substitute for a protected network or encrypted tunnel.
Provider OAuth remains mandatory.

## Build and checks

1. Install Go 1.25 or later with CGO support and a C compiler.
2. Run `make` from this repository.
3. Run `./bin/agentmailguard-go --help`.

`make` checks formatting, runs `go vet` and race tests, builds the packages, and creates `bin/agentmailguard-go`.
SQLite requires CGO.
Native credential storage requires macOS and CGO.
Other platforms can run synthetic tests, but they have no plaintext credential fallback.
The build does not install anything into your PATH.

## Server configuration

The server owns configuration, OAuth credentials, the Jev key, `cache.db`, and `audit.jsonl`.
The CLI never loads these files for data commands, even with a loopback server.
The Go home is separate from Python: `--home`, then `AGENTMAILGUARD_GO_HOME`, then `~/.agentmailguard-go`.
Only `serve` and local `auth login` accept `--home`.
There is no automatic Python configuration, cache, or credential import.

Create a private server-home directory and a `config.toml` file there.
Prefer a restricted provider application that grants only the required scopes.
Extra grants are the user's choice: a usable token with extra scopes or Microsoft 365 application roles is accepted with a warning. The server never uses the extra grants.
These examples contain placeholders, not working credentials.

Microsoft 365 example:

```toml
[accounts.work]
provider = "m365"
client_id = "<restricted-application-client-id>"
tenant_id = "<tenant-id>"
```

Gmail example:

```toml
[accounts.gmail]
provider = "gmail"
client_id = "<desktop-application-client-id>"
client_secret = "<desktop-application-client-secret>"
```

Both account tables can exist in one file.
The first account in file order is the default.
Use `--account NAME` to select another account.
The CLI refuses an explicitly empty or whitespace-only account name before any server request or local login.
The server resolves nonempty names, not the client.
Keep `config.toml` private because the Gmail application secret belongs on the server host.

Provider OAuth records use macOS Keychain services `agentmailguard-go-m365` and `agentmailguard-go-gmail`, with the account name as the Keychain account.
The Jev key uses service `agentmailguard-go-openrouter` and account `agentmailguard-go`.
The server can also read `OPENROUTER_API_KEY` from its environment.
Run `agentmailguard-go auth set-jev-key` on the server host to save the key. On a terminal it prompts with echo disabled; otherwise it reads the key from stdin, for example from a password manager CLI. The key never appears in arguments or output.
The command is local only: it refuses `--server`, `AGENTMAILGUARD_GO_SERVER`, `--account`, and `--home`. It refuses keys with spaces or control characters and updates an existing Keychain item in place.
The server reads the key at start (see below). Restart the server after replacing the key.
No client command transfers a provider token or Jev key to the server.

## Local login and server start

Run login locally on the server host:

```sh
./bin/agentmailguard-go --account work auth login --home /private/path/agentmailguard-go
./bin/agentmailguard-go --account work auth login --home /private/path/agentmailguard-go --device-code
./bin/agentmailguard-go serve --home /private/path/agentmailguard-go
```

`serve` loads every credential before it accepts requests, because an operator is present at server start:

1. It reads the OAuth record of every account in `config.toml`, and the Jev key unless `OPENROUTER_API_KEY` is set. Approve the macOS Keychain prompts at this time. Choose "Always Allow" to prevent later prompts; a rebuilt binary asks again.
2. It writes each OAuth record back unchanged, so the Keychain also asks for modify permission now and not at the first token refresh.
3. If the Jev key is missing and stdin is a terminal, it asks for the key with echo disabled and saves it. Piped or absent stdin gets no prompt.
4. It prints one `credential check:` line per item, without secrets. If any item fails, the server does not start. Fix the item (for example with `auth login` or `auth set-jev-key`) and start again.

After start, the server never reads the Keychain again. It keeps credentials in memory. Token refreshes are saved to the Keychain first, then to memory. An account added to `config.toml` after start returns the restart-required error.

Browser login is the default.
Microsoft 365 supports device-code login.
Gmail refuses device-code login because that flow does not support the required scopes.
Login is never an HTTP operation.
A supplied `--server` or `AGENTMAILGUARD_GO_SERVER` value makes `auth login` refuse the operation instead of silently logging in locally.
Data commands and `auth status` never open a login prompt.
Login and `auth status` print a warning on stderr for extra grants. `auth status` lists them in `extra_scopes`; `scopes` lists only requested grants. Application roles appear with a `role:` prefix.
Tokens that cannot work are refused: malformed tokens, and Gmail tokens without the mail and calendar scopes. Gmail refusals list `missing_scopes`.
Diagnostics show at most 100 conventional scope labels of 256 bytes each. Malformed labels are omitted.
The program does not start a server automatically.

Microsoft 365 login reports fixed errors for browser startup, authorization, token exchange, token validation, profile lookup, and Keychain storage.
Device-code request, polling, refusal, cancellation, and timeout errors are also distinct.
The browser callback only confirms receipt of a response. The terminal shows the final result.
These diagnostics contain no raw provider errors, token values, or callback URLs. They do not bypass Microsoft access policies.

**Restart the server after local login or account configuration changes.**
A cached account with changed configuration returns a fixed restart-required error.
The server does not replace its cached provider identity in place.
Provider access tokens still refresh normally after expiry, with the same checks and warnings on the refreshed scopes.
Rules are different: the server reloads and validates `rules.toml` on every `rules list` and `mail organize` call.

## Local and remote clients

```sh
./bin/agentmailguard-go auth status
./bin/agentmailguard-go --account work mail list --count 5
./bin/agentmailguard-go mail get MESSAGE_ID --pretty
./bin/agentmailguard-go --timeout 2h mail organize --since 1m
```

The server URL comes from `--server`, then `AGENTMAILGUARD_GO_SERVER`, then `http://127.0.0.1:8787`.
The client supports HTTP or HTTPS URLs without userinfo, query parameters, or fragments.
HTTPS requires an external TLS endpoint because `serve` does not provide TLS.
The overall client deadline defaults to 30 minutes and accepts a positive `--timeout` duration.
Long organizer scans can require many 30-second Jev batches.

For protected remote access, keep the server bound to loopback and use an SSH tunnel:

```sh
ssh -N -L 18787:127.0.0.1:8787 <server-host>
./bin/agentmailguard-go --server http://127.0.0.1:18787 --account work mail list
```

An explicit remote listener is available with `serve --listen ADDRESS:PORT`.
Do not expose it to an untrusted network.
The tunnel protects the connection, not against other users who can already access either local listener.

## Commands and output

Run `--help` without a server to see every command and option.
Flags can follow positional IDs, and recipient/category flags can repeat.
`--no-pretty` and `--no-device-code` cancel their positive forms.
The last supplied boolean option wins.
Required text flags distinguish an omitted value from an explicit empty value, including `draft update ID --body ''`.

| Group | Commands |
| --- | --- |
| State and auth | `stats --days 30`, `cache clear`, `rules list`, `auth status`, local `auth login [--device-code]` |
| Mail reads | `mail list`, `mail get ID [--pretty]`, `mail search`, `mail link ID L1 L2` or `mail link ID --all`, `mail folders`, `mail categories` |
| Mail changes | `mail archive IDS...`, `mail trash IDS...`, `mail move FOLDER_ID IDS...`, `mail read IDS...`, `mail unread IDS...`, `mail flag IDS... --state flagged|complete|none`, `mail categorize IDS... --add NAME --remove NAME` |
| Organization and calendar | `mail organize --since 7d [--until DATE] [--apply]`, `calendar list --from today --to +7d`, `calendar get ID` |
| Drafts only | `draft new --to ADDRESS --subject TEXT --body TEXT`, `draft reply ID --body TEXT`, `draft forward ID --to ADDRESS`, `draft update ID --body TEXT` |

Successful commands print the original operation's JSON result, not an RPC envelope.
`mail get --pretty` formats only the guarded view.
Draft update returns the resulting draft ID, which can change on Gmail.
There is no send, permanent-delete, folder creation, category creation, calendar write, or arbitrary provider request command.

Exit code `0` means the call ran, including a mutation with failed per-ID results.
Exit code `1` means the whole call failed or was refused, or `auth status` reports an unusable login.
Exit code `2` means invalid CLI usage.
Errors use fixed text or approved safe codes, never raw network errors or provider response bodies.

## Rules and guarded behavior

Example `rules.toml` in the server home:

```toml
[accounts.work.receipts]
folder = "archive"
min_age_hours = 24
instructions = "Receipts that need no further action."

[accounts.work.finance]
category = "Finance"
instructions = "Messages about invoices or receipts."
```

A folder target is a mailbox folder ID, `archive`, or `trash`.
A category target must already exist in the mailbox.
`rules list` shows resolved names and missing-target warnings, never instructions or confidence scores.
Missing rules produce an empty organizer report without provider or Jev access.
Invalid rules fail before provider access.
`stats` and `cache clear` need neither account configuration nor provider authentication.
Cache clear removes all accounts' verdicts, saved organization answers, and completion markers.

Organizer dates use the server clock and UTC midnight.
`Nd`, `Nw`, and `Nm` require `N >= 1`.
Month subtraction clamps to the last day of the target month.
Explicit `YYYY-MM-DD` dates cannot be in the future.
`--until` is exclusive and must be later than `--since`.
Calendar dates use the client's local date and timezone.
For calendar queries, a relative `--to +Nd` starts from `--from`.
The calendar interval must increase and cannot exceed 90 days plus one hour for DST.

| Verdict | Visible mail content |
| --- | --- |
| green | Cleaned headers, body, attachment names, and link IDs |
| yellow | Cleaned headers, attachment names, and reason, without body |
| red or sealed | Sender address, date, and reason, without display name or subject |
| error | Date and safe error code, without sender, subject, or body |

Every verdict also includes the message ID, verdict, authentication results, read state, flag, and mailbox-listed categories.
Manual mutations work at every verdict.
Automatic business actions remain green-only.
`mail organize --apply` can add existing `Yellow`, `Red`, or `Sealed` security tags to matching hidden mail.
The program never creates tags or changes their colors.
Successful action and security-tag markers prevent repeated organizer writes.
Ordinary reads do not modify the mailbox.
Cached hidden reads avoid fetching the body, cached green reads avoid Jev, and error verdicts retry screening on the next read.
All email-derived text remains untrusted, including green output.

## HTTP interface and limits

The only data endpoint is `POST /v1/call` with `Content-Type: application/json`:

```json
{"operation":"mail.list","account":"work","params":{"folder":"inbox","count":25,"skip":0}}
```

Operation names match CLI groups, such as `mail.get`, `draft.update`, and `cache.clear`.
The `account` field is optional, and `params` must be an object.
Unknown operations or fields, duplicate keys, malformed JSON, trailing JSON, null fields, invalid types, and oversized requests are refused.
There is no HTTP login, token, key, raw-message, classification, or rules-mutation endpoint.

Requests are limited to 1 MiB and 16 JSON nesting levels.
The client bounds responses at 64 MiB and never follows redirects or retries requests, including stale-connection write replay.
Mail list/search counts range from 1 to 100, skip is at most 1,000,000, and manual mutations accept at most 50 IDs.
Recipient and category lists accept at most 100 entries, and IDs and search strings are limited to 4,096 bytes.
Stats accepts 1 to 365,000 days.

The server admits at most 64 concurrent calls.
Header, request-body, idle, and response-write limits are bounded.
There is no short whole-operation server deadline that cuts off a valid organizer scan.
SIGINT or SIGTERM stops network admission, allows five seconds to drain, then cancels work with a ten-second shutdown limit.
The cache closes after active calls finish.
An adapter that ignores cancellation can delay resource cleanup and produces a shutdown failure.
The server logs no request arguments, bodies, credentials, or panic payloads.
There is no scheduler and no automatic mailbox work.

## Verification and limits

Offline tests use actual loopback HTTP, temporary SQLite/audit files, synthetic provider/Jev endpoints, and injected credential and clock adapters.
The native workflow test builds the real binary and runs remote commands against one long-lived server.
It covers cached hidden reads, error retry, classification, preview/apply, concurrent completion, security tags, manual mutations, drafts, calendar, stats, rules, and separate accounts.
Transport tests cover redirects, stale connections, cancellation, request validation, and content-free errors.
No test needs real mailbox data, a browser, OpenRouter, or Keychain access.

Earlier authorized checks passed for live Gmail reads and a real Jev request containing synthetic text only.
Earlier live Microsoft 365 checks were blocked because the adapter refused extra scopes. The adapter now accepts them with a warning.
Real browser/device login, native Go Keychain integration, Microsoft 365 Graph access, and provider acceptance of generated drafts remain unverified.
This step adds offline verification only and does not claim production readiness.
