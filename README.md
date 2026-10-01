# agentmailguard

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
3. Run `./bin/agentmailguard --help`.

`make` checks formatting, runs `go vet` and race tests, builds the packages, and creates `bin/agentmailguard`.
SQLite still requires CGO with either credential backend.
The native Keychain backend requires macOS and CGO.
The file backend does not require Keychain, but it does not remove the SQLite build requirement.
Credential locking uses Unix `flock`, and terminal password input uses `stty`. Windows is not supported.
There is no plaintext credential fallback.
The build does not install anything into your PATH.

## Server configuration

The server owns configuration, OAuth credentials, the Jev key, `cache.db`, and `audit.jsonl`.
The CLI never loads these files for data commands, even with a loopback server.
The server home is `--home`, then `AGENTMAILGUARD_HOME`, then `~/.agentmailguard`.
`serve` and local `auth login`, `auth set-jev-key`, `auth migrate`, and `auth change-password` accept `--home`.
There is no automatic import of configuration, cache, or credentials from the retired Python version.

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

## Credential storage

Both backends store all provider OAuth credentials and the Jev (OpenRouter) key in one versioned record.
The optional configuration table selects the backend:

```toml
[credentials]
backend = "keychain" # Default. Use "file" for password-encrypted storage.
```

An absent table selects `keychain`.
If present, the table must contain exactly one string field, `backend`.
Unknown backend values or keys cause a configuration error.
Neither backend detects rollback: another process under the same user can restore an older valid credential record.
The lock coordinates only processes that use the same home directory.

### Keychain backend

The Keychain backend uses one non-synchronizable generic-password item: service `agentmailguard`, account `credentials`.
Choose "Always Allow" for this item to avoid repeated access prompts for the same build.
One item replaces the separate per-account approvals with one item approval per build.
An ad-hoc-signed rebuild changes the application identity, so it needs approval again.
Stable code signing is outside this implementation.

If the single item is absent, the first local store operation automatically imports the configured accounts and Jev key from legacy items.
This applies to `serve`, `auth login`, and `auth set-jev-key`, so an initial login cannot hide other legacy credentials.
The import leaves legacy items unchanged.
An unreadable legacy item refuses the operation instead of creating a partial record.

### File backend and password input

The file backend stores the record in `<home>/credentials.age` with mode `0600`.
It uses age password encryption with scrypt work factor `logN = 18` and refuses a higher decryption work factor.
Writes use a same-directory temporary file, file fsync, atomic rename, and directory fsync.
A wrong password or damaged file refuses access with the same fixed error.
Unknown record versions also refuse access.

The password source is `AGENTMAILGUARD_CREDENTIALS_PASSWORD`, then stdin if the variable is absent.
An empty password is refused, including an explicitly empty environment variable.
Passwords must be valid UTF-8, at most 4096 bytes, without control characters.
Terminal input disables echo. Creation of a new file asks for the password twice.
Non-terminal stdin supplies one password line without confirmation.
The process keeps the password in memory for its lifetime.

**Caution:** Environment variables can expose secrets to other processes under the same user.
The program unsets the password variable after reading it, before it starts child processes such as a browser.
Unsetting the variable does not remove the initial environment exposure or erase the password from process memory.
Do not put passwords in command arguments, shell history, or configuration files.
A secret manager can supply the environment variable or stdin.

Run `agentmailguard auth set-jev-key --home /private/path/agentmailguard` locally to save the Jev key.
Terminal input disables echo. Non-terminal stdin supplies the key as one line.
With the file backend and no password environment variable, piped input must contain two lines: the password first, then the key.
With the password environment variable, piped input contains only the key line.
The command refuses keys with spaces or control characters. It never prints the key or accepts it as an argument.
It also refuses `--server`, `AGENTMAILGUARD_SERVER`, and `--account`.

The server can use `OPENROUTER_API_KEY` instead of the stored key.
The server selects its Jev key at start. Restart it after replacing the key.
No remote client command transfers a provider token or Jev key to the server.

### Copy credentials to the other backend

1. Stop the server.
2. Run one command locally on the server host:

