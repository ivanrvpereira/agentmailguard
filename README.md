# agentmailguard

Guarded Microsoft 365 and Gmail access for AI agents, with a long-lived server and a thin CLI.
The guard limits what an agent sees in mail and calendar events.
It does not protect a human from fraud.

**Exposure warning:** the client/server connection has no authentication and no built-in TLS.
Anyone who can reach the listener can read guarded mailbox data, change mail, and clear cached verdicts.
They can also do every action that the [write policy](#write-policy) allows, for example create drafts, send mail, and send meeting invitations, updates, cancellations, and responses.
The default listener is `127.0.0.1:8787`.
A non-loopback listener requires an explicit `--listen` option and prints a warning.
Browser requests with an `Origin` header are refused, and the server does not enable CORS.
These restrictions are not a substitute for a protected network or encrypted tunnel.
Provider OAuth is required.

## Privacy and external screening

**Privacy warning:** Screening is an external service, not a local-only check.
By default, the server sends requests to OpenRouter's System One endpoint for model `typesafe/jev-1.13`.
The [`[screening]`](#screening) table can set another endpoint and model, which then receives the same data.
The fake screening engine sends nothing, but it is only for tests and local work.

- Mail requests include rendered body text, subject, sender and recipient identities, attachment names, authentication results, and link domains.
- Calendar requests include subject, organizer, attendees, location, recurrence, meeting join URLs, and other event metadata.
- Private-event bodies are excluded, but private-event metadata and accepted meeting join URLs are still included.
- Organization requests include the rule instructions used for classification.
- [Action checks](#jev-action-check) include the recipients, subject, body, comment, and event fields that the agent wrote, and for a forward the subject and body of the forwarded message.

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

## Build and checks

1. Install a currently supported, patched Go release (minimum Go 1.26) and a C compiler with CGO support: the Xcode Command Line Tools on macOS, or your distribution's compiler toolchain on Linux.
2. Run `make` from this repository.
3. Run `./bin/agentmailguard --help`.

`make` checks formatting, runs `go vet` and race tests, builds the packages, and creates `bin/agentmailguard`.
Both credential backends need CGO for SQLite. The Keychain backend works only on macOS. Windows is not supported.
There is no plaintext credential fallback.
The build does not install anything into your PATH.

## Server configuration

The supported deployment is one server setup per machine, with multiple configured accounts in one server home.
Each account has a distinct configuration name.
Independent server homes with separate Keychain identities are not supported.

The server owns configuration, OAuth credentials, the screening key, `cache.db`, and `audit.jsonl`.
The CLI never loads these files for data commands, even with a loopback server.
The server home is `--home`, then `AGENTMAILGUARD_HOME`, then `~/.agentmailguard`.
`serve` and the local `auth` commands (`login`, `set-screening-key`, `set-approval-password`, `migrate`, `change-password`) run on the server host and accept `--home`.
The local `auth` commands refuse `--server` and `AGENTMAILGUARD_SERVER`. All of them except `auth login` also refuse `--account`.

The server reads `config.toml` and the credential store (logins, the screening key, and the approval password hash) only at start. **After a change to any of them, restart the server.**
Until the restart, requests for an account whose configuration or policy changed fail with `account configuration changed; restart required`, and an invalid change gives `configuration unavailable`.
`rules.toml` is different: the server reads and validates it on every `rules list` and `mail organize` call.

Create a private server-home directory and a `config.toml` file there.
Prefer a restricted provider application that grants only the required scopes.
The [setup guide](docs/setup.md) lists the exact scopes that login requests.
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
Keep `config.toml` private because the Gmail application secret belongs on the server host.
The optional `[policy]` tables in `config.toml` set the [write policy](#write-policy).

## Fake provider

A fake account has a local mailbox and calendar in a SQLite file. Use it to run real workflows through the real CLI and server without a provider, a login, or Keychain access.

```toml
[accounts.demo]
provider = "fake"
```

A fake account needs no `client_id`, `tenant_id`, or `client_secret`, and `auth login` refuses it with `fake accounts need no login`. The `[policy]` tables and `rules.toml` work as for other accounts. At start, `serve` shows `account "demo" (fake): no login needed`, and `auth status` shows the account address with no scopes.

All fake accounts share `fake-provider.db` in the server home, for example `~/.agentmailguard/fake-provider.db`. Each row has an `account` column with the account name. The server creates the file with mode 0600 and applies the schema in [`internal/providers/fake/schema.sql`](internal/providers/fake/schema.sql) when it first opens the file. The schema file explains each table and column. Each account gets the folders `inbox`, `drafts`, `sent`, `archive`, and `trash`. The account address is `<account>@fake.test` unless the `identities` table has a row for the account.

The server reads and writes `fake-provider.db` on every call, so you can seed and inspect it with `sqlite3` while the server runs:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db < internal/providers/fake/schema.sql
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, to_addresses, subject, body, received_at, spf, dkim, dmarc) VALUES ('demo', 'm1', 'Ana', 'ana@example.test', '[\"demo@fake.test\"]', 'Lunch on Friday?', 'Are you free at noon?', '2026-04-01T09:30:00Z', 'pass', 'pass', 'pass');"
sqlite3 ~/.agentmailguard/fake-provider.db "SELECT id, folder, subject, origin FROM messages WHERE account = 'demo';"
```

Trash and archive are moves to the `trash` and `archive` folders; nothing is ever deleted. Drafts go to `drafts`. `mail send`, `mail reply`, and `mail forward` store the message in `sent`, with `origin` (`new`, `reply`, or `forward`) and `source_id`. Nothing leaves the machine.

To seed a conversation, give its messages the same `thread_id`. A message whose `thread_id` is NULL is its own conversation, and its `thread_id` in results is its message ID. Reply and forward drafts, and sent replies and forwards, join the conversation of their source message; new drafts and new sends are their own conversation. A `fake-provider.db` made before the `thread_id` column existed gets the column when the server first opens it. For example, a reply from Ana in the conversation of `m1`:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, subject, body, thread_id) VALUES ('demo', 'm2', 'Ana', 'ana@example.test', 'Re: Lunch on Friday?', 'Noon works.', 'm1');"
```

The calendar uses three tables. `events` holds the events, `attendees` holds the attendees of each event and their responses, and `free_busy` holds the free/busy times that `calendar availability` reads. The account organizes an event when its `organizer_address` is empty or is the account address; only then can it update or cancel the event. It can respond only to the other events. Availability comes only from `free_busy`, never from `events`. An address without `free_busy` rows is `unknown`, and a slot that no row covers is `free`. For example, an event that Ana organizes with the demo account as attendee, and Ana's busy time:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO events (account, id, subject, start_at, end_at, organizer_name, organizer_address) VALUES ('demo', 'e1', 'Planning', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'Ana', 'ana@example.test'); INSERT INTO attendees (account, event_id, address, name) VALUES ('demo', 'e1', 'demo@fake.test', 'Demo'); INSERT INTO free_busy (account, address, start_at, end_at, status) VALUES ('demo', 'ana@example.test', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'busy');"
```

`calendar invite`, `update`, `cancel`, and `respond` change only `fake-provider.db`, under the write policy as for other accounts. No notification leaves the machine. A cancel sets `is_cancelled` and `cancel_comment` and deletes nothing. A response sets the account's own row in `attendees`; `--no-notify` changes nothing else. An invite stores `--optional` attendees with `optional` set to 1 in `attendees`. A `fake-provider.db` made before the `optional` column existed gets the column, with 0 for every existing attendee, when the server first opens it.

Fake mail is untrusted like real mail. The configured [screening](#screening) engine screens it. With the fake screening engine, the server needs no screening key.

## Screening

The server screens every message and checks the text of agent actions with a decision engine. The optional `[screening]` table selects the engine:

```toml
[screening]
provider = "jev"                                     # Default. Use "fake" for the keyword engine.
endpoint = "https://openrouter.ai/api/v1/systemone"  # Default. Used only by "jev".
model = "typesafe/jev-1.13"                          # Default. Used only by "jev".
```

- `jev` calls Jev at `endpoint` with the stored screening key. The endpoint can be OpenRouter, Jev cloud, or another host that serves the same protocol. It must be an `https` URL with a host name, no user name or password, and no fragment, because each request carries mail text and the key. A port is optional and must be from 1 to 65535.
- `model` names the model that `jev` asks. The default, `typesafe/jev-1.13`, is the model name at OpenRouter. Jev cloud uses other model names. For example, to use Jev cloud:

  ```toml
  [screening]
  endpoint = "https://api.typesafe.ai/v1/systemone"
  model = "jev-1.13.0"
  ```

  A model name starts with a letter or a digit. It contains only letters, digits, `.`, `_`, `:`, `/`, and `-`, and it has a maximum of 128 characters. The screening thresholds are tuned for jev-1.13, so another model can change the verdicts.
- `fake` decides by keywords in the message or action. It makes no network calls and needs no key. It is for tests and local development:
  - "ignore previous instructions" makes a message `red`;
  - "verification code" makes a message `sealed`;
  - "SCREENFAIL" makes the screening fail, as when the engine is down;
  - "password" flags an action, so an allowed action waits for approval;
  - a folder or category rule is chosen when its rule name (the table name in `rules.toml`) is in the message.
- At start, `serve` shows `screening key: not needed (fake screening)`. If a configured account is not a fake account, `serve` also prints a warning: the mail of that account is then judged by keywords, not by a decision model.
- An absent table selects `jev` with the default endpoint and model. An unknown key or provider, an invalid endpoint, or an invalid model stops `serve` with `invalid screening: KEY_PATH`, for example `invalid screening: screening.endpoint` or `invalid screening: screening.model`. The error never shows the value.

## Credential storage

Both backends store all provider OAuth credentials, the screening key, and the [approval password](#approval-password) hash in one versioned record.
The optional configuration table selects the backend:

```toml
[credentials]
backend = "keychain" # Default. Use "file" for password-encrypted storage.
```

An absent table selects `keychain`.
Linux requires `backend = "file"` because the native Keychain backend is macOS-only.
Any other key or value is a configuration error.
Neither backend detects rollback: another process under the same user can restore an older valid credential record.

### Keychain backend

The Keychain backend uses one non-synchronizable generic-password item: service `agentmailguard`, account `credentials`.
All configured accounts share this item under the current operating-system user, with separate entries keyed by provider and account name.
Choose "Always Allow" for this item to avoid repeated access prompts for the same build.
A rebuilt binary needs approval again.

### File backend and password input

The file backend stores the record in `<home>/credentials.age` with mode `0600`.
It uses age password encryption, and writes are atomic.
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

Run `agentmailguard auth set-screening-key --home /private/path/agentmailguard` locally to save the screening key.
Terminal input disables echo. Non-terminal stdin supplies the key as one line.
With the file backend and no password environment variable, piped input must contain two lines: the password first, then the key.
With the password environment variable, piped input contains only the key line.
The command refuses keys with spaces or control characters. It never prints the key or accepts it as an argument.

The server reads the screening key only from the credential store. It ignores `OPENROUTER_API_KEY`, so a key set for other tools is never sent to a configured endpoint.
No remote client command transfers a provider token or screening key to the server.

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
It refuses an existing target record. It also refuses a target that matches the configured backend.
The file password comes from the normal sources. Terminal creation of the file target asks twice.

### Change the file password

1. Stop the server.
2. Run `./bin/agentmailguard auth change-password --home /private/path/agentmailguard` on a terminal.
3. Supply the current password through its normal source.
4. Enter the new password twice at the terminal prompts.
5. Restart the server with the new password.

This command works only with the file backend and re-encrypts the latest record under the same-home lock.
The new password cannot come from an environment variable or a pipe.
A running server retains its old password and cannot save refreshed tokens after this change.

## Local login and server start

Run login locally on the server host:

```sh
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard --device-code
./bin/agentmailguard --account personal auth login --home /private/path/agentmailguard --no-browser
./bin/agentmailguard serve --home /private/path/agentmailguard
```

`serve` loads every credential before it accepts requests, because an operator is present at server start:

1. It reads the credential store and writes it back once, so any Keychain prompt appears at start.
2. Its first `credential check:` item is `credentials (keychain)` or `credentials (file)`. A store failure refuses startup.
3. It checks every configured account. It also checks the stored screening key, unless `[screening]` selects the fake engine. Check lines contain fixed statuses, not secrets.
4. If the screening key is missing and stdin is a terminal, it prompts with echo disabled and saves the key. Piped password input gets no key prompt.
5. If every check passes, it asks the provider about each saved login, with a 20-second limit per account. The account line then shows the provider's answer.

Each account has one line. Its status names the step that failed:

- `ok`: a login is saved, and the provider accepted it.
- `no login needed`: a fake account, which reads no credentials.
- `not logged in; ...`: no login is saved for this account.
- `credential record unreadable; run auth login`, `Keychain access failed or denied`, or `Keychain write failed or denied`: the credential store failed for this account.
- `sign-in failed; ...`: a login is saved, but the provider refused it or did not answer. The cause can be an expired or revoked login, wrong client settings in `config.toml`, or a network problem. The provider codes cannot tell these apart.

An account that is not logged in, or whose sign-in failed, does not block startup.
Its commands fail until the cause is fixed.
A login can also expire or be revoked while the server runs. Commands for that account then fail with `sign-in failed; check the network, or run auth login and restart the server`.
Other credential failures, a missing screening key, or missing configuration refuse startup.
Store statuses include `ok`, `access denied`, `wrong password or damaged file`, `password missing`, and `unsupported version`.

After startup, the server keeps credentials in memory. A token refresh saves to the store without losing the other accounts' entries or the screening key.

Browser login is the default.
Microsoft 365 supports device-code login.
Gmail refuses device-code login because that flow does not support the required scopes.

On a headless host, use Gmail login with `--no-browser`. It needs no SSH tunnel; the steps are in [Headless login](docs/setup.md#headless-login).
The pasted address gets the same checks as the loopback callback: host, port, path, and `state` must match this sign-in.
If the address does not match, login asks for it again. A provider error in a matching address ends the login.
The PKCE verifier stays on the server host, so the code in the address cannot be exchanged anywhere else.
The program does not print or log the pasted address. The terminal shows it as you paste it.
Without `--no-browser`, Gmail login opens the URL with `open` on macOS or `xdg-open` on Linux. If that fails, login continues and accepts the loopback callback or a pasted address.
`--no-browser` is refused for Microsoft 365. Use `--device-code` on a headless host.

A successful login prints a final `✓ Signed in as ...` line on stderr.
Stdout gets the JSON result with `account`, `username`, and `scopes`.
Data commands and `auth status` never open a login prompt.
Login and `auth status` print a warning on stderr for extra grants. `auth status` lists them in `extra_scopes`; `scopes` lists only requested grants. Application roles appear with a `role:` prefix.
Tokens that cannot work are refused: malformed tokens, and Gmail tokens without the mail and calendar scopes. Gmail refusals list `missing_scopes`.
The program does not start a server automatically.

Microsoft 365 `auth status` can use a cached token and username without a Graph request.
A successful status does not prove current Graph access, all required permissions, or provider acceptance of mailbox operations.
Login errors are fixed texts that name the failed step. They contain no raw provider errors, token values, or callback URLs.


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
Long `mail organize` scans need a longer `--timeout`.

For protected remote access, keep the server bound to loopback and use an SSH tunnel:

```sh
ssh -N -L 18787:127.0.0.1:8787 <server-host>
./bin/agentmailguard --server http://127.0.0.1:18787 --account work mail list
```

An explicit remote listener is available with `serve --listen ADDRESS:PORT`.
The tunnel protects the connection, not against other users who can already access either local listener.
Forward only the API port (8787). Do not forward the [approval console](#approval-console) port through a tunnel that the agent can use.

## Commands and output

Run `--help` without a server to see every command and option.
Recipient and category flags can repeat.
An explicit empty value, such as `draft update ID --body ''`, is not the same as an omitted flag.

| Group | Commands |
| --- | --- |
| State and auth | `stats --days 30`, `cache clear`, `rules list`, `approvals status ID`, `auth status`, local `auth login [--device-code] [--no-browser]`, `auth set-screening-key`, `auth set-approval-password`, `auth migrate --to file\|keychain`, `auth change-password` |
| Mail reads | `mail list`, `mail get ID [--pretty]`, `mail thread THREAD_ID`, `mail search`, `mail link ID L1 L2` or `mail link ID --all`, `mail folders`, `mail categories` |
| Mail changes | `mail archive IDS...`, `mail trash IDS...`, `mail move FOLDER_ID IDS...`, `mail read IDS...`, `mail unread IDS...`, `mail flag IDS... --state flagged|complete|none`, `mail categorize IDS... --add NAME --remove NAME` |
| Organization and calendar | `mail organize --since 7d [--until DATE] [--apply]`, `calendar list --from today --to +7d`, `calendar get ID`, `calendar search TEXT` |
| Availability (Microsoft 365 and fake, read-only) | `calendar availability --to ADDRESS [--to ADDRESS] --start 'YYYY-MM-DD HH:MM' --end 'YYYY-MM-DD HH:MM'` |
| Meetings (send notifications) | `calendar invite --to ADDRESS --subject TEXT --start 'YYYY-MM-DD HH:MM' --duration 30m\|--end 'YYYY-MM-DD HH:MM' [--optional ADDRESS] [--body TEXT] [--location TEXT] [--no-online]`, `calendar update ID [--subject TEXT] [--location TEXT] [--body TEXT] [--start TIME] [--duration D\|--end TIME] [--add-attendee ADDRESS] [--remove-attendee ADDRESS] [--online]`, `calendar cancel ID [--comment TEXT]`, `calendar respond ID --response accept\|tentative\|decline [--comment TEXT\|--no-notify]` |
| Mail send (sends at once) | `mail send --to ADDRESS --subject TEXT --body TEXT`, `mail reply ID --body TEXT`, `mail forward ID --to ADDRESS` |
| Drafts (no send) | `draft new --to ADDRESS --subject TEXT --body TEXT`, `draft reply ID --body TEXT`, `draft forward ID --to ADDRESS`, `draft update ID --body TEXT` |

Successful commands print the operation's JSON result.
`mail get --pretty` formats only the guarded view.
`mail search` needs at least one of `--text`, `--from`, `--to`, `--subject`, `--after`, or `--before`. `--to ADDRESS` matches the To and Cc fields: the Gmail `to:` and `cc:` operators, or the Microsoft 365 `to` and `cc` search properties.
`mail thread` prints JSON only. It returns the messages of the conversation as a list like `mail list`, newest first, from every folder, including trash, spam or junk, sent mail, and drafts. Gmail returns the whole thread (`threads.get`); on Microsoft 365 the server reads every page of the `conversationId` query. An unknown thread fails. Read bodies with `mail get`.
`calendar search TEXT` uses the provider's own search: Google Calendar `events.list` with `q` on Gmail, and the Microsoft Search API (`POST /search/query` for events) on Microsoft 365. It returns one page in the provider's default size and order, with no date range. Each event is screened as `calendar get` screens it, and the result has the fields of a `calendar list` result. The provider matches the text on its side, so a hidden subject or body can decide whether an event is in the result. The fake provider matches the subject, body, location, organizer, and attendees, ignoring case.
Draft update returns the resulting draft ID, which can change on Gmail.
`mail send`, `mail reply`, and `mail forward` return `{"ok": true}`. The providers give no ID for a sent message.
`calendar invite` returns the new event ID. The other meeting commands return the event ID and `"ok": true`.
The [write policy](#write-policy) can refuse trash, draft, send, and meeting commands, or store them as pending items. See [Results](#results) for these results.
There is no permanent-delete, folder creation, category creation, or arbitrary provider request command.

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
| `mail_send` | `mail send`, `mail reply`, `mail forward` | `deny` |
| `calendar_invite` | `calendar invite` | `deny` |
| `calendar_update` | `calendar update` | `deny` |
| `calendar_cancel` | `calendar cancel` | `deny` |
| `calendar_respond` | `calendar respond` | `deny` |

For each key, the server uses the first value in this order: `[accounts.NAME.policy]`, `[policy]`, the default.
These commands have no key, and the policy always allows them: `mail read`, `mail unread`, `mail flag`, `mail categorize`, `mail archive`, and `mail move`.
In `mail organize --apply`, only a rule move to `trash` has a key.

An invalid policy stops `serve` and the local `auth` commands with `invalid policy: KEY_PATH`.
For example: `invalid policy: accounts.personal.policy.sealed.calendar_invite`. The error never shows the value.
A policy is invalid if it has an unknown key or table, a value other than `allow`, `approve`, or `deny`, or a calendar key in a sealed table.

### Sealed messages

The sealed tables apply to actions on an existing message: `mail trash`, `draft reply`, `draft forward`, `mail reply`, and `mail forward`.
A sealed table accepts only `mail_trash`, `mail_draft`, and `mail_send`.
For each key, the sealed decision is the first value in this order: `[accounts.NAME.policy.sealed]`, `[policy.sealed]`, `deny`.

If the referenced message is `sealed` or `error`, the server uses the stricter of the base decision and the sealed decision.
The order is `allow`, then `approve`, then `deny`.
The server looks up the verdict only when the sealed decision is stricter than the base decision.
If the message has no cached verdict, the server screens it first. A message that the server cannot screen counts as `sealed`.
For `green`, `yellow`, and `red` messages, only the base decision applies.
`draft new`, `draft update`, and `mail send` do not reference a received message, so only the base decision applies.

### Jev action check

Before an action runs, Jev checks the text that the agent wrote for it. The agent can be under prompt injection from mail it read, so the server does not trust this text.

- Checked actions: `draft new`, `draft reply`, `draft update`, `draft forward`, `mail send`, `mail reply`, `mail forward`, `calendar invite`, and `calendar update`. `calendar respond` and `calendar cancel` are checked only when they have a comment, so `calendar respond --no-notify` is never checked.
- Not checked: `mail trash`, rule moves, and the commands without a policy key.
- Jev sees the operation name, a fixed description of what the action does, and the action fields that the agent gave: recipients (for `calendar invite`, also the `--optional` attendees), subject, body (for `calendar update`, the new `--body`), comment, response, and event fields. It never sees a message or event ID.
- For `draft forward` and `mail forward`, Jev also sees the subject and body of the forwarded message, read through the screened read path. If the server cannot read this text, for example because the message is not `green`, the check fails.

The check comes after the policy decision:

- `deny` refuses the action. Jev does not see it.
- If Jev flags the action, or the check fails, `allow` becomes `approve`, and the action waits in the [approval console](#approval-console). `approve` stays `approve`.
- A missing screening key or a Jev outage makes every check fail, so every checked action waits for approval.
- When a person approves an item, the server runs it without a second check.

Jev gives a score from 0 to 1. A score of 0.5 or more is a flag. This threshold is provisional and will change after tuning.
The agent never sees the score or the flag. It gets the normal result or the normal `pending` result.
The item page of the approval console shows the result, for example `Jev: flagged (score 0.62)`, `Jev: not flagged (score 0.08)`, or `Jev: check failed (TimeoutError)`.
If the [fake screening engine](#screening) made the check, the page shows `Fake check (keywords only):` instead of `Jev:`, for example `Fake check (keywords only): flagged (score 0.90)`. 

### Results

- `allow`: the command gives its normal result.
- `deny`: a draft, send, or meeting command fails with `refused by policy: KEY`, for example `refused by policy: calendar_invite`, with exit code `1`. The action does not run.
- `approve`: a draft, send, or meeting command returns `{"status":"pending","approval_id":"...","url":"http://localhost:8788/a/..."}` with exit code `0`. The action does not run. The `url` is the page of this item in the approval console. It uses `[approvals].listen` (see [Approvals configuration](#approvals-configuration)).

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

A rule with `folder = "trash"` follows `mail_trash` for each message.
In the `mail organize --apply` report, the `move.result` of such a message is `pending` or `refused by policy: mail_trash`.
The report does not show the approval ID, and the stats count the message as `failed`.
The message stays incomplete, so each later `--apply` run makes a new pending item for it.

On Microsoft 365, `mail move` to Deleted Items or to a folder below it fails with `move refused: use mail.trash`.
Use `mail trash` instead.
A rule move to such a folder also fails, and the report shows `GuardError`.
On Microsoft 365, `mail folders`, `mail move`, `mail organize`, and `rules list` fail if the server cannot look up Deleted Items.

### Approvals configuration

The optional `[approvals]` table in `config.toml` sets the address of the [approval console](#approval-console).

```toml
[approvals]
listen = "localhost:8788"
hosts = ["my-mac.example.ts.net"]
```

- `listen`: `HOST:PORT`. The default is `localhost:8788`. The console listens on this address, and the pending `url` uses it.
- `hosts`: extra host names that the console accepts, for example a Tailscale name. Each entry is a host name or IP address with an optional `:PORT`. An entry without a port matches a request without a port. Put an IPv6 address in brackets. The default is none.

An invalid table stops `serve` and the local `auth` commands with `invalid approvals: KEY_PATH`, for example `invalid approvals: approvals.listen`. The error never shows the value.

### Approval password

The approval password protects the [approval console](#approval-console). Without it, the console is off.

Run `agentmailguard auth set-approval-password --home /private/path/agentmailguard` locally on the server host to set or change it.
If a password is already set, the command first asks for the current password. A wrong current password fails with `approval password is wrong`, and nothing changes.
Then it asks for the new password. On a terminal, echo is off and it asks for the new password twice.
Non-terminal stdin supplies the lines in the same order without a confirmation: the file backend password (if `AGENTMAILGUARD_CREDENTIALS_PASSWORD` is absent), the current approval password (if one is set), and then the new password.
The password must be 8 to 72 bytes of valid UTF-8 without control characters.
The command stores only a bcrypt hash of the password in the credential store (Keychain or the encrypted file). It never prints the password or the hash, and it does not accept them as arguments.

The server reads the hash at start and shows `credential check: approval password: ok` or `not set`. A missing password does not stop the start.

### Approval console

The approval console is a small web page where you approve or deny pending items.
To turn it on, set the [approval password](#approval-password).
At start, the server then shows `approval console listening`.
If the console address is taken, the server does not start and shows `approval console listener unavailable`.

The browser asks for a username and the password. The console ignores the username.

- The list page `/` shows all pending items, newest first, and then the 50 most recently changed other items. Each row shows the account, the operation, the recipients, the subject, the state, and the age.
- The item page `/a/ID` shows the account, the operation, the state, the times, and every stored request field as plain text. Links in the text are never links on the page.
- For an action on an existing message, the item page also shows the sender, subject, received date, and verdict of the message. For an action on an event, it shows the subject, start, end, and organizer. The server reads these through the screened read path and never shows a body. If the read fails, the page shows `not available`, and you can still decide.
- The item page also shows the result of the [Jev action check](#jev-action-check), if a check ran.
- A pending item has **Approve** and **Deny** buttons.

**Approve** runs the stored action once, with no second policy check. A `done` item shows its result. If the run fails, the item shows `failed` and a safe error code. A failed item never runs again.
**Deny** stops the item for good. Nothing runs.
An item that is already handled shows `already handled`, and nothing runs again.

To use the console from a phone, put it behind an HTTPS tunnel, for example `tailscale serve`, and add the tunnel host name to `[approvals].hosts`.
Keep `listen` on loopback. If `listen` is not `localhost` or a loopback address, the server shows a warning at start.

Protections:

- Every page needs the password (HTTP Basic authentication). The console has no TLS of its own.
- The console refuses a request whose `Host` is not `listen` or an entry of `hosts`. This blocks DNS rebinding.
- Approve and Deny need an `Origin` of the console itself. This blocks form posts from other sites.
- After 5 wrong passwords in one minute, the console refuses every request with HTTP 429 until the oldest failure is more than one minute old.
- Each wrong password adds a `console_signin` record to the audit log.
- Pages have no scripts and no remote resources, and browsers must not cache or frame them.

### Storage and audit

Pending items are in the `pending` table of `cache.db`. They do not expire.
A pending item stores the account, the operation, and the request arguments: recipients, subject, body, comment, event fields, and the message or event ID.
It also stores the result of the Jev check, if a check ran: the flag and the score, or the safe error code, and whether the fake screening engine made the check.
`cache clear` does not delete pending items.

The audit log gets content-free records for the policy. Their `operation` is the API operation name, for example `mail.trash`.

- `"event": "policy"` records each decision, also `allow`. It has `time`, `account`, `operation`, `key`, `decision`, and `reasons`: `policy`; `sealed` if the sealed decision made it stricter; `jev_flag` if Jev flagged the action; `jev_error` if the Jev check failed. It can also have `id`, `rule`, and `approval_id`. It never has the Jev score.
- `"event": "approval"` records each step of a pending item. It has `time`, `account`, `operation`, `state` (`created`, `approved`, `denied`, `done`, or `failed`), and `approval_id`.
- `"event": "console_signin"` records each wrong approval console password. It has only `time` and `result` (`failed`). It never has the username, the password, the host name, or the address of the browser.

### Known limits

The policy is effective only when the agent can use only agentmailguard commands.
An agent with a shell as the same OS user can edit `config.toml`, change pending items in `cache.db`, or restart the server.
Anyone who can reach the listener gets the same decisions as the agent.
On Gmail, only the server code stops a send. The OAuth grant (`gmail.modify`) permits it.

## Mail send

**Caution:** If the write policy allows them, `mail send`, `mail reply`, and `mail forward` send real email at once. A sent message cannot be recalled.
The [Jev action check](#jev-action-check) can hold a command for approval, but it is a statistical check: it can miss harmful text.
An agent must run these commands only after the user authorizes that specific send.

The [write policy](#write-policy) key `mail_send` decides all three commands. The default is `deny`.
To use them, set `mail_send` to `allow` or `approve` in `config.toml`.
With `approve`, each send waits in the [approval console](#approval-console) and runs only after a person approves it.
`mail reply` and `mail forward` act on an existing message, so `[policy.sealed] mail_send` applies to them. By default, the server refuses a reply to or a forward of a `sealed` or `error` message.

The commands take the same options as the matching draft commands:

- `mail send --to ADDRESS [--to ADDRESS] --subject TEXT --body TEXT` sends a new plain-text message. `--subject` and `--body` can be empty, but you must give them.
- `mail reply ID --body TEXT` sends a reply to the sender of the message. On Gmail, the reply goes to the `Reply-To` address if the message has one, and the reply quotes the original text. It does not go to the other recipients.
- `mail forward ID --to ADDRESS [--to ADDRESS]` forwards the message with no added text. On Gmail, the original message is attached unchanged.

Both providers keep a copy in the sent mail folder. On Gmail, the server refuses a reply to or a forward of a draft.
A result of `{"ok": true}` means that the provider accepted the message. It does not prove delivery.

Microsoft 365 login requests the `Mail.Send` scope, and token refreshes request it too.
Without this grant, send commands fail with `missing_scopes` before the policy decision and before any Graph request.
After `missing_scopes`, run `auth login` again on the server host.

The server sends each request to the provider once. It does not retry a send, also not after a timeout or a rate limit, because a second send can deliver a duplicate message.
A send command can return `operation failed` after the message was sent, for example if the audit write fails.
A timeout, transport error, or `operation failed` does not prove that the send failed.
Before you retry such a command, ask the user to check the sent mail folder.

The audit record of a send has no content. A `mail send` record has no message ID, because the providers give none. A `mail reply` or `mail forward` record has the ID of the source message.

## Meetings

**Caution:** If the write policy allows them, `calendar invite`, `calendar update`, `calendar cancel`, and `calendar respond` (except `respond --no-notify` on Microsoft 365) send real meeting notifications at once.
The [Jev action check](#jev-action-check) can hold a command for approval, but it is a statistical check: it can miss harmful text.
An agent must run these commands only after the user authorizes that specific action.

The [write policy](#write-policy) denies these commands by default.
To use them, set `calendar_invite`, `calendar_update`, `calendar_cancel`, and `calendar_respond` to `allow` in `config.toml`.
These commands work for Microsoft 365, Gmail, and fake accounts. On Gmail, they change the primary calendar.

Microsoft 365 login requests the `Calendars.ReadWrite` scope, and token refreshes request it too.
Without this grant, meeting commands fail with `missing_scopes` before any Graph calendar request.
After `missing_scopes`, run `auth login` again on the server host.
Gmail login requests the `calendar.events.owned` scope. Without this grant, meeting commands fail with `missing_scopes` before any Google Calendar request.

Times use the format `YYYY-MM-DD HH:MM` in the local timezone of the client.
The client refuses other formats, times that do not exist because of a DST change, and years outside 1–9999.
The client does not refuse a time that occurs two times when clocks go back. It uses one of the two offsets, so do not use such a time.
`--duration` takes values such as `30m` or `1h30m` and must be more than zero.
An invite needs exactly one of `--end` and `--duration`. An update can use one of them, but not both.

An invite needs one or more `--to` addresses and an explicit `--subject`, which can be empty.
The body is empty if you omit `--body`.
`--body` is plain text.
The invite sends it as escaped HTML and keeps the line breaks, so the text cannot become markup or a link with hidden text.
Every invite has an online meeting link by default: a Microsoft Teams meeting on Microsoft 365, and a Google Meet link on Gmail.
For a Teams meeting, Microsoft adds the Teams join block to the body.
Use `--no-online` to create a meeting without an online meeting.
`--to` attendees are required attendees. `--optional ADDRESS` (repeatable) adds optional attendees: a Graph attendee of type `optional` on Microsoft 365, and a Google Calendar attendee with `"optional": true` on Gmail. Optional attendees also receive the invitation. An address in both `--to` and `--optional`, ignoring case, is an invalid request.
On Gmail, guests cannot invite other people to a meeting that the server creates or updates.

An update needs at least one change and sends only the options that you supply.
An explicit empty `--subject ''`, `--location ''`, or `--body ''` clears that field.
`--body` is plain text and replaces the whole body. As in an invite, it goes as escaped HTML that keeps the line breaks. On Microsoft 365, `--body` replaces the whole description, including the Teams join section. On Gmail, the Meet link is not part of the body and stays.
If an update changes only `--start`, the server keeps the current duration.
`--duration` alone or `--end` alone keeps the current start.
Attendee changes keep the type and the response of existing attendees. Added attendees are required attendees.
`calendar update ID --online` adds an online meeting to the same event and sends an update notification to the attendees. It does not create a new event.
On Microsoft 365, it adds a Teams meeting, and Microsoft Graph can add Teams join details to the body.
On Gmail, it adds a Google Meet link only if the event has no conference. If the event already has one, the update keeps it.
With `--online` alone, the update sends only online-meeting settings. It does not replace the body or change the times or attendees.
An update without `--online` leaves the online meeting unchanged. Updates cannot remove an online meeting or change the online-meeting provider. Without `--body`, an existing Teams link stays in the body.
`--online=false` on an update is invalid usage.

Only the organizer can update or cancel an event.
Only an attendee can respond. The response goes to the organizer unless you use `--no-notify`.
On Microsoft 365, `calendar respond --no-notify` records your response without notifying the organizer (Graph `sendResponse` set to `false`). `--no-notify` with `--comment` is invalid usage, because the comment would reach nobody; on the HTTP interface it is `invalid request`.
Google Calendar always notifies the organizer of a response. On Gmail, `calendar respond --no-notify` fails with `calendar response refused: responses without notification are not supported for this account`, before any credential or provider access.
There is no proposal of a new time.
On Gmail, a response changes only your own attendee entry, and the event must list you as an attendee.
The server refuses recurring events and cancelled events. It does not write in these cases.

Every Gmail meeting command notifies all guests (`sendUpdates=all`).
On Gmail, `calendar cancel` deletes the event and notifies the guests. Google keeps the deleted event on your calendar with the status `cancelled`, so it can be restored from the calendar trash for 30 days. The server never deletes an event permanently.
Google sends no cancellation message. On Gmail, `calendar cancel --comment` fails with `calendar cancel refused: comments are not supported for this account`, before any credential or provider access. On Microsoft 365, the comment goes with the cancellation.

Invites accept 1–100 required attendee addresses and at most 100 optional ones, and each update attendee list accepts at most 100 addresses.

The client does not retry a meeting request, and the server sends no transaction ID.
A meeting command can return `operation failed` after the notification was sent, for example if the audit write fails.
A timeout, transport error, or `operation failed` does not prove that the meeting action failed.
Before you retry such a command, use `calendar list` or `calendar get` to verify the outcome.
A second invite can send duplicate invitations.

## Calendar availability

`calendar availability` shows the free/busy status of participants in 30-minute slots.
It is read-only. It sends no notification, reserves no slot, and writes no audit record.
It works only for Microsoft 365 and [fake](#fake-provider) accounts. Gmail accounts get the fixed error `calendar availability is not supported for this account`, before any credential or provider access.
The `Calendars.ReadBasic`, `Calendars.Read`, or `Calendars.ReadWrite` grant is sufficient. Without one of these grants, the command fails with `missing_scopes` before any Graph request. Fake accounts need no grant.

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
On Microsoft 365, a folder ID of Deleted Items or of a folder below it fails. Use `trash`.
A category target must already exist in the mailbox.
`rules list` shows resolved names and missing-target warnings, never instructions or confidence scores.
Missing rules produce an empty organizer report without provider or Jev access.
Invalid rules fail before provider access.
`stats` and `cache clear` need neither account configuration nor provider authentication.
Cache clear removes all accounts' verdicts, saved organization answers, and completion markers, but not pending items.

Organizer dates use the server clock and UTC midnight.
`Nd`, `Nw`, and `Nm` require `N >= 1`.
Explicit `YYYY-MM-DD` dates cannot be in the future.
`--until` is exclusive and must be later than `--since`.
Calendar dates use the client's local date and timezone.
For calendar queries, a relative `--to +Nd` starts from `--from`.
The calendar range must be at most 90 days.

| Verdict | Visible mail content |
| --- | --- |
| green | Cleaned headers, body, attachment names, and link IDs |
| yellow | Cleaned headers, attachment names, and reason, without body |
| red or sealed | Sender address, date, and reason, without display name or subject |
| error | Date and safe error code, without sender, subject, or body |

Every verdict also includes the message ID, the thread ID, verdict, authentication results, read state, flag, mailbox-listed categories, and folders.
The thread ID (`thread_id` in `mail list`, `mail search`, `mail get`, and `mail thread` results) is the provider's conversation ID: the Gmail `threadId` or the Microsoft 365 `conversationId`. Messages with the same `thread_id` belong to one conversation. It is an opaque provider ID, not message text. It is left out when the provider gives none, so an empty ID never groups unrelated messages.
`folders` lists the folder IDs (Microsoft 365 and fake) or label IDs (Gmail) that the message is in. They are provider IDs, not message text.
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

Operation names match CLI groups, such as `mail.get`, `mail.send`, `draft.update`, `calendar.invite`, `cache.clear`, and `approvals.status`.
`mail.thread` takes `{"id":"THREAD_ID"}`, and `calendar.search` takes `{"text":"TEXT"}`.
Meeting times on the HTTP interface are RFC 3339 timestamps. `calendar.update` also accepts a Go `duration` string.
`calendar.invite` takes an optional `optional` address list, `calendar.update` an optional `body`, and `calendar.respond` an optional boolean `no_notify`.
The `account` field is optional, and `params` must be an object.
Unknown operations or fields, duplicate keys, malformed JSON, trailing JSON, null fields, invalid types, and oversized requests are refused.
There is no HTTP login, token, key, raw-message, classification, rules-mutation, or approval endpoint. Only the [approval console](#approval-console), on its own address, approves or denies pending items.
A pending action returns HTTP 200 with `{"status":"pending","approval_id":"...","url":"..."}`.
`approvals.status` takes `{"id":"APPROVAL_ID"}` (32 lowercase hex characters) and ignores `account`. An unknown ID returns HTTP 404 with `{"error":"approval not found"}`.
A policy refusal returns HTTP 500 with `{"error":"refused by policy: KEY"}`.

Requests are limited to 1 MiB and 16 JSON nesting levels.
The client bounds responses at 64 MiB and never follows redirects or retries requests, including stale-connection write replay.
Mail list and search counts range from 1 to 100, skip is at most 1,000,000, and manual mutations accept at most 50 IDs.
Recipient and category lists accept at most 100 entries, and IDs and search strings are limited to 4,096 bytes.
Stats accepts 1 to 365,000 days.

The server admits at most 64 concurrent calls.
The server sets no overall deadline that would cut off a long `mail organize` scan; set the client `--timeout` instead.
SIGINT or SIGTERM stops network admission, allows five seconds to drain, then cancels work with a ten-second shutdown limit.
The server logs no request arguments, bodies, credentials, or panic payloads.
There is no scheduler and no automatic mailbox work.

## Verification and limits

Tests run offline: real loopback HTTP, temporary homes, the fake provider, fake Graph, Gmail, and Jev endpoints, and a fake Keychain `Platform`.
No test needs real mailbox data, a browser, OpenRouter, or Keychain access. CI (`.github/workflows/ci.yml`) runs `make` on Linux and macOS.

The end-to-end tests in `internal/e2e` build the real binary and run `serve` as its own process, on a temporary home with a fake account, the fake screening engine, and the file credential backend. The CLI runs as a separate process, and the tests approve and deny items through the approval console over HTTP.
After each test, the server log, `audit.jsonl`, and every table of `cache.db` except `pending` must contain no seeded name, subject, body, or address.

Not verified against real providers or services:

- mail sends, replies, and forwards;
- meeting invites, updates, cancellations, and responses;
- provider acceptance of generated drafts;
- `mail thread` on a Microsoft 365 conversation with a message in Junk Email (checked live on 2026-10-26: Gmail threads with drafts, and Microsoft 365 conversations across Inbox, Sent Items, Drafts, Deleted Items, and other folders; `calendar search` on both providers was checked the same day);
- Microsoft 365 device-code login, which organization policies can also refuse;
- requests to Jev cloud, or to any screening endpoint or model other than the default;
- migration of a real Keychain record.

Offline verification does not establish production readiness.

## License

Copyright 2026 Ivan Pereira.
Licensed under the [Apache License, Version 2.0](LICENSE) (`Apache-2.0`).
Dependencies retain their own licenses and attribution requirements.
Binary distributions must include the applicable third-party license notices.
