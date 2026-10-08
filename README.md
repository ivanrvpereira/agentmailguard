# agentmailguard

Guarded Microsoft 365 and Gmail access for AI agents, with a long-lived server and a thin CLI.
The guard limits what an agent sees in mail and calendar events.
It does not protect a human from fraud.

**Exposure warning:** the client/server connection has no authentication and no built-in TLS.
Anyone who can reach the listener can read guarded mailbox data, change mail, and clear cached verdicts.
They can also do every action that the [write policy](#write-policy) allows, for example create drafts and send Microsoft 365 meeting invitations, updates, cancellations, and responses.
The default listener is `127.0.0.1:8787`.
A non-loopback listener requires an explicit `--listen` option and prints a warning.
Browser requests with an `Origin` header are refused, and the server does not enable CORS.
These restrictions are not a substitute for a protected network or encrypted tunnel.
Provider OAuth remains mandatory.

## Privacy and external screening

**Privacy warning:** Screening is an external service, not a local-only check.
The server sends requests to OpenRouter's System One endpoint for model `typesafe/jev-1.13`.

- Mail requests include rendered body text, subject, sender and recipient identities, attachment names, authentication results, and link domains.
- Calendar requests include subject, organizer, attendees, location, recurrence, meeting join URLs, and other event metadata.
- Private-event bodies are excluded, but private-event metadata and accepted meeting join URLs are still included.
- Organization requests include the rule instructions used for classification.

These transfers occur before the verdict.
Content hidden from the agent can still reach the screening service.
Meeting join URLs can contain access secrets.
Content-free local logs and encrypted credentials do not prevent external disclosure.

The project does not guarantee zero retention, no training, or a processing region.
Review [OpenRouter's provider policies](https://openrouter.ai/docs/guides/privacy/logging) and the applicable screening-service terms before using real mail.
Obtain any required organizational approval before connecting a work mailbox.

## Getting started

Use the [setup guide](docs/setup.md) for provider application registration, exact scopes, credential-backend selection, and the first guarded read.
See [SECURITY.md](SECURITY.md) for supported security boundaries and vulnerability-reporting instructions.
Maintainers can use the [GitHub publication checklist](docs/github-setup.md) for CI enforcement and repository security configuration.

## Build and checks

1. Install a currently supported, patched Go release with CGO support and a C compiler.
   The minimum language version is Go 1.26.
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

The supported deployment is one server setup per machine, with multiple configured accounts in one server home.
Each account has a distinct configuration name.
Independent server homes with separate Keychain identities are not supported.

The server owns configuration, OAuth credentials, the Jev key, `cache.db`, and `audit.jsonl`.
The CLI never loads these files for data commands, even with a loopback server.
The server home is `--home`, then `AGENTMAILGUARD_HOME`, then `~/.agentmailguard`.
`serve` and local `auth login`, `auth set-jev-key`, `auth set-approval-password`, `auth migrate`, and `auth change-password` accept `--home`.
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
The optional `[policy]` tables in `config.toml` set the [write policy](#write-policy).

## Credential storage

Both backends store all provider OAuth credentials, the Jev (OpenRouter) key, and the [approval password](#approval-password) hash in one versioned record.
The optional configuration table selects the backend:

```toml
[credentials]
backend = "keychain" # Default. Use "file" for password-encrypted storage.
```

An absent table selects `keychain`.
Linux requires `backend = "file"` because the native Keychain backend is macOS-only.
If present, the table must contain exactly one string field, `backend`.
Unknown backend values or keys cause a configuration error.
Neither backend detects rollback: another process under the same user can restore an older valid credential record.
The lock coordinates only processes that use the same home directory.

### Keychain backend

The Keychain backend uses one non-synchronizable generic-password item: service `agentmailguard`, account `credentials`.
All configured accounts share this item under the current operating-system user, with separate entries keyed by provider and account name.
A different `--home` does not create an independent Keychain namespace.
Use one server home and distinct account names for the supported multi-account setup.
Choose "Always Allow" for this item to avoid repeated access prompts for the same build.
One item replaces the separate per-account approvals with one item approval per build.
An ad-hoc-signed rebuild changes the application identity, so it needs approval again.
Stable code signing is outside this implementation.

If the single item is absent, the first local store operation automatically imports the configured accounts and Jev key from legacy items.
This applies to `serve`, `auth login`, `auth set-jev-key`, and `auth set-approval-password`, so an initial login cannot hide other legacy credentials.
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
./bin/agentmailguard --account personal auth login --home /private/path/agentmailguard --no-browser
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

On a headless host, use Gmail login with `--no-browser`. It needs no SSH tunnel:

1. Run `auth login --no-browser` in an SSH session on the server host.
2. Open the printed sign-in URL in a browser on any computer, and approve access.
3. The browser goes to `http://127.0.0.1:PORT/?state=...&code=...` and shows a connection error. Copy the full address from the address bar.
4. Paste the address into the SSH session and press Enter. Finish within five minutes.

The pasted address gets the same checks as the loopback callback: host, port, path, and `state` must match this sign-in.
If the address does not match, login asks for it again. A provider error in a matching address ends the login.
The PKCE verifier stays on the server host, so the code in the address cannot be exchanged anywhere else.
The program does not print or log the pasted address. The terminal shows it as you paste it.
Without `--no-browser`, Gmail login opens the URL with `open` on macOS or `xdg-open` on Linux. If that fails, login continues and accepts the loopback callback or a pasted address.
`--no-browser` is refused for Microsoft 365. Use `--device-code` on a headless host.

Gmail login and device-code login print numbered steps on stderr. The sign-in URL is on its own line, without quotes.
A successful login prints a final `✓ Signed in as ...` line on stderr.
Stdout gets the JSON result with `account`, `username`, and `scopes`.
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
Provider access tokens still refresh normally after expiry, with scope validation on the refreshed token.
Login and `auth status` report extra grants. Ordinary mailbox requests do not emit refreshed-scope warnings.
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
Forward only the API port (8787). Do not forward the [approval console](#approval-console) port through a tunnel that the agent can use.

## Commands and output

Run `--help` without a server to see every command and option.
Flags can follow positional IDs, and recipient/category flags can repeat.
`--no-pretty` and `--no-device-code` cancel their positive forms.
The last supplied boolean option wins.
Required text flags distinguish an omitted value from an explicit empty value, including `draft update ID --body ''`.

| Group | Commands |
| --- | --- |
| State and auth | `stats --days 30`, `cache clear`, `rules list`, `approvals status ID`, `auth status`, local `auth login [--device-code] [--no-browser]`, `auth set-jev-key`, `auth set-approval-password`, `auth migrate --to file\|keychain`, `auth change-password` |
| Mail reads | `mail list`, `mail get ID [--pretty]`, `mail search`, `mail link ID L1 L2` or `mail link ID --all`, `mail folders`, `mail categories` |
| Mail changes | `mail archive IDS...`, `mail trash IDS...`, `mail move FOLDER_ID IDS...`, `mail read IDS...`, `mail unread IDS...`, `mail flag IDS... --state flagged|complete|none`, `mail categorize IDS... --add NAME --remove NAME` |
| Organization and calendar | `mail organize --since 7d [--until DATE] [--apply]`, `calendar list --from today --to +7d`, `calendar get ID` |
| Availability (Microsoft 365, read-only) | `calendar availability --to ADDRESS [--to ADDRESS] --start 'YYYY-MM-DD HH:MM' --end 'YYYY-MM-DD HH:MM'` |
| Meetings (Microsoft 365, send notifications) | `calendar invite --to ADDRESS --subject TEXT --start 'YYYY-MM-DD HH:MM' --duration 30m\|--end 'YYYY-MM-DD HH:MM' [--body TEXT] [--location TEXT] [--no-teams]`, `calendar update ID [--subject TEXT] [--location TEXT] [--start TIME] [--duration D\|--end TIME] [--add-attendee ADDRESS] [--remove-attendee ADDRESS] [--teams]`, `calendar cancel ID [--comment TEXT]`, `calendar respond ID --response accept\|tentative\|decline [--comment TEXT]` |
| Drafts only | `draft new --to ADDRESS --subject TEXT --body TEXT`, `draft reply ID --body TEXT`, `draft forward ID --to ADDRESS`, `draft update ID --body TEXT` |

Successful commands print the original operation's JSON result, not an RPC envelope.
`mail get --pretty` formats only the guarded view.
Draft update returns the resulting draft ID, which can change on Gmail.
`calendar invite` returns the new event ID. The other meeting commands return the event ID and `"ok": true`.
The [write policy](#write-policy) can refuse trash, draft, and meeting commands, or store them as pending items. See [Results](#results) for these results.
There is no mail send, permanent-delete, folder creation, category creation, Gmail calendar write, or arbitrary provider request command.

Exit code `0` means the call ran, including a mutation with failed per-ID results and a `pending` result.
Exit code `1` means the whole call failed or was refused, or `auth status` reports an unusable login.
Exit code `2` means invalid CLI usage.
Errors use fixed text or approved safe codes, never raw network errors or provider response bodies.

## Write policy

The write policy in `config.toml` sets, for each account, what the server does with each write action.
Each policy key has one of three decisions:

- `allow`: the server runs the action now.
- `approve`: the server stores the action as a pending item. The action does not run.
- `deny`: the server refuses the action.

**Caution:** Do not set `approve` unless the [approval password](#approval-password) is set.
Only a person in the [approval console](#approval-console) can approve or deny a pending item. Without the password, the console is off and a pending item stays pending.
The CLI and the API cannot approve or deny. `approvals status ID` shows the state of a pending item. See [Results](#results).

Example:

```toml
[policy]
calendar_invite = "allow"
calendar_respond = "allow"

[policy.sealed]
mail_trash = "allow"

[accounts.work.policy]
calendar_update = "allow"
calendar_cancel = "allow"

[accounts.personal.policy]
mail_draft = "deny"

[accounts.personal.policy.sealed]
mail_trash = "deny"
```

| Key | Commands | Default |
| --- | --- | --- |
| `mail_trash` | `mail trash`, and rule moves to `trash` | `allow` |
| `mail_draft` | `draft new`, `draft reply`, `draft forward`, `draft update` | `allow` |
| `mail_send` | None. There is no send command. | `deny` |
| `calendar_invite` | `calendar invite` | `deny` |
| `calendar_update` | `calendar update` | `deny` |
| `calendar_cancel` | `calendar cancel` | `deny` |
| `calendar_respond` | `calendar respond` | `deny` |

For each key, the server uses the first value in this order: `[accounts.NAME.policy]`, `[policy]`, the default.
These commands have no key, and the policy always allows them: `mail read`, `mail unread`, `mail flag`, `mail categorize`, `mail archive`, and `mail move`.
In `mail organize --apply`, only a rule move to `trash` has a key.

The server reads the policy at start.
After a policy change, requests for each changed account fail with `account configuration changed; restart required` until you restart the server.
If the changed policy is invalid, requests fail with `configuration unavailable`.
An invalid policy stops `serve`, `auth login`, `auth set-jev-key`, `auth set-approval-password`, `auth migrate`, and `auth change-password` with `invalid policy: KEY_PATH`.
For example: `invalid policy: accounts.personal.policy.sealed.calendar_invite`. The error never shows the value.
A policy is invalid if it has an unknown key or table, a value other than `allow`, `approve`, or `deny`, or a calendar key in a sealed table.

### Sealed messages

The sealed tables apply to actions on an existing message: `mail trash`, `draft reply`, and `draft forward`.
A sealed table accepts only `mail_trash`, `mail_draft`, and `mail_send`.
For each key, the sealed decision is the first value in this order: `[accounts.NAME.policy.sealed]`, `[policy.sealed]`, `deny`.

If the referenced message is `sealed` or `error`, the server uses the stricter of the base decision and the sealed decision.
The order is `allow`, then `approve`, then `deny`.
The server looks up the verdict only when the sealed decision is stricter than the base decision.
If the message has no cached verdict, the server screens it first. A message that the server cannot screen counts as `sealed`.
For `green`, `yellow`, and `red` messages, only the base decision applies.
`draft new` and `draft update` do not reference a received message, so only the base decision applies.

### Results

- `allow`: the command gives its normal result.
- `deny`: a draft or meeting command fails with `refused by policy: KEY`, for example `refused by policy: calendar_invite`, with exit code `1`. The action does not run.
- `approve`: a draft or meeting command returns `{"status":"pending","approval_id":"...","url":"http://localhost:8788/a/..."}` with exit code `0`. The action does not run. The `url` is the page of this item in the approval console. It uses `[approvals].listen` (see [Approvals configuration](#approvals-configuration)).

The server validates a meeting action before it stores it.
Thus an invalid time, a wrong role, or a recurring or cancelled event fails with the usual error, not `pending`.

`mail trash` decides each message ID on its own.
The server trashes the allowed IDs.
A denied ID gets `"ok": false` and `"error": "refused by policy: mail_trash"`.
An ID that needs approval gets `"ok": false`, `"error": "pending"`, and an `approval_id`, but no `url`. Each such ID is its own pending item.

`approvals status ID` returns `{"state":"..."}` for a pending item. The state is `pending`, `running`, `done`, `denied`, or `failed`.
A `done` item also has `result`: the normal result of the command.
The output never shows the stored request arguments.
An unknown ID fails with `approval not found` and exit code `1`.

Rules act only on green messages.
A rule with `folder = "trash"` follows `mail_trash` for each message.
In the `mail organize --apply` report, the `move.result` of such a message is `pending` or `refused by policy: mail_trash`.
The report does not show the approval ID, and the stats count the message as `failed`.
The message stays incomplete, so each later `--apply` run makes a new pending item for it.

On Microsoft 365, `mail move` to Deleted Items or to a folder below it fails with `move refused: use mail.trash`.
Use `mail trash` instead.
A rule move to such a folder also fails, and the report shows `GuardError`. Use `folder = "trash"` in the rule.
On Microsoft 365, `mail folders`, `mail move`, `mail organize`, and `rules list` now also look up Deleted Items. If this lookup fails, the command fails.
Gmail moves do not change.

### Approvals configuration

The optional `[approvals]` table in `config.toml` sets the address of the [approval console](#approval-console).

```toml
[approvals]
listen = "localhost:8788"
hosts = ["my-mac.example.ts.net"]
```

- `listen`: `HOST:PORT`. The default is `localhost:8788`. The console listens on this address, and the pending `url` uses it.
- `hosts`: extra host names that the console accepts, for example a Tailscale name. Each entry is a host name or IP address with an optional `:PORT`. An entry without a port matches a request without a port. Put an IPv6 address in brackets. The default is none.

The server reads `[approvals]` at start. Restart the server after a change.
An invalid table stops `serve`, `auth login`, `auth set-jev-key`, `auth set-approval-password`, `auth migrate`, and `auth change-password` with `invalid approvals: KEY_PATH`, for example `invalid approvals: approvals.listen`. The error never shows the value.

### Approval password

The approval password protects the [approval console](#approval-console). Without it, the console is off.

Run `agentmailguard auth set-approval-password --home /private/path/agentmailguard` locally on the server host to set or change it.
If a password is already set, the command first asks for the current password. A wrong current password fails with `approval password is wrong`, and nothing changes.
Then it asks for the new password. On a terminal, echo is off and it asks for the new password twice.
Non-terminal stdin supplies the lines in the same order without a confirmation: the file backend password (if `AGENTMAILGUARD_CREDENTIALS_PASSWORD` is absent), the current approval password (if one is set), and then the new password.
The password must be 8 to 72 bytes of valid UTF-8 without control characters.
The command stores only a bcrypt hash of the password in the credential store (Keychain or the encrypted file). It never prints the password or the hash, and it does not accept them as arguments.
It refuses `--server`, `AGENTMAILGUARD_SERVER`, and `--account`.

The server reads the hash at start and shows `credential check: approval password: ok` or `not set`. A missing password does not stop the start.
Restart the server after you set or change the password.

### Approval console

The approval console is a small web page where you approve or deny pending items.
To turn it on, set the [approval password](#approval-password) and restart the server.
At start, the server then shows `approval console listening`.
The console listens on `[approvals].listen`, by default `http://localhost:8788`. The pending `url` opens the page of its item.
If the console address is taken, the server does not start and shows `approval console listener unavailable`.

The browser asks for a username and the password. The console ignores the username.

- The list page `/` shows all pending items, newest first, and then the 50 most recently changed other items. Each row shows the account, the operation, the recipients, the subject, the state, and the age.
- The item page `/a/ID` shows the account, the operation, the state, the times, and every stored request field as plain text. Links in the text are never links on the page.
- For an action on an existing message, the item page also shows the sender, subject, received date, and verdict of the message. For an action on an event, it shows the subject, start, end, and organizer. The server reads these through the screened read path and never shows a body. If the read fails, the page shows `not available`, and you can still decide.
- A pending item has **Approve** and **Deny** buttons.

**Approve** runs the stored action once, with no second policy check. A `done` item shows its result. If the run fails, the item shows `failed` and a safe error code. A failed item never runs again.
**Deny** stops the item for good. Nothing runs.
An item that is already handled shows `already handled`, and nothing runs again.

To use the console from a phone, put it behind an HTTPS tunnel, for example `tailscale serve`, and add the tunnel host name to `[approvals].hosts`, for example `hosts = ["my-mac.example.ts.net"]`.
Keep `listen` on loopback. If `listen` is not `localhost` or a loopback address, the server shows a warning at start.

Protections:

- Every page needs the password (HTTP Basic authentication). The console has no TLS of its own.
- The console refuses a request whose `Host` is not `listen` or an entry of `hosts`. This blocks DNS rebinding.
- Approve and Deny need an `Origin` of the console itself. This blocks form posts from other sites.
- After 5 wrong passwords in one minute, the console refuses every request with HTTP 429 until the oldest failure is more than one minute old.
- Each wrong password adds a `console_signin` record to the audit log.
- Pages have no scripts and no remote resources, and browsers must not cache or frame them.

Known limits:

- Do not forward the console port through an SSH tunnel that the agent can use. Then the agent could reach the console, and only the password would stop it.
- An agent with a shell as the same OS user can go around the console, for example by changing `cache.db` or `config.toml`.

### Storage and audit

Pending items are in the `pending` table of `cache.db`. They do not expire.
A pending item stores the account, the operation, and the request arguments: recipients, subject, body, comment, event fields, and the message or event ID.
`cache clear` does not delete pending items.

The audit log gets content-free records for the policy. Their `operation` is the API operation name, for example `mail.trash`.

- `"event": "policy"` records each decision, also `allow`. It has `time`, `account`, `operation`, `key`, `decision`, and `reasons` (`policy`, and `sealed` if the sealed decision made it stricter). It can also have `id`, `rule`, and `approval_id`.
- `"event": "approval"` records each step of a pending item. It has `time`, `account`, `operation`, `state` (`created`, `approved`, `denied`, `done`, or `failed`), and `approval_id`.
- `"event": "console_signin"` records each wrong approval console password. It has only `time` and `result` (`failed`). It never has the username, the password, the host name, or the address of the browser.

### Upgrade notes

1. Meeting commands are now denied by default. To use them, set the calendar keys to `allow`, then restart the server.
2. `mail trash`, `draft reply`, and `draft forward` on `sealed` or `error` messages are now denied by default. To change this, set `[policy.sealed]`.
3. The cache database gets the `pending` table (schema version 5). After the upgrade, `serve` of an older binary fails with `server state unavailable`.
4. Pending items store request text in `cache.db`. Earlier versions kept no subject, recipient, or body text in storage.
5. After you set an approval password, older builds refuse the credential record. Records without an approval password keep the old format.

### Known limits

The policy is effective only when the agent can use only agentmailguard commands.
An agent with a shell as the same OS user can edit `config.toml`, change pending items in `cache.db`, or restart the server.
Anyone who can reach the listener gets the same decisions as the agent.

## Microsoft 365 meetings

**Caution:** If the write policy allows them, `calendar invite`, `calendar update`, `calendar cancel`, and `calendar respond` send real meeting notifications at once.
No outbound screening checks the subject, body, location, comment, or attendees.
An agent must run these commands only after the user authorizes that specific action.

The [write policy](#write-policy) denies these commands by default.
To use them, set `calendar_invite`, `calendar_update`, `calendar_cancel`, and `calendar_respond` to `allow` in `config.toml`, then restart the server.
These commands work only for Microsoft 365 accounts.
Gmail accounts get the fixed error `calendar writes are not supported for this account`, before any credential or provider access.

Microsoft 365 login requests the `Calendars.ReadWrite` scope, and token refreshes request it too.
Without this grant, meeting commands fail with `missing_scopes` before any Graph calendar request. A token refresh can occur before this check.
If an account logged in before this scope was added, a refresh can need new consent.
In that case, run `auth login` again on the server host and restart the server.

Times use the format `YYYY-MM-DD HH:MM` in the local timezone of the client.
The client converts them to RFC 3339 with the local offset before it sends the request.
The client refuses other formats, times that do not exist because of a DST change, and years outside 1–9999.
The client does not refuse a time that occurs two times when clocks go back. It uses one of the two offsets, so do not use such a time.
Go parses `--duration`, for example `30m` or `1h30m`. The duration must be more than zero.
An invite needs exactly one of `--end` and `--duration`. An update can use one of them, but not both.

An invite needs one or more `--to` addresses and an explicit `--subject`, which can be empty.
The body is empty if you omit `--body`.
`--body` is plain text.
The invite sends it as escaped HTML and keeps the line breaks.
For a Teams meeting, Microsoft adds the Teams join block to the body.
Every invite is a Microsoft Teams meeting by default. Use `--no-teams` to create a meeting without an online meeting. `--teams` is accepted, and the last of `--teams` and `--no-teams` wins.
All invite attendees are required attendees.

An update needs at least one change and sends only the options that you supply.
An explicit empty `--subject ''` or `--location ''` clears that field.
If an update changes only `--start`, the server keeps the current duration.
`--duration` alone or `--end` alone keeps the current start.
Attendee changes keep the type of existing attendees. Added attendees are required attendees.
`calendar update ID --teams` adds a Teams meeting to the same event and sends an update notification to the attendees. It does not create a new event.
With `--teams` alone, the update sends only online-meeting settings.
It does not replace the body or change the times or attendees. Microsoft Graph can add Teams join details to the body.
An update without `--teams` leaves the online meeting unchanged. Updates cannot remove a Teams meeting, change the online-meeting provider, or change the body, so an existing Teams link stays in the body.
`--teams=false` on an update is invalid usage.

Only the organizer can update or cancel an event.
Only an attendee can respond, and the response always goes to the organizer.
There is no silent response and no proposal of a new time.
The server refuses recurring events and cancelled events. It does not write in these cases.

Invites accept 1–100 attendee addresses, and each update attendee list accepts at most 100 addresses.
There is no limit of 20 attendees or 8 hours, and there is no distribution-list detection.

The client does not retry a meeting request, and the server sends no transaction ID.
A meeting command can return `operation failed` after the notification was sent, for example if the audit write fails.
A timeout, transport error, or `operation failed` does not prove that the meeting action failed.
Before you retry such a command, use `calendar list` or `calendar get` to verify the outcome.
A second invite can send duplicate invitations.

## Microsoft 365 availability

`calendar availability` shows the free/busy status of participants in 30-minute slots.
It is read-only. It sends no notification, reserves no slot, and writes no audit record.
It works only for Microsoft 365 accounts. Gmail accounts get the fixed error `calendar availability is not supported for this account`, before any credential or provider access.
The `Calendars.ReadBasic`, `Calendars.Read`, or `Calendars.ReadWrite` grant is sufficient. Without one of these grants, the command fails with `missing_scopes` before any Graph request.

```sh
agentmailguard --account work calendar availability --to me@example.com --to ana@example.com --start '2026-10-09 09:00' --end '2026-10-09 18:00'
```

The command needs 1–20 distinct `--to` addresses, a `--start`, and an `--end`.
The window must be less than 62 days. These are Microsoft Graph limits for one query.
Times use the same local format as the meeting commands. The interval is always 30 minutes.

The result has `start`, `end`, `interval_minutes`, and one entry in `schedules` for each requested address, in the request order.
Each entry has the requested `address` and a `slots` list.
Slot `i` starts at `start` plus `30 * i` minutes. The last slot ends at `end`, so it can be shorter than 30 minutes.
Each slot is `free`, `tentative`, `busy`, `out_of_office`, or `unknown`.
Microsoft Graph reports "working elsewhere" as `free`.
The result never contains event subjects, locations, or provider error text.

`unknown` means that no usable data came back for that participant.
Sharing permissions in the Microsoft 365 organization can cause this result. A missing, duplicate, failed, or malformed entry also gives `unknown`.
`unknown` never means free.
A slot is free for a meeting only if every requested participant has `free` in that slot.

The result is a snapshot. Calendars can change after the query.
Before you send an invite, run the query again for the selected slot.
`calendar list` shows only the visible events of the selected account. It does not show that other participants are free.

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
A `trash` target follows the `mail_trash` key of the [write policy](#write-policy).
On Microsoft 365, a folder ID of Deleted Items or of a folder below it fails. Use `trash`.
A category target must already exist in the mailbox.
`rules list` shows resolved names and missing-target warnings, never instructions or confidence scores.
Missing rules produce an empty organizer report without provider or Jev access.
Invalid rules fail before provider access.
`stats` and `cache clear` need neither account configuration nor provider authentication.
Cache clear removes all accounts' verdicts, saved organization answers, and completion markers.
It does not delete pending items.

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
Manual mutations work at every verdict, but the [write policy](#write-policy) can refuse or hold `mail trash`, also by verdict.
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

Operation names match CLI groups, such as `mail.get`, `draft.update`, `calendar.invite`, `cache.clear`, and `approvals.status`.
Meeting times on the HTTP interface are RFC 3339 timestamps. `calendar.update` also accepts a Go `duration` string.
The `account` field is optional, and `params` must be an object.
Unknown operations or fields, duplicate keys, malformed JSON, trailing JSON, null fields, invalid types, and oversized requests are refused.
There is no HTTP login, token, key, raw-message, classification, rules-mutation, or approval endpoint. Only the [approval console](#approval-console), on its own address, approves or denies pending items.
A pending action returns HTTP 200 with `{"status":"pending","approval_id":"...","url":"..."}`.
`approvals.status` takes `{"id":"APPROVAL_ID"}` (32 lowercase hex characters) and ignores `account`. An unknown ID returns HTTP 404 with `{"error":"approval not found"}`.
A policy refusal returns HTTP 500 with `{"error":"refused by policy: KEY"}`.

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
A second native workflow sends meeting invites, updates, cancellations, and responses to a fake Graph endpoint, and reads the changed events back.
It also covers the Gmail, missing-scope, recurring, cancelled, and wrong-role refusals.
Offline tests also cover the write policy: validation, defaults, sealed decisions, per-ID trash decisions, rule trash, pending items, and the Deleted Items refusal.
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
No live Microsoft 365 meeting write has been verified. Graph acceptance of invites, updates, cancellations, and responses remains unverified.
Native Keychain migration and the user's real credential migration also remain unverified.
Migration tests use a fake Keychain Platform at the constructor and CLI seams, not the production Security APIs.
Production-binary Keychain migration remains deferred; the migration evidence for this change comes from these fake-Platform tests.
Offline verification does not establish production readiness.

## License

Copyright 2026 Ivan Pereira.
Licensed under the [Apache License, Version 2.0](LICENSE) (`Apache-2.0`).
Dependencies retain their own licenses and attribution requirements.
Binary distributions must include the applicable third-party license notices.