```sh
./bin/agentmailguard auth migrate --home /private/path/agentmailguard --to file
# Or, from a configured file backend on macOS:
./bin/agentmailguard auth migrate --home /private/path/agentmailguard --to keychain
```

3. Set `[credentials] backend` in `config.toml` to the value printed by the command.
4. Restart the server with the selected backend.

Migration copies the whole record from the configured backend to the other backend under the same-home lock.
It leaves the source record and configuration unchanged.
It refuses an existing target record, including legacy Keychain entries. It also refuses a target that matches the configured backend.
The file password comes from the normal sources. Terminal creation of the file target asks twice.
Migration refuses `--server`, `AGENTMAILGUARD_SERVER`, and `--account`.
Migration of your real Keychain needs separate permission and remains unverified by this workflow.

### Change the file password

1. Stop the server.
2. Run `./bin/agentmailguard auth change-password --home /private/path/agentmailguard` on a terminal.
3. Supply the current password through its normal source.
4. Enter the new password twice at the terminal prompts.
5. Restart the server with the new password.

This command works only with the file backend and re-encrypts the latest record under the same-home lock.
The new password cannot come from an environment variable or a pipe.
A running server retains its old password and cannot save refreshed tokens after this change.
The command refuses `--server`, `AGENTMAILGUARD_SERVER`, and `--account`.

### Optional manual cleanup of legacy Keychain items

**Caution:** Do not delete legacy items before you verify the copied credentials after a server restart.
Deletion removes the old copies and can break older binaries that still use them.
The program never deletes these items.

The following commands are templates, not verification steps. Replace each placeholder with the exact legacy account name and intended Keychain path.
Use only the commands for items that you intend to delete.
Do not delete the new `agentmailguard` / `credentials` item.

```sh
security delete-generic-password -s agentmailguard-go-m365 -a '<m365-account-name>' '<keychain-path>'
security delete-generic-password -s agentmailguard-go-gmail -a '<gmail-account-name>' '<keychain-path>'
security delete-generic-password -s agentmailguard-go-openrouter -a agentmailguard-go '<keychain-path>'
```

## Local login and server start

Run login locally on the server host:

```sh
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard --device-code
./bin/agentmailguard serve --home /private/path/agentmailguard
```

`serve` loads every credential before it accepts requests, because an operator is present at server start:

1. It reads the whole store and rewrites the record once to check read and write access, not once per account.
2. Its first `credential check:` item is `credentials (keychain)` or `credentials (file)`. A store failure refuses startup.
3. It checks every configured account. It also checks the stored Jev key unless `OPENROUTER_API_KEY` supplies the key. Check lines contain fixed statuses, not secrets.
4. If the Jev key is missing and stdin is a terminal, it prompts with echo disabled and saves the key. Piped password input gets no Jev prompt.

An account that is not logged in does not block startup.
Its commands fail until local `auth login` and a server restart.
Other credential failures, a missing Jev key, or missing configuration refuse startup.
Store statuses include `ok`, `access denied`, `wrong password or damaged file`, `password missing`, and `unsupported version`.

After startup, ordinary credential loads use server memory.
Each runtime save rereads the backend under an exclusive `<home>/credentials.lock` before it changes its own entry.
A token refresh preserves other accounts' entries and the Jev key from concurrent local commands.
The running server still keeps its earlier in-memory credentials.
Token refreshes reach the backend before memory. With Keychain "Always Allow", the reread does not need another prompt for the same build.
A file save needs one scrypt derivation for the read and another for the write.
An account added to `config.toml` after startup returns the restart-required error.

Browser login is the default.
Microsoft 365 supports device-code login.
Gmail refuses device-code login because that flow does not support the required scopes.
Login is never an HTTP operation.
A supplied `--server` or `AGENTMAILGUARD_SERVER` value makes `auth login` refuse the operation instead of silently logging in locally.
Data commands and `auth status` never open a login prompt.
Login and `auth status` print a warning on stderr for extra grants. `auth status` lists them in `extra_scopes`; `scopes` lists only requested grants. Application roles appear with a `role:` prefix.
Tokens that cannot work are refused: malformed tokens, and Gmail tokens without the mail and calendar scopes. Gmail refusals list `missing_scopes`.
Diagnostics show at most 100 conventional scope labels of 256 bytes each. Malformed labels are omitted.
The program does not start a server automatically.

Microsoft 365 `auth status` can use a cached token and username without a Graph request.
A successful status does not prove current Graph access, all required permissions, or provider acceptance of mailbox operations.
Microsoft 365 login reports fixed errors for browser startup, authorization, token exchange, token validation, profile lookup, and credential storage.
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
./bin/agentmailguard auth status
./bin/agentmailguard --account work mail list --count 5
./bin/agentmailguard mail get MESSAGE_ID --pretty
./bin/agentmailguard --timeout 2h mail organize --since 1m
```

The server URL comes from `--server`, then `AGENTMAILGUARD_SERVER`, then `http://127.0.0.1:8787`.
The client supports HTTP or HTTPS URLs without userinfo, query parameters, or fragments.
HTTPS requires an external TLS endpoint because `serve` does not provide TLS.
The overall client deadline defaults to 30 minutes and accepts a positive `--timeout` duration.
Long organizer scans can require many 30-second Jev batches.

For protected remote access, keep the server bound to loopback and use an SSH tunnel:

```sh
ssh -N -L 18787:127.0.0.1:8787 <server-host>
./bin/agentmailguard --server http://127.0.0.1:18787 --account work mail list
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
| State and auth | `stats --days 30`, `cache clear`, `rules list`, `auth status`, local `auth login [--device-code]`, `auth set-jev-key`, `auth migrate --to file\|keychain`, `auth change-password` |
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
HTML bodies become Markdown before screening.
The renderer drops non-rendered elements, comments, invisible Unicode, elements with the `hidden` attribute, and elements an inline style hides with `display:none`, `visibility:hidden` or `collapse`, `mso-hide:all`, `opacity` below 0.1, or a font size of 0 or below 2px or 2pt.
Where an inline hide removed prose, the Markdown keeps a `[hidden text removed]` marker so Jev and the agent see that the sender hid text; non-rendered markup and empty hidden boxes such as tracking pixels leave no marker.
It does not evaluate stylesheets, `!important` precedence, colour contrast, geometry, or clipping; that text stays in the Markdown so Jev and the agent see it.

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
The native workflow test builds the real client binary and runs remote commands against a long-lived in-process server with injected external adapters.
It covers cached hidden reads, error retry, classification, preview/apply, concurrent completion, security tags, manual mutations, drafts, calendar, stats, rules, and separate accounts.
Native file-backend tests also cover server startup, password refusal, terminal password changes, and SIGTERM shutdown.
A separate combined native acceptance run used the file backend, temporary HOME, synthetic credentials, and a non-forwarding loopback HTTPS proxy.
It passed device-code login, native server startup, an external key update while serving, token refresh, terminal password change, and restart.
A fresh screened read after restart required the updated Jev key and refreshed OAuth token at fake endpoints.
This run did not access real providers, Keychain, or user configuration.
Transport tests cover redirects, stale connections, cancellation, request validation, and content-free errors.
No test needs real mailbox data, a browser, OpenRouter, or Keychain access.

Earlier authorized checks passed for live Gmail reads and a real Jev request containing synthetic text only.
Earlier Microsoft 365 checks refused extra scopes. After the approved policy change, a user-authorized browser login and `auth status` succeeded with warnings.
That status result alone does not establish live Graph mailbox access.
A subsequent authorized Microsoft 365 organize preview succeeded with screening and no mailbox writes.
These earlier read-only checks do not verify the credential-backend changes. This workflow adds no new live provider verification.
Device-code login can still fail under Microsoft organization policies.
Real device-code login and provider acceptance of generated drafts remain unverified here.
Native Keychain migration and the user's real credential migration also remain unverified.
Migration tests use a fake Keychain Platform at the constructor and CLI seams, not the production Security APIs.
Production-binary Keychain migration remains deferred; the migration evidence for this change comes from these fake-Platform tests.
Offline verification does not establish production readiness.
