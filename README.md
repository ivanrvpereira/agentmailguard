# agentmailguard

Guarded Microsoft 365 and Gmail access for AI agents, with a long-lived daemon and a thin client.
The project has two programs.
`amg` is the client of the agent. It has only the API commands and talks to the daemon over HTTP.
`amgd` runs the daemon (`amgd serve`) on the daemon host. It also has all management commands: `reload`, `account`, `config`, `store`, and `cache clear`.
The API never manages the daemon.
The guard limits what an agent sees in mail and calendar events.
It does not protect a human from fraud.

**Exposure warning:** the API listener has no authentication and no built-in TLS.
Anyone who can reach the listener can read guarded mailbox data and change mail.
They can also do every action that the [write policy](#write-policy) allows, for example create drafts, send mail, and send meeting invitations, updates, cancellations, and responses.
The default listener is `127.0.0.1:8787`.
A non-loopback listener requires an explicit `amgd serve --listen` option and prints a warning.
Browser requests with an `Origin` header are refused, and the daemon does not enable CORS.
These restrictions are not a substitute for a protected network or encrypted tunnel.
Provider OAuth is required.

## Privacy and external screening

**Privacy warning:** Screening is an external service, not a local-only check.
By default, the daemon sends requests to OpenRouter's System One endpoint for model `typesafe/jev-1.13`.
The [`[screening]`](#screening) table can set another endpoint and model, which then receives the same data.
The fake screening engine sends nothing, but it is only for tests and local work.

- Mail requests include rendered body text, subject, sender and recipient identities (To, Cc, and the Bcc of drafts and sent mail where the provider gives it), attachment names, authentication results, and link domains.
- Calendar requests include subject, organizer, attendees, location, recurrence, meeting join URLs, and other event metadata.
- Private-event bodies are excluded, but private-event metadata and accepted meeting join URLs are still included.
- Organization requests include the rule instructions used for classification.
- [Action checks](#jev-action-check) include the recipients (To, Cc, and Bcc), subject, body (HTML as its source), HTML flag, reply-all flag, comment, and event fields that the agent wrote, for a reply the To and Cc addresses that the reply goes to, worked out from the original message, for a forward the subject and body of the forwarded message, and for a draft send the draft's To, Cc, Bcc, subject, body (HTML as its source), and HTML flag as the daemon reads them from the provider.

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
3. Run `./bin/amgd --help` and `./bin/amg --help`.

`make` checks formatting, runs `go vet` and race tests, builds the packages, and creates `bin/amg` and `bin/amgd`.
`amgd` needs CGO for SQLite and the Keychain. The Keychain backend works only on macOS. Windows is not supported.
`amg` is pure Go and builds with `CGO_ENABLED=0`, so you can build it for the machine where the agent runs and copy it there.
There is no plaintext credential fallback.
The build does not install anything into your PATH. The examples in this document use `amg` and `amgd` as if they are on your PATH.

## Daemon configuration

The supported deployment is one daemon per machine, with multiple accounts in one daemon home.
Use one home per machine. All homes share one Keychain item, so separate homes do not isolate credentials.
`--home` and `AMG_HOME` exist for tests and for a home that you moved.

The daemon owns the configuration, OAuth credentials, the screening key, `cache.db`, and `audit.jsonl`.
`amg` never reads these files. It sends every command to the daemon over HTTP.
The daemon home is `--home`, then `AMG_HOME`, then `~/.agentmailguard`.
Every `amgd` command runs on the daemon host and accepts `--home`.
To move a home from the earlier single `agentmailguard` program, see [Moving an existing home](docs/setup.md#moving-an-existing-home).

### Accounts

An account has an alias, for example `work`, and an email.
The lowercase email is the account key: the name under which the daemon keeps the data of the account.
The cache, pending actions, stats, audit records, `rules.toml`, and the credential store use the account key.
The alias is only a short name for the command line. It appears only in `config.toml`, in commands, and in their output.
Every sign-in must return the email that `config.toml` has for the account. If it returns another email, `amgd` saves nothing.
Microsoft 365 sign-in uses the `userPrincipalName` of the user, else `mail`. Gmail sign-in uses the `emailAddress` of the profile.

Prefer a restricted provider application that grants only the required scopes.
The [setup guide](docs/setup.md) lists the exact scopes that login requests.
Extra grants are the user's choice: a usable token with extra scopes or Microsoft 365 application roles is accepted with a warning. The daemon never uses the extra grants.

Add an account with `amgd account add` on the daemon host.
The command signs in first. Then it writes the account table with the email of the sign-in, so you never type the email.
These examples contain placeholders, not working credentials:

```sh
amgd account add work --provider m365 --client-id '<restricted-application-client-id>' --tenant-id '<tenant-id>'
amgd account add personal --provider gmail --client-id '<desktop-application-client-id>' --client-secret '<desktop-application-client-secret>'
```

The commands write a `config.toml` like this one:

```toml
version = 1
default_account = "work"

[accounts.personal]
provider = "gmail"
email = "me@gmail.com"
client_id = "<desktop-application-client-id>"
client_secret = "<desktop-application-client-secret>"

[accounts.work]
provider = "m365"
email = "me@example.com"
client_id = "<restricted-application-client-id>"
tenant_id = "<tenant-id>"
```

- `version = 1` is required.
- `default_account` is the alias that `amg` uses without `--account`. The first `account add` sets it. If the file has accounts, `amgd serve` and `amgd config check` refuse a missing or unknown `default_account`.
- Each email can belong to one account only. `account add` refuses an email that another account has, for example `me@example.com is already account work`.
- `account add` creates `config.toml` if it does not exist. If `rules.toml` already has rules for the email, `account add` tells you that they apply to the new account.
- `amg --account ALIAS` selects another account. `amg account list` shows the aliases, providers, and emails.
- The Gmail client secret of a desktop application is not confidential, so it stays in `config.toml`. The refresh token in the credential store is the real credential. Keep `config.toml` private.
- The optional `[policy]` tables in `config.toml` set the [write policy](#write-policy).

Other account commands:

- `amgd account login ALIAS` signs in again, for example after a login expired. It accepts only the email in `config.toml`. If you sign in as another user, it saves nothing and names `amgd account add`.
- `amgd account remove ALIAS` removes the account and policy tables, the login, and the cached verdicts of the account. It denies the pending actions of the account. Audit records and the rules of the account in `rules.toml` stay.
- `account remove` asks you to type the alias on a terminal. If stdin is not a terminal, add `--yes`.
- `account remove` refuses the default account while other accounts exist. First run `amgd config set default_account OTHER_ALIAS`.
- `account remove` does not call the provider. It prints where you can revoke the access of the application at the provider.
- After `account remove`, make sure that the daemon log shows `reload ok`. If it shows `reload refused`, the daemon still serves the removed account with its old state, and the console can still approve its pending actions. Fix the cause and run `amgd reload`, or stop the daemon.

To rename an alias, edit the table name and `default_account` in `config.toml`, and then run `amgd reload`.
No data is lost, because the data uses the email.

### Settings

`amgd config` reads and changes `config.toml` and the two secrets on the daemon host:

| Command | Effect |
| --- | --- |
| `amgd config path` | Prints the path of `config.toml`. |
| `amgd config list` | Shows every setting with its value. `(default)` marks a value that the file does not set. Secrets show only `set` or `not set`. |
| `amgd config get KEY` | Shows the value of one setting. |
| `amgd config set KEY VALUE` | Changes one setting and reloads the daemon. |
| `amgd config set screening.api_key` | Reads the screening key from stdin and saves it in the credential store. |
| `amgd config set console.password` | Reads the [console password](#console-password) from stdin and saves its hash in the credential store. |
| `amgd config unset KEY` | Removes a setting so that its default applies again, and reloads the daemon. |
| `amgd config check` | Validates `config.toml` and `rules.toml` with the code of the daemon. |

A key is a dotted path:

- `default_account`.
- `policy.KEY`, `policy.sealed.mail_trash`, `accounts.ALIAS.policy.KEY`, and `accounts.ALIAS.policy.sealed.mail_trash`, for example `policy.mail_send`. See [Write policy](#write-policy).
- `accounts.ALIAS.client_id`, `accounts.ALIAS.tenant_id` (Microsoft 365), and `accounts.ALIAS.client_secret` (Gmail).
- `screening.provider`, `screening.endpoint`, and `screening.model`. See [Screening](#screening).
- `console.listen`, `console.hosts`, and `console.username`. `console.hosts` takes a comma-separated list. See [Console configuration](#console-configuration).
- `credentials.backend`. See [Credential storage](#credential-storage).

`config set` refuses an unknown key or an invalid value, and the file stays unchanged.
It refuses `accounts.ALIAS.email` and `accounts.ALIAS.provider`. Only `amgd account add` and `amgd account remove` change them.
A secret is never an argument. `config list` and `config get` read the credential store to show whether a secret is set, and show `unknown` if the store stays locked.

`amgd` writes `config.toml` in one atomic step, after it validated the result with the code of the daemon.
The file then starts with `# Written by amgd. Comments are not kept.`
`amgd` never writes `rules.toml`.

### Reload

The daemon reads `config.toml`, `rules.toml`, and the credential store only at start and at a reload.
`amgd config set`, `config unset`, `account add`, `account login`, and `account remove` reload the daemon after the change.
They print `reload sent to daemon (PID n); see the daemon log`.
If no daemon runs, they print `no daemon runs for this home; the change applies when amgd serve starts`.
After a hand edit of `config.toml` or `rules.toml`, run `amgd reload` on the daemon host.

`amgd reload` sends SIGUSR1 to the daemon. The daemon log then shows `reload requested`, the check list of a start, and `reload ok`.
If the new state is invalid, the log shows `reload refused: CODE; old settings kept`. For `config_invalid` and `rules_invalid`, an `error:` line before it names the key; for `credentials_failed`, the check list shows the failed item.
The code is `config_invalid`, `rules_invalid`, `credentials_failed`, or `canceled`.
For example:

```text
reload requested
error: config.toml: policy.mail_send: must be allow, approve, or deny
reload refused: config_invalid; old settings kept
```

- A reload keeps the API port and the console port open, so the agent sees no gap.
- A reload keeps the file backend password that you entered at start.
- A request that runs during a reload, such as a send, finishes on the old state with no time limit.
- One reload runs at a time. Signals during a reload cause exactly one more reload after it.
- Each reload writes a `reload` record to `audit.jsonl` with `ok` or the refusal code.

Two keys take effect only at a restart: `console.listen` and `credentials.backend`.
`config set` for these keys writes the file without a reload, and prints `restart needed: KEY changed`.
A later reload applies all other changes and keeps the old value of these keys. The daemon log then shows `restart needed: KEY changed` again.

`amgd serve` holds the lock file `server.lock` in the home, with its PID.
A second `amgd serve` for the same home fails with `another daemon runs for this home (PID n)`.
`amgd` sends the reload signal only while the lock is held. Without a daemon, `amgd reload` fails with `no daemon runs for this home`.
SIGINT, SIGTERM, and SIGHUP stop the daemon. SIGHUP keeps its usual meaning: the terminal closed.
For systemd and launchd, see [Run as a service](docs/setup.md#run-as-a-service).

## Fake provider

A fake account has a local mailbox and calendar in a SQLite file. Use it to run real workflows through the real `amg` and daemon without a provider or a login. With the default file backend, set `AMG_CREDENTIALS_PASSWORD` or enter the password when `amgd` asks for it.

```sh
amgd account add demo --provider fake
```

The command needs no sign-in. It writes this table:

```toml
[accounts.demo]
provider = "fake"
email = "demo@fake.invalid"
```

A fake account needs no `client_id`, `tenant_id`, or `client_secret`. Its email is `ALIAS@fake.invalid`. `amgd account login` refuses it with `fake accounts need no login`. The `[policy]` tables and `rules.toml` work as for other accounts. At start, `amgd serve` shows `account "demo" (fake): no login needed`, and `amg account status` shows the email with no scopes.

All fake accounts share `fake-provider.db` in the daemon home, for example `~/.agentmailguard/fake-provider.db`. Each row has an `account` column with the account key: the email of the account in lowercase. The daemon creates the file with mode 0600 and applies the schema in [`internal/providers/fake/schema.sql`](internal/providers/fake/schema.sql) when it first opens the file. The schema file explains each table and column. Each account gets the folders `inbox`, `drafts`, `sent`, `archive`, and `trash`. The address of the account is its email.

The daemon reads and writes `fake-provider.db` on every call, so you can seed and inspect it with `sqlite3` while the daemon runs:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db < internal/providers/fake/schema.sql
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, to_addresses, subject, body, received_at, spf, dkim, dmarc) VALUES ('demo@fake.invalid', 'm1', 'Ana', 'ana@example.test', '[\"demo@fake.invalid\"]', 'Lunch on Friday?', 'Are you free at noon?', '2026-04-01T09:30:00Z', 'pass', 'pass', 'pass');"
sqlite3 ~/.agentmailguard/fake-provider.db "SELECT id, folder, subject, origin FROM messages WHERE account = 'demo@fake.invalid';"
```

Trash and archive are moves to the `trash` and `archive` folders. Only `draft delete` deletes a row, the draft's. Drafts go to `drafts`; `draft send` moves the draft to `sent` with `is_draft` 0 and the send time, and it keeps its ID. `mail send`, `mail reply`, and `mail forward` store the message in `sent`, with `origin` (`new`, `reply`, or `forward`) and `source_id`. Nothing leaves the machine. New drafts, replies, and sends keep their To, Cc, and Bcc lists in `to_addresses`, `cc_addresses`, and `bcc_addresses`, and `content_type` is `html` for an `--html` body. A reply picks its recipients by the Gmail rule (see [Mail send](#mail-send)); the fake has no Reply-To, and a message in `sent` is one that the account sent. A plain-text reply body is the agent's text; an `--html` reply body is the agent's HTML with the original body below it in a `<blockquote>`. A forward goes to its `--to`, `--cc`, and `--bcc`, has the subject `Fw: ` and the original subject, and carries the original's `attachments`; its body follows the same rule as a reply, with the note as the agent's text. A draft update changes only the given columns of any row with `is_draft` 1, whatever its `origin`, and keeps its ID; a message that is not a draft is refused. A `fake-provider.db` made before the `bcc_addresses` column existed gets the column when the daemon first opens it.

To seed a conversation, give its messages the same `thread_id`. A message whose `thread_id` is NULL is its own conversation, and its `thread_id` in results is its message ID. Reply and forward drafts, and sent replies and forwards, join the conversation of their source message; new drafts and new sends are their own conversation. A `fake-provider.db` made before the `thread_id` column existed gets the column when the daemon first opens it. For example, a reply from Ana in the conversation of `m1`:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, subject, body, thread_id) VALUES ('demo@fake.invalid', 'm2', 'Ana', 'ana@example.test', 'Re: Lunch on Friday?', 'Noon works.', 'm1');"
```

The calendar uses three tables. `events` holds the events, `attendees` holds the attendees of each event and their responses, and `free_busy` holds the free/busy times that `calendar availability` reads. The account organizes an event when its `organizer_address` is empty or is the account address; only then can it update or cancel the event. It can respond only to the other events. Availability comes only from `free_busy`, never from `events`. An address without `free_busy` rows is `unknown`, and a slot that no row covers is `free`. For example, an event that Ana organizes with the demo account as attendee, and Ana's busy time:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO events (account, id, subject, start_at, end_at, organizer_name, organizer_address) VALUES ('demo@fake.invalid', 'e1', 'Planning', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'Ana', 'ana@example.test'); INSERT INTO attendees (account, event_id, address, name) VALUES ('demo@fake.invalid', 'e1', 'demo@fake.invalid', 'Demo'); INSERT INTO free_busy (account, address, start_at, end_at, status) VALUES ('demo@fake.invalid', 'ana@example.test', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'busy');"
```

`calendar invite`, `update`, `cancel`, and `respond` change only `fake-provider.db`, under the write policy as for other accounts. No notification leaves the machine. A cancel sets `is_cancelled` and `cancel_comment` and deletes nothing. A response sets the account's own row in `attendees`; `--no-notify` changes nothing else. An invite stores `--optional` attendees with `optional` set to 1 in `attendees`. An event `body` is HTML (`content_type` `html`): an `--html` body as given, plain text escaped. A `fake-provider.db` made before the `optional` column existed gets the column, with 0 for every existing attendee, when the daemon first opens it.

Fake mail is untrusted like real mail. The configured [screening](#screening) engine screens it. With the fake screening engine, the daemon needs no screening key.

## Screening

The daemon screens every message and checks the text of agent actions with a decision engine. The optional `[screening]` table selects the engine. `amgd config set screening.provider fake` and the other `screening.*` keys change it without a hand edit:

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
- At start, `amgd serve` shows `screening key: not needed (fake screening)`. If a configured account is not a fake account, `amgd serve` also prints a warning: the mail of that account is then judged by keywords, not by a decision model.
- An absent table selects `jev` with the default endpoint and model. An unknown key or provider, an invalid endpoint, or an invalid model stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, for example `config.toml: screening.endpoint: must be an https URL`. A reload with such a change is refused. The error never shows the value.

## Credential storage

Both backends store all provider OAuth credentials, the screening key, and the [console password](#console-password) hash in one versioned record.
Only `amgd` reads the store. `amg` has no store code.
The optional configuration table selects the backend:

```toml
[credentials]
backend = "file" # Default. Use "keychain" for the macOS Keychain.
```

An absent table selects `file`.
Linux supports only `file` because the native Keychain backend is macOS-only.
On macOS, run `amgd config set credentials.backend keychain` on a new home before the first `amgd account add` to use the Keychain.
Earlier versions selected `keychain` when the table was absent. A home that keeps its record in the Keychain must now set `backend = "keychain"`, or copy the record to the file backend as described in [Copy credentials to the other backend](#copy-credentials-to-the-other-backend).
Any other key or value is a configuration error.
Neither backend detects rollback: another process under the same user can restore an older valid credential record.

### Keychain backend

The Keychain backend uses one non-synchronizable generic-password item: service `agentmailguard`, account `credentials`.
All configured accounts share this item under the current operating-system user, with separate entries keyed by provider and email.
All homes of the user also share this item.
When `amgd` asks for access, choose "Always Allow" to avoid repeated access prompts for the same build.
A rebuilt binary needs approval again.

### File backend and password input

The file backend stores the record in `<home>/credentials.age` with mode `0600`.
It uses age password encryption, and writes are atomic.
A wrong password or damaged file refuses access with the same fixed error.
Unknown record versions also refuse access.

The password source is `AMG_CREDENTIALS_PASSWORD`, then stdin if the variable is absent.
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
A password that starts with `op://` is refused: it is an unresolved 1Password secret reference, not a password.

With 1Password and mise, put the secret reference in a git-ignored `.env` that `mise.toml` loads (`_.file = ".env"`), and run `amgd` through `op run`:

```sh
# .env
AMG_CREDENTIALS_PASSWORD=op://development/AMG_CREDENTIALS_PASSWORD/password
```

```sh
op run -- amgd serve
```

Without `op run`, `amgd` gets the reference text and refuses it.

Run `amgd config set screening.api_key` on the daemon host to save the screening key.
Terminal input disables echo. Non-terminal stdin supplies the key as one line.
With the file backend and no password environment variable, piped input must contain two lines: the password first, then the key.
With the password environment variable, piped input contains only the key line.
The command refuses keys with spaces or control characters. It never prints the key or accepts it as an argument.
It writes a `screening_api_key_set` audit record without the key, and reloads the daemon.

The daemon reads the screening key only from the credential store. It ignores `OPENROUTER_API_KEY`, so a key set for other tools is never sent to a configured endpoint.
No `amg` command transfers a provider token or screening key to the daemon.

### Copy credentials to the other backend

1. Stop the daemon.
2. Make sure `[credentials] backend` names the source backend. To copy from the Keychain on a home without the table, run `amgd config set credentials.backend keychain` first, because an absent table selects `file`.
3. Run one command on the daemon host:

```sh
amgd store migrate --to file
# Or, from a configured file backend on macOS:
amgd store migrate --to keychain
```

4. Start the daemon with the selected backend.

Migration copies the whole record from the configured backend to the other backend under the same-home lock.
Then it sets `[credentials] backend` in `config.toml` to the new backend and prints `restart needed: credentials.backend changed`.
It leaves the source record unchanged.
It refuses while a daemon runs for the home: `a daemon runs for this home; stop it first`.
It refuses an existing target record. It also refuses a target that matches the configured backend.
It writes the audit records `store_migrated` and `config_changed`.
The file password comes from the normal sources. Terminal creation of the file target asks twice.

### Change the file password

1. Stop the daemon.
2. Run `amgd store change-password` on a terminal on the daemon host.
3. Supply the current password through its normal source.
4. Enter the new password twice at the terminal prompts.
5. Start the daemon with the new password.

This command works only with the file backend and re-encrypts the latest record under the same-home lock.
It refuses while a daemon runs for the home.
The new password cannot come from an environment variable or a pipe.
It writes a `store_password_changed` audit record.

## Sign-in and daemon start

Run sign-in on the daemon host. `amgd account add` signs in to a new account. `amgd account login` signs in again to an existing account:

```sh
amgd account add work --provider m365 --client-id '<client-id>' --tenant-id '<tenant-id>' --device-code
amgd account login work
amgd account login work --device-code
amgd account login personal --no-browser
amgd serve
```

`amgd serve` loads every credential before it accepts requests, because an operator is present at the start:

1. It reads the credential store and writes it back once, so any Keychain prompt appears at the start.
2. Its first `credential check:` item is `credentials (keychain)` or `credentials (file)`. A store failure refuses the start.
3. It checks every configured account and the console password. It also checks the stored screening key, unless `[screening]` selects the fake engine. Check lines contain fixed statuses, not secrets.
4. If the screening key is missing and stdin is a terminal, it prompts with echo disabled and saves the key. Piped password input gets no key prompt.
5. If every check passes, it asks the provider about each saved login, with a 20-second limit per account. The account line then shows the answer of the provider.

Each account has one line. Its status names the step that failed:

- `ok`: a login is saved, and the provider accepted it.
- `no login needed`: a fake account, which reads no credentials.
- `not logged in; ...`: no login is saved for this account.
- `sign-in failed; ...`: a login is saved, but the provider refused it or did not answer. The cause can be an expired or revoked login, wrong client settings in `config.toml`, or a network problem. The provider codes cannot tell these apart.

An account that is not logged in, or whose sign-in failed, does not block the start.
Its commands fail until the cause is fixed.
A login can also expire or be revoked while the daemon runs. Commands for that account then fail with `sign-in failed; check the network, or run amgd account login`.
A failed credential store, a missing screening key, or an invalid configuration refuses the start.
Store statuses include `ok`, `access denied`, `wrong password or damaged file`, `password missing`, and `unsupported version`.
A missing console password does not block the start. Its line is `console password: not set; the console refuses every request until amgd config set console.password`.

After the start, the daemon keeps credentials in memory. A reload reads the store again.
A token refresh saves to the store only if the stored login did not change since the daemon read it. Otherwise the daemon uses the stored login. Thus a login that `amgd account login` saved while the daemon runs is never lost.
A token refresh saves without losing the entries of the other accounts or the screening key.

Browser login is the default.
Microsoft 365 supports device-code login.
Gmail refuses device-code login because that flow does not support the required scopes.

On a headless host, use Gmail login with `--no-browser`. It needs no SSH tunnel; the steps are in [Headless login](docs/setup.md#headless-login).
The pasted address gets the same checks as the loopback callback: host, port, path, and `state` must match this sign-in.
If the address does not match, login asks for it again. A provider error in a matching address ends the login.
The PKCE verifier stays on the daemon host, so the code in the address cannot be exchanged anywhere else.
The program does not print or log the pasted address. The terminal shows it as you paste it.
Without `--no-browser`, Gmail login opens the URL with `open` on macOS or `xdg-open` on Linux. If that fails, login continues and accepts the loopback callback or a pasted address.
`--no-browser` is refused for Microsoft 365. Use `--device-code` on a headless host.

`amgd` saves a login only after the email check.
A successful `account login` prints a final `✓ Signed in as EMAIL` line on stderr. A successful `account add` prints `✓ Added account ALIAS: EMAIL (PROVIDER)`.
Stdout gets the JSON result with `account`, `provider`, `email`, and `scopes`.
Each command writes an audit record with the account key: `account_added`, `account_login`, or `account_removed`.
`amg` commands never open a login prompt.
Login and `amg account status` print a warning on stderr for extra grants. `account status` lists them in `extra_scopes`; `scopes` lists only requested grants. Application roles appear with a `role:` prefix.
Tokens that cannot work are refused: malformed tokens, and Gmail tokens without the mail and calendar scopes. Gmail refusals list `missing_scopes`.
`amg` does not start a daemon.

Microsoft 365 `amg account status` can use a cached token and username without a Graph request.
A successful status does not prove current Graph access, all required permissions, or provider acceptance of mailbox operations.
Login errors are fixed texts that name the failed step. They contain no raw provider errors, token values, or callback URLs.


## Local and remote clients

```sh
amg account list
amg account status
amg --account work mail list --count 5
amg mail get MESSAGE_ID --pretty
amg --timeout 2h mail organize --since 1m
```

The daemon URL comes from `-H` or `--host`, then `AMG_HOST`, then `http://127.0.0.1:8787`.
`amg` supports HTTP or HTTPS URLs without userinfo, query parameters, or fragments.
HTTPS requires an external TLS endpoint because `amgd serve` does not provide TLS.
The overall client deadline defaults to 30 minutes and accepts a positive `--timeout` duration.
Long `mail organize` scans need a longer `--timeout`.
`amg` has no `--home` and reads no file of the daemon home.

For protected remote access, keep the daemon bound to loopback and use an SSH tunnel:

```sh
ssh -N -L 18787:127.0.0.1:8787 <daemon-host>
amg -H http://127.0.0.1:18787 --account work mail list
```

An explicit remote listener is available with `amgd serve --listen ADDRESS:PORT`.
The tunnel protects the connection, not against other users who can already access either local listener.
Forward only the API port (8787). Do not forward the [approval console](#approval-console) port through a tunnel that the agent can use.

## Commands and output

Run `amg --help` or `amgd --help` without a daemon to see every command and option.
The table lists the `amg` commands. The `amgd` commands are in [Daemon configuration](#daemon-configuration) and [Credential storage](#credential-storage).
Recipient and category flags can repeat.
An explicit empty value, such as `draft update ID --body ''`, is not the same as an omitted flag. For the lists of `draft update`, one empty value, such as `--cc ''`, clears the list.

| Group | Commands |
| --- | --- |
| State and accounts | `stats --days 30`, `rules list`, `approvals status ID`, `account list`, `account status` |
| Mail reads | `mail list [--folder ID] [--count N] [--page NEXT_PAGE]`, `mail get ID [--pretty]`, `mail thread THREAD_ID`, `mail search [--count N] [--page NEXT_PAGE]`, `mail link ID L1 L2` or `mail link ID --all`, `mail folders`, `mail categories` |
| Mail changes | `mail archive IDS...`, `mail trash IDS...`, `mail move FOLDER_ID IDS...`, `mail read IDS...`, `mail unread IDS...`, `mail flag IDS... --state flagged|complete|none`, `mail categorize IDS... --add NAME --remove NAME`; each of these except `mail flag` also takes `--thread THREAD_ID [--in FOLDER_ID]` instead of `IDS...` |
| Organization and calendar | `mail organize --since 7d [--until DATE] [--apply]`, `calendar list --from today --to +7d`, `calendar get ID`, `calendar search TEXT [--page NEXT_PAGE]` |
| Availability (Microsoft 365 and fake, read-only) | `calendar availability --to ADDRESS [--to ADDRESS] --start 'YYYY-MM-DD HH:MM' --end 'YYYY-MM-DD HH:MM'` |
| Meetings (send notifications) | `calendar invite --to ADDRESS --subject TEXT --start 'YYYY-MM-DD HH:MM' --duration 30m\|--end 'YYYY-MM-DD HH:MM' [--optional ADDRESS] [--body TEXT] [--html] [--location TEXT] [--no-online]`, `calendar update ID [--subject TEXT] [--location TEXT] [--body TEXT [--html]] [--start TIME] [--duration D\|--end TIME] [--add-attendee ADDRESS] [--remove-attendee ADDRESS] [--online]`, `calendar cancel ID [--comment TEXT]`, `calendar respond ID --response accept\|tentative\|decline [--comment TEXT\|--no-notify]` |
| Mail send (sends at once) | `mail send [--to ADDRESS] [--cc ADDRESS] [--bcc ADDRESS] [--subject TEXT] [--body TEXT] [--html]`, `mail reply ID [--all] [--cc ADDRESS] [--bcc ADDRESS] [--body TEXT] [--html]`, `mail forward ID [--to ADDRESS] [--cc ADDRESS] [--bcc ADDRESS] [--body TEXT] [--html]`, `draft send ID` |
| Drafts (no send) | `draft new [--to ADDRESS] [--cc ADDRESS] [--bcc ADDRESS] [--subject TEXT] [--body TEXT] [--html]`, `draft reply ID [--all] [--cc ADDRESS] [--bcc ADDRESS] [--body TEXT] [--html]`, `draft forward ID [--to ADDRESS] [--cc ADDRESS] [--bcc ADDRESS] [--body TEXT] [--html]`, `draft update ID [--to ADDRESS] [--cc ADDRESS] [--bcc ADDRESS] [--subject TEXT] [--body TEXT [--html]]`, `draft delete ID` |

Successful commands print the operation's JSON result.
`mail list`, `mail search`, and `calendar search` return one page: `{"messages": [...], "next_page": "..."}`, with `"events"` for `calendar search`. `next_page` is present only when the provider has more results. To read the next page, always repeat the same command with the same options and add `--page VALUE`. The daemon checks the form of the value and otherwise passes it to the provider as is; only for Microsoft 365 mail does it also refuse a next link from other options.
Without `--count`, the provider's default page size applies, so the same command returns a different number of messages on each provider: Gmail returns 100 (`messages.list` `maxResults`, at most 500), and Microsoft 365 returns 10 (Graph `$top`, at most 1,000). The fake provider pages like Microsoft 365. A `--count` above the provider's maximum is sent as given; the provider refuses or caps it, and a refusal fails the command like other provider errors. `calendar search` has no `--count`: Google Calendar uses its default page size, and Microsoft Search returns at most 25 events a page.
`mail list` reads the Inbox unless `--folder` is given. This is our choice: without a folder, the providers' lists differ in meaning (Gmail: all mail except spam and trash; Microsoft 365: every folder, including Deleted Items and Junk Email).
`mail list --pretty` and `mail search --pretty` print the table and then, when there is a next page, one `next_page: VALUE` line.
`mail get --pretty` formats only the guarded view.
`mail search` needs at least one of `--text`, `--from`, `--to`, `--subject`, `--after`, or `--before`. `--to ADDRESS` matches the To and Cc fields: the Gmail `to:` and `cc:` operators, or the Microsoft 365 `to` and `cc` search properties.
Dates follow one rule: a day runs from 00:00 to the end of the day on the clock and in the time zone of the machine where the CLI runs, and the last day is included. Where a clock change skips 00:00, the day starts at its first instant. This holds for `mail search --after DAY --before DAY`, `mail organize --since --until`, and `calendar list --from --to`. The CLI turns the days into exact times, from the first day's local midnight up to, not including, the local midnight after the last day; the daemon and the providers use only these exact times. `calendar availability` and the meeting commands take exact local times (`YYYY-MM-DD HH:MM`), not days.
`calendar list` reads from today 00:00 to the end of the day seven days from today by default. `--to +Nd` counts from `--from`, and a `--to` day before the `--from` day is invalid usage.
`mail organize` uses `--since 7d` by default. `Nd`, `Nw`, and `Nm` count back from today and need `N >= 1`. `--since` cannot be later than the daemon clock, and an `--until` day before the `--since` day is invalid usage.
On Gmail, `mail search` sends the times in Unix seconds (`after:` and `before:`), because Gmail reads a date there as midnight Pacific time ([Gmail API filtering](https://developers.google.com/workspace/gmail/api/guides/filtering)); the guide does not say whether the boundary second matches, so the daemon asks for one more second on each side. On Microsoft 365, KQL `received` takes only a day, in UTC when no time zone is set ([MS-KQL Date Tokens](https://learn.microsoft.com/en-us/openspecs/sharepoint_protocols/ms-kql/21b23dbb-6c49-4d35-86e4-80fb1d66b301)), so the daemon asks for one more UTC day on each side. On both, the daemon then drops the messages received outside the exact times, and messages without a received time, so a page can hold fewer messages than `--count`, and `next_page` still continues the same search.
`mail thread` prints JSON only. It returns the messages of the conversation as a plain list of `mail list` messages, without pages, newest first, from every folder, including trash, spam or junk, sent mail, and drafts. Gmail returns the whole thread (`threads.get`); on Microsoft 365 the daemon reads every page of the `conversationId` query. An unknown thread fails. Read bodies with `mail get`.

`mail archive`, `trash`, `move`, `read`, `unread`, and `categorize` take `--thread THREAD_ID` instead of message IDs: a conversation action. It changes the conversation's messages in one folder only, as a mail app does from the folder that you look at: the folder that `--in FOLDER_ID` names, or the Inbox without `--in`. `--in` takes the folder IDs of `mail list --folder`. Messages of the conversation in other folders do not change. For example, a conversation has two messages in the Inbox and your reply in Sent: `mail archive --thread THREAD_ID` archives the two Inbox messages, and the reply stays in Sent.
- The command gives either IDs or `--thread`, never both, and `--in` only with `--thread`. `mail flag` has no `--thread`: a flag is set on single messages.
- The daemon reads the conversation right before it writes, under the same lock as other writes, so a message that arrived after you read the conversation is included when it is in the folder. A failed read changes nothing, and an unknown thread fails like an unknown message ID.
- Each chosen message then goes through the same action as an explicit ID: the same write policy decision for `mail trash`, one pending action for each message that needs approval, and the same refusals. The result has one entry per message, newest first, as for an ID list. A conversation without messages in the folder gives `[]` after the same refusals: for example, a move into the trash or an unknown category is still refused. There is no limit on the number of messages.
- `--in` can name the trash (Gmail `TRASH`, Microsoft 365 Deleted Items or a folder below it) like any other folder. A move from there takes the messages out of the trash, as Move to does in Gmail and Outlook: Gmail removes `TRASH` and adds the new label, and Microsoft 365 moves them to the new folder. An archive from the trash moves the messages to Archive on Microsoft 365. On Gmail it changes nothing, because the trash already removed `INBOX`.
- On Gmail, a conversation move removes the label of `--in` and adds the new one, as Gmail on the web does when you move a conversation from a label; from the Inbox, it removes `INBOX`, as a move by ID does. A Gmail archive removes only `INBOX`, as everywhere, so an archive with `--in` a label leaves the messages in that label. Gmail keeps the `SENT` label and user labels on a message in the trash or spam, so a conversation action skips messages in the trash or spam unless `--in` is that folder, as `messages.list` does by default. On Microsoft 365 a message is in one folder: the daemon looks up the ID of `--in` (`GET /me/mailFolders/{folder}`), so a well-known name such as `inbox` works, and compares it with each message's folder.
`calendar search TEXT` uses the provider's own search: Google Calendar `events.list` with `q` on Gmail, and the Microsoft Search API (`POST /search/query` for events) on Microsoft 365. It returns one page in the provider's default size and order, with `next_page` as above, and has no date range. Each event is screened as `calendar get` screens it, and the result has the fields of a `calendar list` result. The provider matches the text on its side, so a hidden subject or body can decide whether an event is in the result. The fake provider matches the subject, body, location, organizer, and attendees, ignoring case.
`draft new` takes every option as optional, as the mail apps do, and `--to`, `--cc`, and `--bcc` can each be repeated. `--bcc` adds hidden recipients: the other recipients do not see them. The body is plain text with its line breaks unless `--html` is given; with `--html`, the body is HTML that the daemon sends unchanged, without cleaning or checking it.
`draft new` returns `{"id", "thread_id", "to", "cc", "bcc", "subject"}`: the draft ID, its conversation ID (left out when the provider gives none), and its recipients and subject. On Microsoft 365, they come from the message that Graph returns; on Gmail, the IDs come from Gmail's answer and the recipients and subject from the message that the daemon built.
`draft reply` takes the same options as `mail reply` (see [Mail send](#mail-send)) and returns the same fields as `draft new`, or only `{"id", "thread_id"}` when the daemon cannot read the verdict of the replied-to message after the write. The daemon always refuses a reply to a `red`, `sealed`, or `error` message; see [Sealed messages](#sealed-messages).
`draft forward` takes the same options as `mail forward` (see [Mail send](#mail-send)), but needs no recipient, as the mail apps allow; add the recipients later in the app. It returns the same fields as `draft reply`, and the daemon refuses it for the same messages.
`mail get` also shows `bcc` on drafts and sent mail, where the provider gives it, at the verdicts that show `to` and `cc`. Sent mail is a Gmail message with the `SENT` label, or a Microsoft 365 message whose `from` is the account's address, so on Microsoft 365 mail sent from an alias, a shared mailbox, or on someone's behalf shows no Bcc; the daemon reads no Bcc of other mail, where a Bcc header is sender text. A message with Bcc is screened again on every `mail get`, as a draft is, so a cached verdict from before Bcc was screened never shows it; each such `mail get` costs one screening.
`draft update` changes any draft: a new draft, a reply or forward draft, or a draft that the user wrote in the app. Only the fields that it is given change, and it needs at least one of `--to`, `--cc`, `--bcc`, `--subject`, and `--body`. A list replaces the draft's list, and one empty value (`--to ''`) clears it. `--body` replaces the whole body, the quoted original of a reply or forward included, as both providers do; it is plain text unless `--html`, which goes only with `--body`. On Gmail, the update keeps the draft's conversation and its other headers byte for byte, `In-Reply-To` and `References` included, except `Date` and `Message-ID`. `--body` replaces the body text that `mail get` shows (the first text and HTML parts that are not attachments, with the `multipart/alternative` that holds only them) with one base64 text part, and keeps every other part, such as attachments, inline images, and attached messages, byte for byte at its place. A draft with only attachments gets the body as its first part; a draft that cannot take one, or whose MIME does not parse, fails with `Gmail error ValueError`. Without `--body`, the body stays as it is. An update of a message that is not a draft fails with `draft update refused: not a draft` before anything is written.
`draft update` returns `{"id", "thread_id", "ok": true}` and only the fields that it was given (`to`, `cc`, `bcc`, `subject`), cleaned; it does not read back the other fields. Gmail gives the updated draft a new message ID, so use the returned `id` for later commands; on Microsoft 365 and the fake provider, the ID stays. When the update withdrew a waiting send of the draft, the result also has `send_approval_id`, the approval ID of the new waiting send (see [Waiting sends and draft changes](#waiting-sends-and-draft-changes)).
`draft delete ID` deletes any draft: on Gmail with `drafts.delete` on the draft that holds the message, which deletes it for good and does not trash it; on Microsoft 365 with Graph `DELETE` of the message, which keeps the item restorable (Graph has a separate permanent delete that the daemon never uses). `draft send ID` sends any draft as it is (see [Mail send](#mail-send)). Both refuse a message that is not a draft, with `draft delete refused: not a draft` or `draft send refused: not a draft`, before anything is written.
`mail send`, `mail reply`, `mail forward`, `draft send`, and `draft delete` return `{"ok": true}`. The providers give no ID for a sent message.
`calendar invite` returns the new event ID. The other meeting commands return the event ID and `"ok": true`.
The [write policy](#write-policy) can refuse trash, draft, send, and meeting commands, or store them as pending actions. See [Results](#results) for these results.
There is no permanent-delete command for mail, and no folder creation, category creation, or arbitrary provider request command. Only `draft delete` deletes, and only a draft; on Gmail it deletes the draft for good.

Exit code `0` means the call ran, including a mutation with failed per-ID results and a `pending` result.
Exit code `1` means the whole call failed or was refused, or `account status` reports an unusable login.
Exit code `2` means invalid command usage.
Errors use fixed text or approved safe codes, never raw network errors or provider response bodies.

## Write policy

The write policy in `config.toml` sets, for each account, what the daemon does with each write action.
Each policy key has one of three decisions:

- `allow`: the daemon runs the action now.
- `approve`: the daemon stores the action as a pending action. The action does not run.
- `deny`: the daemon refuses the action.

**Caution:** Do not set `approve` unless the [console password](#console-password) is set.
Only a person in the [approval console](#approval-console) can approve or deny a pending action. Without the password, the console refuses every request and a pending action stays pending.
`amg` and the API cannot approve or deny. A draft change or delete through the API withdraws a waiting send of that draft and never runs or approves anything (see [Waiting sends and draft changes](#waiting-sends-and-draft-changes)). `approvals status ID` shows the state of a pending action. See [Results](#results).

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
| `mail_draft` | `draft new`, `draft reply`, `draft forward`, `draft update`, `draft delete` | `allow` |
| `mail_send` | `mail send`, `mail reply`, `mail forward`, `draft send` | `deny` |
| `calendar_invite` | `calendar invite` | `deny` |
| `calendar_update` | `calendar update` | `deny` |
| `calendar_cancel` | `calendar cancel` | `deny` |
| `calendar_respond` | `calendar respond` | `deny` |

For each key, the daemon uses the first value in this order: `[accounts.ALIAS.policy]`, `[policy]`, the default.
To change one key without a hand edit, run for example `amgd config set policy.mail_send approve` or `amgd config set accounts.work.policy.calendar_update allow` on the daemon host. `amgd config unset KEY` removes the value.
These commands have no key, and the policy always allows them: `mail read`, `mail unread`, `mail flag`, `mail categorize`, `mail archive`, and `mail move`.
In `mail organize --apply`, only a rule move to `trash` has a key.

An invalid policy stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, and a reload with an invalid policy is refused.
For example: `config.toml: accounts.personal.policy.sealed.calendar_invite: unknown key`. The error never shows the value.
`amgd config set` refuses an invalid policy key or value and leaves the file unchanged.
A policy is invalid if it has an unknown key or table, a value other than `allow`, `approve`, or `deny`, or a key other than `mail_trash` in a sealed table.

### Sealed messages

The daemon always refuses `draft reply`, `draft forward`, `mail reply`, and `mail forward` on a `red`, `sealed`, or `error` message, with the refusal of their key: `refused by policy: mail_draft` or `refused by policy: mail_send`. No setting allows them.
Unless the base decision is `deny`, the daemon looks up the verdict of the message for each reply and forward.

The sealed tables apply only to `mail trash` and accept only `mail_trash`.
The sealed decision is the first value in this order: `[accounts.ALIAS.policy.sealed]`, `[policy.sealed]`, `deny`.
If the message to trash is `sealed` or `error`, the daemon uses the stricter of the base decision and the sealed decision.
The order is `allow`, then `approve`, then `deny`.
The daemon looks up the verdict only when the sealed decision is stricter than the base decision.

If the message has no cached verdict, the daemon screens it first. A message that the daemon cannot screen counts as `sealed`.
For replies and forwards of `green` and `yellow` messages, and for `mail trash` of `green`, `yellow`, and `red` messages, only the base decision applies.
`draft new`, `draft update`, `draft delete`, `draft send`, and `mail send` do not reference a received message, so only the base decision applies. Unless the base decision is `deny`, `draft update`, `draft delete`, and `draft send` first read their message and refuse one that is not a draft, before the Jev action check and any pending action. An approved run reads the message again.

### Jev action check

Before an action runs, Jev checks the text that the agent wrote for it. The agent can be under prompt injection from mail it read, so the daemon does not trust this text.

- Checked actions: `draft new`, `draft reply`, `draft update`, `draft forward`, `mail send`, `mail reply`, `mail forward`, `draft send`, `calendar invite`, and `calendar update`. `calendar respond` and `calendar cancel` are checked only when they have a comment, so `calendar respond --no-notify` is never checked.
- Not checked: `mail trash`, `draft delete`, rule moves, and the commands without a policy key.
- Jev sees the operation name, a fixed description of what the action does, and the action fields that the agent gave: recipients (for `draft new`, `mail send`, and forwards, To, Cc, and Bcc; for `draft update`, the lists that it gives; for replies, the added Cc and Bcc; for `calendar invite`, also the `--optional` attendees), subject, body (for `calendar update`, the new `--body`; an `--html` body, on mail or meetings, as its HTML source, not rendered), the HTML flag, the reply-all flag, comment, response, and event fields. It never sees a message or event ID. A `draft update` gives only the fields that it changes.
- For `draft reply` and `mail reply`, Jev also sees `reply_recipients`: the To and Cc addresses that the reply goes to, worked out from the original message with the reply rule of [Mail send](#mail-send). On Microsoft 365 this list is for the check; Microsoft picks the final list when it makes the reply. If the daemon cannot read them, the check fails.
- For `draft forward` and `mail forward`, Jev also sees the subject and body of the forwarded message, read through the screened read path. If the daemon cannot read this text, for example because the message is not `green`, the check fails.
- For `draft send`, the agent gives only the draft ID, and Jev sees the draft's To, Cc, Bcc, subject, body (HTML as its source), and HTML flag, as the daemon reads them from the provider.

The check comes after the policy decision:

- `deny` refuses the action. Jev does not see it.
- If Jev flags the action, or the check fails, `allow` becomes `approve`, and the action waits in the [approval console](#approval-console). `approve` stays `approve`.
- A missing screening key or a Jev outage makes every check fail, so every checked action waits for approval.
- When a person approves a pending action, the daemon runs it without a second check.

Jev gives a score from 0 to 1. A score of 0.5 or more is a flag. This threshold is provisional and will change after tuning.
The agent never sees the score or the flag. It gets the normal result or the normal `pending` result.
The action page of the approval console shows the result, for example `Jev: flagged (score 0.62)`, `Jev: not flagged (score 0.08)`, or `Jev: check failed (TimeoutError)`.
If the [fake screening engine](#screening) made the check, the page shows `Fake check (keywords only):` instead of `Jev:`, for example `Fake check (keywords only): flagged (score 0.90)`. 

### Results

- `allow`: the command gives its normal result.
- `deny`: a draft, send, or meeting command fails with `refused by policy: KEY`, for example `refused by policy: calendar_invite`, with exit code `1`. The action does not run.
- `approve`: a draft, send, or meeting command returns `{"status":"pending","approval_id":"...","url":"http://localhost:8788/a/..."}` with exit code `0`. The action does not run. The `url` is the page of this action in the approval console. It uses `[console].listen` (see [Console configuration](#console-configuration)).

The daemon validates a meeting action before it stores it.
Thus an invalid time, a wrong role, or a recurring or cancelled event fails with the usual error, not `pending`.

`mail trash` decides each message ID on its own.
The daemon trashes the allowed IDs.
A denied ID gets `"ok": false` and `"error": "refused by policy: mail_trash"`.
An ID that needs approval gets `"ok": false`, `"error": "pending"`, and an `approval_id`, but no `url`. Each such ID is its own pending action.
A conversation trash (`mail trash --thread`) decides each chosen message the same way. An approved action trashes only the message of its approval, never a message that arrived later.

`approvals status ID` returns `{"state":"..."}` for a pending action. The state is `pending`, `running`, `done`, `denied`, `withdrawn`, or `failed`. A `withdrawn` action is a waiting `draft send` that a draft change or delete withdrew; it never runs.
A `done` action also has `result`: the normal result of the command, except that a `draft new`, `draft reply`, `draft forward`, or `draft update` result keeps only `id` and `thread_id`, and for `draft update` also `send_approval_id`, because the stored result holds no mail content.
The output never shows the stored request arguments.
An unknown ID fails with `approval not found` and exit code `1`.

A rule with `folder = "trash"` follows `mail_trash` for each message.
In the `mail organize --apply` report, the `move.result` of such a message is `pending` or `refused by policy: mail_trash`.
The report does not show the approval ID, and the stats count the message as `failed`.
The message stays incomplete, so each later `--apply` run makes a new pending action for it.

On Microsoft 365, `mail move` to Deleted Items or to a folder below it fails with `move refused: use mail.trash`.
Use `mail trash` instead.
A rule move to such a folder also fails, and the report shows `GuardError`.
On Microsoft 365, `mail folders`, `mail move`, `mail organize`, and `rules list` fail if the daemon cannot look up Deleted Items.

### Console configuration

The optional `[console]` table in `config.toml` sets the address of the [approval console](#approval-console) and the username of its sign-in.

```toml
[console]
listen = "localhost:8788"
hosts = ["my-mac.example.ts.net"]
username = "admin"
```

- `listen`: `HOST:PORT`. The default is `localhost:8788`. The console listens on this address, and the pending `url` uses it.
- `hosts`: extra host names that the console accepts, for example a Tailscale name. Each entry is a host name or IP address with an optional `:PORT`. An entry without a port matches a request without a port. Put an IPv6 address in brackets. The default is none.
- `username`: the username that the console sign-in needs, with the [console password](#console-password). The default is `admin`. It has 1 to 64 characters: ASCII letters, digits, `.`, `_`, `-`, and `@`. It is not a secret.

`amgd config set console.listen HOST:PORT`, `amgd config set console.hosts NAME1,NAME2`, and `amgd config set console.username NAME` change the table.
A new `hosts` list and a new `username` take effect at the reload. A new `listen` address takes effect only when you start the daemon again: the command prints `restart needed: console.listen changed`.
An invalid table stops `amgd serve` with `config.toml: KEY_PATH: PROBLEM`, for example `config.toml: console.listen: must be HOST:PORT`. The error never shows the value.

### Console password

The console password protects the [approval console](#approval-console). Until you set it, the console refuses every request.

Run `amgd config set console.password` on the daemon host to set or change it.
The command does not ask for the current password, so you can also reset a forgotten password with it.
On a terminal, echo is off and it asks for the new password twice.
Non-terminal stdin supplies the lines in the same order without a confirmation: the file backend password (if `AMG_CREDENTIALS_PASSWORD` is absent), and then the new password.
The password must be 8 to 72 bytes of valid UTF-8 without control characters.
The command stores only a bcrypt hash of the password in the credential store (Keychain or the encrypted file). It never prints the password or the hash, and it does not accept them as arguments.
It prints `Console password saved. Sign in to the console as USERNAME (console.username).`, writes a `console_password_set` audit record, and reloads the daemon. The new password takes effect at the reload, and the old password stops working at once. Make sure that the daemon log shows `reload ok`: after `reload refused`, the old password stays valid until a reload succeeds.

The daemon reads the hash at start and at each reload. It shows `credential check: console password: ok` or `not set; the console refuses every request until amgd config set console.password`. A missing password does not stop the start.

### Approval console

The approval console is a small web page where you approve or deny pending actions.
It always listens, from the start of the daemon. At start, the daemon shows `approval console listening`.
If the console address is taken, the daemon does not start and shows `console listener unavailable`.
Until you set the [console password](#console-password), the console answers every request with HTTP 503 and `console password not set; run amgd config set console.password`. You do not have to restart the daemon after you set the password.

The browser asks for a username and a password. Give the [console username](#console-configuration) (`admin` by default; `amgd config get console.username` shows it) and the [console password](#console-password).

- The list page `/` shows all pending actions, newest first, and then the 50 most recently changed other actions, `withdrawn` actions among them. Each row shows the account, the operation, the recipients (To, then `cc:` and `bcc (hidden):` lists), the subject, the state, and the age. For a draft send, the recipients and subject are those of the stored draft fields. A reply row starts its recipients with `reply` or, with `--all`, `reply all`, then the request's `cc:` and `bcc (hidden):` lists; the action page shows who the reply goes to.
- The action page `/a/ID` shows the account, the operation, the state, the times, and every stored request field as plain text, with `to`, `cc`, and `bcc` first. `bcc` is labeled `bcc (hidden recipients)`: these people get the message, and the other recipients do not see them. An `--html` body is labeled `body (HTML source, not rendered)` and shows as source text. Links in the text are never links on the page.
- For a reply, the action page also shows the recipients that the reply goes to under **Reply recipients**: the To and Cc worked out from the original message with the reply rule of [Mail send](#mail-send), plus the request's `cc`, and the request's `bcc` as `bcc (hidden recipients)`. The daemon reads the original when the page loads; if it cannot, the page shows `Reply recipients could not be read.` On Microsoft 365, Microsoft picks its own list when it makes the reply; it should match.
- For a draft send, the action page also shows under **Draft** the draft fields that Jev checked, stored when the send began to wait: `to`, `cc`, `bcc (hidden recipients)`, `subject`, and `body` (an HTML body as `body (HTML source, not rendered)`), as plain text. The send sends the draft as it is at the provider when it runs, so a later change that the user makes in the mail app is not on the page. A change or delete of the draft by the agent withdraws the action instead (see [Waiting sends and draft changes](#waiting-sends-and-draft-changes)). If the draft is gone or is no longer a draft when the run starts, the run fails with a safe code and nothing is sent.
- For an action on an existing message, the action page also shows the sender, subject, received date, and verdict of the message. For an action on an event, it shows the subject, start, end, and organizer. The daemon reads these through the screened read path and never shows a body. If the read fails, the page shows `not available`, and you can still decide.
- The action page also shows the result of the [Jev action check](#jev-action-check), if a check ran.
- A pending action has **Approve** and **Deny** buttons.

**Approve** runs the stored action once, with no second policy check. A `done` action shows its result. If the run fails, the action shows `failed` and a safe error code. A failed action never runs again.
**Deny** stops the action for good. Nothing runs.
A `withdrawn` action has no buttons and never runs.
An action that is already handled, also a `withdrawn` one, shows `already handled`, and nothing runs.

To use the console from a phone, put it behind an HTTPS tunnel, for example `tailscale serve`, and add the tunnel host name to `[console].hosts`.
Keep `listen` on loopback. If `listen` is not `localhost` or a loopback address, the daemon shows a warning at start.

Protections:

- Every page needs the username and the password (HTTP Basic authentication). The console has no TLS of its own.
- The console refuses a request whose `Host` is not `listen` or an entry of `hosts`, also before a password exists. This blocks DNS rebinding.
- Approve and Deny need an `Origin` of the console itself. This blocks form posts from other sites.
- After 5 failed sign-ins (a wrong username or password) in one minute, the console refuses every request with HTTP 429 until the oldest failure is more than one minute old.
- Each failed sign-in adds a `console_signin` record to the audit log.
- Pages have no scripts and no remote resources, and browsers must not cache or frame them.

### Storage and audit

Pending actions are in the `pending` table of `cache.db`. They do not expire.
A pending action stores the account key, the operation, and the request arguments: recipients, subject, body, comment, event fields, and the message or event ID.
A waiting draft send stores only the draft ID as its arguments, and beside them, in the `draft` column, the draft fields that Jev checked: To, Cc, Bcc, subject, body, and the HTML flag. A `cache.db` made before this column or the `withdrawn` state existed gets them when the daemon first opens it.
It also stores the result of the Jev check, if a check ran: the flag and the score, or the safe error code, and whether the fake screening engine made the check.
`amgd cache clear` does not delete pending actions. `amgd account remove` denies the pending actions of the removed account.

The audit log gets content-free records for the policy. Their `account` is the account key, and their `operation` is the API operation name, for example `mail.trash`.

- `"event": "policy"` records each decision, also `allow`. It has `time`, `account`, `operation`, `key`, `decision`, and `reasons`: `policy`; `sealed` if the referenced message is `sealed` or `error` and that made the decision stricter; `red` if the replied-to or forwarded message is `red` and that made the decision stricter; `jev_flag` if Jev flagged the action; `jev_error` if the Jev check failed. It can also have `id`, `rule`, and `approval_id`. It never has the Jev score.
- `"event": "approval"` records each step of a pending action. It has `time`, `account`, `operation`, `state` (`created`, `approved`, `denied`, `withdrawn`, `done`, or `failed`), and `approval_id`. A `withdrawn` record is written for each waiting draft send that a draft change or delete withdrew.
- `"event": "console_signin"` records each failed console sign-in: a wrong username or password. It has only `time` and `result` (`failed`). It never has the username, the password, the host name, or the address of the browser.

The operation record of a conversation action also has `thread`, the conversation ID, beside the IDs and results of the chosen messages. Its policy and approval records stay one per message. A conversation ID is an opaque provider ID, not message text.

The audit log also gets content-free records for changes on the daemon host:

- `account_added`, `account_login`, and `account_removed` have `time`, `event`, and `account` (the account key).
- `screening_api_key_set`, `console_password_set`, `store_migrated`, and `store_password_changed` have only `time` and `event`. They never have the secret.
- `config_changed` and `config_removed` have `time`, `event`, and `key`, the dotted key path. A key of an account also has `account`. `config_changed` has `value` only for a key with fixed choices, such as a policy decision, a screening provider, or a backend. Free text, such as an endpoint, a model, or an address, is never in the record.
- `reload` has `time`, `event`, and `result`: `ok` or the refusal code.

A hand edit of `config.toml` writes no `config_changed` record.

### Known limits

The policy is effective only when the agent can use only `amg` commands.
An agent with a shell as the same OS user can run `amgd`, edit `config.toml`, change pending actions in `cache.db`, or restart the daemon.
Anyone who can reach the listener gets the same decisions as the agent.
On Gmail, only the daemon code stops a send. The OAuth grant (`gmail.modify`) permits it.

## Mail send

**Caution:** If the write policy allows them, `mail send`, `mail reply`, `mail forward`, and `draft send` send real email at once. A sent message cannot be recalled.
The [Jev action check](#jev-action-check) can hold a command for approval, but it is a statistical check: it can miss harmful text.
An agent must run these commands only after the user authorizes that specific send.

The [write policy](#write-policy) key `mail_send` decides all four commands. The default is `deny`.
To use them, set `mail_send` to `allow` or `approve` in `config.toml`.
With `approve`, each send waits in the [approval console](#approval-console) and runs only after a person approves it.
`mail reply` and `mail forward` act on an existing message. The daemon always refuses a reply to or a forward of a `red`, `sealed`, or `error` message, or of one that it cannot screen. See [Sealed messages](#sealed-messages).

The commands take the same options as the matching draft commands:

- `mail send [--to ADDRESS]... [--cc ADDRESS]... [--bcc ADDRESS]... [--subject TEXT] [--body TEXT] [--html]` sends a new message. It needs at least one recipient in `--to`, `--cc`, or `--bcc`; a send with only `--bcc` recipients is valid. `--subject` and `--body` are optional and default to empty. The body is plain text with its line breaks unless `--html` is given; an `--html` body goes unchanged, without cleaning or checking.
- `mail reply ID [--all] [--cc ADDRESS]... [--bcc ADDRESS]... [--body TEXT] [--html]` sends a reply in the conversation of the message. `--body` is optional and defaults to empty; it goes above the quoted original, as plain text with its line breaks unless `--html` is given. An `--html` reply keeps the quoted original below the agent's HTML, which goes unchanged. Without `--all`, the reply goes to the sender; with `--all`, also to everyone on the original. `--cc` and `--bcc` are added to the recipients that the reply picks and never replace them; an address that is already a recipient, ignoring case, is not added again.
- `mail forward ID [--to ADDRESS]... [--cc ADDRESS]... [--bcc ADDRESS]... [--body TEXT] [--html]` forwards the message in its conversation, with the original's attachments. It needs at least one recipient in `--to`, `--cc`, or `--bcc`. `--body` is an optional note above the forwarded message, plain text with its line breaks unless `--html` is given; an `--html` forward keeps the original below the agent's HTML, which goes unchanged. Forwards with attachments follow the same `mail_send` decision as other sends.

`draft send ID` sends a draft as it is at the provider when the send runs: a new, reply, or forward draft, or one that the user wrote in the app. It has no options and no sealed check, because it acts on a draft; a draft that the user wrote is the user's responsibility. The [Jev action check](#jev-action-check) reads the draft's fields. A waiting send is not tied to a version of the draft: a change that the user makes in the app is sent as changed. A change or delete through `amg` withdraws the waiting send instead; see [Waiting sends and draft changes](#waiting-sends-and-draft-changes). On Microsoft 365, the daemon checks that the message is a draft (`isDraft`) and posts Graph `send` once, without a body; Graph saves the message in Sent Items. On Gmail, it finds the draft that holds the message and calls `drafts.send` once with only the draft ID; Gmail deletes the draft and makes a new sent message. A message that is not a draft is refused with `draft send refused: not a draft`. Approval of a draft write is not approval to send it.

Reply recipients follow each provider's app:

- On Microsoft 365, Microsoft picks them, as Outlook does: the daemon makes the reply draft with Graph `createReply`, or `createReplyAll` with `--all`, with no comment and no body. It then patches the draft once: the agent's text (escaped, with its line breaks) or HTML goes right after the opening `<body>` tag of the quoted reply that Graph made, and `--cc` and `--bcc` are added to Microsoft's lists. When Graph makes a plain-text reply, plain text goes above it after a blank line, and `--html` makes the body HTML with the escaped original text below. Without a body or new recipients, there is no patch. `mail reply` then sends the draft with Graph `send`. Each request goes once; if a step fails, the reply stops and nothing is sent, and the reply draft can stay in Drafts.
- On Gmail, the daemon picks them the way Gmail on the web does. The reply goes to the `Reply-To` addresses, or the sender when there is none; for a message that the account sent (label `SENT`), it goes to the original To. With `--all`, it also copies the original To and Cc (for a sent message, the original Cc), without the account's address and its send-as addresses (`users.settings.sendAs.list`), and lists each address once, ignoring case. The reply keeps the thread, `In-Reply-To`, `References`, and a `Re:` subject. A plain-text reply quotes the original text with `> `; an `--html` reply quotes the original HTML part, or the escaped original text, in a `<blockquote>`.

Forwards follow each provider's app:

- On Microsoft 365, the daemon makes the forward draft with Graph `createForward`, with no comment and with the `--to` recipients when there are any; Graph writes the quoted original and keeps the attachments. It then patches the draft once: the note goes above the quote as in a reply, and each of To, Cc, and Bcc that differs from the agent's list is set to it. Without a note and recipient changes, there is no patch. `mail forward` then sends the draft with Graph `send`. Each request goes once; if a step fails, the forward stops and nothing is sent, and the forward draft can stay in Drafts.
- On Gmail, the forward is inline, as Gmail on the web and Google's CLI forward: the note, a blank line, `---------- Forwarded message ---------` with the original's `From:`, `Date:` (when present), `Subject:`, `To:`, and `Cc:` (when present) lines, a blank line, and the original text. An `--html` forward puts the agent's HTML above the same block, with the lines escaped and separated by `<br>`, and the original HTML part, or the escaped original text, below it. The subject gets `Fwd: ` unless it already starts with `fwd:` or `fw:` in any case. The original's parts are read with `messages.attachments.get` when Gmail gives their data that way. A part whose data Gmail gives neither inline nor there fails the forward with `Gmail error ValueError` before anything is written, unless the part is empty. An inline image is an `image/*` part with a `Content-ID` whose `Content-Disposition` is not `attachment`: an `--html` forward carries each inline image with its `Content-ID` in a `multipart/related` beside the HTML part, so the quoted `cid:` images show, and a plain-text forward drops them, as Google's CLI does. Every other part with a file name, including an inline part with a `Content-ID` that is not an image, is attached again as a regular attachment. An attached message (`message/rfc822` or another `message/*` type) is attached as `application/octet-stream` under its file name, or `forwarded.eml` without one, because MIME allows no base64 for `message/rfc822`. The original itself is no longer attached as an `.eml` file. The forward keeps the thread, `In-Reply-To`, and `References`. The limits are in [HTTP interface and limits](#http-interface-and-limits).

Both providers keep a copy in the sent mail folder. On Gmail, the daemon refuses a reply to or a forward of a draft.
A result of `{"ok": true}` means that the provider accepted the message. It does not prove delivery.

Microsoft 365 login requests the `Mail.Send` scope, and token refreshes request it too.
Without this grant, send commands fail with `missing_scopes` before the policy decision and before any Graph request.
After `missing_scopes`, run `amgd account login ALIAS` again on the daemon host.

The daemon sends each request to the provider once. It does not retry a send, also not after a timeout or a rate limit, because a second send can deliver a duplicate message.
A send command can return `operation failed` after the message was sent, for example if the audit write fails.
A timeout, transport error, or `operation failed` does not prove that the send failed.
Before you retry such a command, ask the user to check the sent mail folder.

The audit record of a send has no content. A `mail send` record has no message ID, because the providers give none. A `mail reply` or `mail forward` record has the ID of the source message.

### Waiting sends and draft changes

A draft has at most one waiting `draft send`. The daemon withdraws and replaces waiting sends under the account's write lock, which a draft write holds from before its decision to its records:

- A `draft update` or `draft delete`, directly or as an approved run, first withdraws every waiting `draft send` of that draft in the account. It withdraws as soon as it holds the write lock, before the policy, the "not a draft" check, and the [Jev action check](#jev-action-check), so the console can no longer approve the old send once the change holds the lock. The withdrawn sends stay withdrawn also when the update or delete itself waits for approval, when the policy refuses it, when the message is not a draft, or when the write fails; then no new send is made.
- After a `draft update` that withdrew a send and ran, the daemon makes a new `mail_send` decision for the draft, under its new ID on Gmail, with a new read of the draft and a new [Jev action check](#jev-action-check). Under `allow` and `approve`, the new send waits for approval, also when Jev does not flag it: a draft change never sends mail by itself. Under `deny`, there is no new send. The new send never takes over the approval of an approved update. The update result has `send_approval_id`, the approval ID of the new send; follow it with `approvals status`. The policy record of the new send has the decision `approve` and the reason `policy`, because it always waits. If the daemon cannot make the new send after the update ran (the new read of the draft, the pending action, or an audit record fails), `draft update` fails although the draft changed, on Gmail also its ID, and no send of the draft waits. Read the draft again, for example with `mail list --folder` on the drafts folder or `mail get`, before you retry.
- `draft delete` makes no new send.
- A second `draft send` of a draft with a waiting send withdraws the old one first, also when the new send is refused (by a `deny` policy, or because the message is no longer a draft), then decides as usual.

A withdrawn send has the state `withdrawn`: it never runs, and the console cannot approve or deny it. Each withdrawal writes an approval audit record with the state `withdrawn`. Withdrawing never runs or approves anything; only a person in the [approval console](#approval-console) approves. `amgd account remove` still denies the pending actions of the account.

Known window: a send that a person approved before the agent's change reached the daemon's write lock runs with the draft as it is then, also with the change, because a waiting send has no tie to a draft version. A change that the user makes in the app is never noticed.

## Meetings

**Caution:** If the write policy allows them, `calendar invite`, `calendar update`, `calendar cancel`, and `calendar respond` (except `respond --no-notify` on Microsoft 365) send real meeting notifications at once.
The [Jev action check](#jev-action-check) can hold a command for approval, but it is a statistical check: it can miss harmful text.
An agent must run these commands only after the user authorizes that specific action.

The [write policy](#write-policy) denies these commands by default.
To use them, set `calendar_invite`, `calendar_update`, `calendar_cancel`, and `calendar_respond` to `allow` in `config.toml`.
These commands work for Microsoft 365, Gmail, and fake accounts. On Gmail, they change the primary calendar.

Microsoft 365 login requests the `Calendars.ReadWrite` scope, and token refreshes request it too.
Without this grant, meeting commands fail with `missing_scopes` before any Graph calendar request.
After `missing_scopes`, run `amgd account login ALIAS` again on the daemon host.
Gmail login requests the `calendar.events.owned` scope. Without this grant, meeting commands fail with `missing_scopes` before any Google Calendar request.

Times use the format `YYYY-MM-DD HH:MM` in the local timezone of the client.
The client refuses other formats, times that do not exist because of a DST change, and years outside 1–9999.
The client does not refuse a time that occurs two times when clocks go back. It uses one of the two offsets, so do not use such a time.
`--duration` takes values such as `30m` or `1h30m` and must be more than zero.
An invite needs exactly one of `--end` and `--duration`. An update can use one of them, but not both.

An invite needs one or more `--to` addresses and an explicit `--subject`, which can be empty.
The body is empty if you omit `--body`.
`--body` is plain text unless `--html` is given.
The invite sends plain text as escaped HTML and keeps the line breaks, so the text cannot become markup or a link with hidden text.
With `--html`, `--body` is HTML that the daemon sends unchanged, without cleaning or checking it: as the Graph event body with content type `html` on Microsoft 365, and as the event description on Gmail.
Every invite has an online meeting link by default: a Microsoft Teams meeting on Microsoft 365, and a Google Meet link on Gmail.
For a Teams meeting, Microsoft adds the Teams join block to the body.
Use `--no-online` to create a meeting without an online meeting.
`--to` attendees are required attendees. `--optional ADDRESS` (repeatable) adds optional attendees: a Graph attendee of type `optional` on Microsoft 365, and a Google Calendar attendee with `"optional": true` on Gmail. Optional attendees also receive the invitation. An address in both `--to` and `--optional`, ignoring case, is an invalid request.
On Gmail, guests cannot invite other people to a meeting that the daemon creates or updates.

An update needs at least one change and sends only the options that you supply.
An explicit empty `--subject ''`, `--location ''`, or `--body ''` clears that field.
`--body` replaces the whole body. As in an invite, plain text goes as escaped HTML that keeps the line breaks, and an `--html` body goes unchanged. `--html` without `--body` is invalid usage. On Microsoft 365, `--body` replaces the whole description, including the Teams join section. On Gmail, the Meet link is not part of the body and stays.
If an update changes only `--start`, the daemon keeps the current duration.
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
The daemon refuses recurring events and cancelled events. It does not write in these cases.

Every Gmail meeting command notifies all guests (`sendUpdates=all`).
On Gmail, `calendar cancel` deletes the event and notifies the guests. Google keeps the deleted event on your calendar with the status `cancelled`, so it can be restored from the calendar trash for 30 days. The daemon never deletes an event permanently.
Google sends no cancellation message. On Gmail, `calendar cancel --comment` fails with `calendar cancel refused: comments are not supported for this account`, before any credential or provider access. On Microsoft 365, the comment goes with the cancellation.

Invites need at least one required attendee address. The daemon sets no maximum on attendee lists: the provider refuses what it does not accept.

The client does not retry a meeting request, and the daemon sends no transaction ID.
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
amg --account work calendar availability --to me@example.com --to ana@example.com --start '2026-10-09 09:00' --end '2026-10-09 18:00'
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

Example `rules.toml` in the daemon home:

```toml
[accounts."me@example.com".receipts]
folder = "archive"
min_age_hours = 24
instructions = "Receipts that need no further action."

[accounts."me@example.com".finance]
category = "Finance"
instructions = "Messages about invoices or receipts."
```

Each table under `accounts` is named by the email of an account, in quotes, for example `[accounts."me@example.com".receipts]`. Emails compare in lowercase.
Rules follow the mailbox, not the alias, so a renamed alias keeps its rules.
The daemon ignores the rules for an email that no account has. `amgd config check` warns about them: `rules.toml has rules for EMAIL, which is no account; the daemon ignores them`.
The daemon reads `rules.toml` only at start and at a reload. After you edit it, run `amgd reload` on the daemon host.
`amgd` never writes `rules.toml`.

A folder target is a mailbox folder ID, `archive`, or `trash`.
On Microsoft 365, a folder ID of Deleted Items or of a folder below it fails. Use `trash`.
A category target must already exist in the mailbox.
`rules list` shows resolved names and missing-target warnings, never instructions or confidence scores.
Missing rules produce an empty organizer report without provider or Jev access.
Invalid rules stop `amgd serve`, and a reload with invalid rules is refused with `rules_invalid`.
`stats` needs neither account configuration nor provider authentication.
`amgd cache clear` runs only on the daemon host. It removes the verdicts, saved organization answers, and completion markers of all accounts, but not pending actions. The API has no cache operation.

`mail organize --since` and `--until` follow the date rule in [Commands and output](#commands-and-output): days on the CLI machine, `--until` day included.
The calendar range has no maximum: Microsoft Graph `calendarView` needs a start and an end but sets no maximum range.

| Verdict | Visible mail content |
| --- | --- |
| green | Cleaned headers (sender, subject, To, Cc, and in `mail get` the Bcc of drafts and sent mail), body, attachment names, and link IDs |
| yellow | Cleaned headers (sender, subject, To, Cc, and in `mail get` the Bcc of drafts and sent mail), attachment names, and reason, without body |
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
{"operation":"mail.list","account":"work","params":{"folder":"inbox","count":10}}
```

Operation names match `amg` command groups, such as `mail.get`, `mail.send`, `draft.update`, `calendar.invite`, `account.list`, `account.status`, and `approvals.status`.
`account.list` returns the alias, provider, email, and `default` flag of each account, with no secrets. `account.status` returns the sign-in state, email, and scopes of one account.
The API has no operation that changes credentials, the console password, accounts, or settings. A test compares the operation table with a reviewed list, so a new operation needs a review.
`mail.thread` takes `{"id":"THREAD_ID"}`, and `calendar.search` takes `{"text":"TEXT"}` and an optional `page`.
Meeting times on the HTTP interface are RFC 3339 timestamps. `calendar.update` also accepts a Go `duration` string.
Date bounds on the HTTP interface are exact RFC 3339 times, never days: `mail.search` `after` (included) and `before` (excluded, later than `after` when both are given), `mail.organize` `since` (required, not later than the daemon clock) and `until` (excluded, later than `since`), and `calendar.list` `start` (included) and `end` (excluded, later than `start`). A date without a time is refused. The CLI computes these times from days, as [Commands and output](#commands-and-output) describes.
`calendar.invite` takes an optional `optional` address list, `calendar.update` an optional `body`, and `calendar.respond` an optional boolean `no_notify`.
`calendar.invite` and `calendar.update` take an optional boolean `html` that marks `body` as HTML; a `calendar.update` with `html` and without `body` is refused as `invalid request`.
`draft.new` and `mail.send` take the optional address lists `to`, `cc`, and `bcc`, the optional strings `subject` and `body` (empty when absent), and the optional boolean `html`. `draft.reply` and `mail.reply` take the message `id`, the optional string `body` (empty when absent), the optional booleans `html` and `all`, and the optional address lists `cc` and `bcc`. `draft.forward` and `mail.forward` take the message `id`, the optional address lists `to`, `cc`, and `bcc`, the optional string `body` (empty when absent), and the optional boolean `html`. `mail.send` and `mail.forward` without an address in `to`, `cc`, or `bcc` are refused as `invalid request` before any policy decision. `draft.update` takes the message `id` and at least one of the address lists `to`, `cc`, and `bcc`, the string `subject`, and the string `body`, with the optional boolean `html` only together with `body`; an absent param stays unchanged and an empty list clears. A `draft.update` without a field is refused as `invalid request`. `draft.delete` and `draft.send` take only the message `id`.
`mail.archive`, `mail.trash`, `mail.read`, `mail.unread`, `mail.move`, and `mail.categorize` take either a non-empty `ids` list or `thread`, a conversation ID, never both; `in`, a folder ID, goes only with `thread` and defaults to `inbox`, as for `mail.list`. `mail.flag` takes neither `thread` nor `in`.
The `account` field is optional, and `params` must be an object.
Unknown operations or fields, duplicate keys, malformed JSON, trailing JSON, null fields, invalid types, and oversized requests are refused.
There is no HTTP login, token, key, raw-message, classification, rules-mutation, or approval endpoint. Only the [approval console](#approval-console), on its own address, approves or denies pending actions.
A pending action returns HTTP 200 with `{"status":"pending","approval_id":"...","url":"..."}`.
`approvals.status` takes `{"id":"APPROVAL_ID"}` (32 lowercase hex characters) and ignores `account`. An unknown ID returns HTTP 404 with `{"error":"approval not found"}`.
A policy refusal returns HTTP 500 with `{"error":"refused by policy: KEY"}`.

Requests are limited to 1 MiB and 16 JSON nesting levels.
The client bounds responses at 64 MiB and never follows redirects or retries requests, including stale-connection write replay.
Mail list and search take an optional `count` of 0 or more; 0 or no count sends no size, so the provider's default applies. They and `calendar.search` take an optional `page`: the `next_page` of an earlier result, at most 16 KiB of printable ASCII without spaces. A `next_page` from the provider that breaks these bounds fails the call. On Gmail the page is the provider's `pageToken`; a Gmail `calendar.search` page with more than the `events.list` default of 250 events fails. On Microsoft 365 a mail page is Graph's whole `@odata.nextLink`, which the daemon follows only with Graph's origin, the path of the same options, each query value of those options, and otherwise only `$top`, `$skip`, and `$skiptoken`; a calendar page is the Microsoft Search `from` offset.
Manual mutations need at least one ID or a `thread`, and recipient and attendee lists at least one address, except the `to`, `cc`, and `bcc` lists of `draft.new`, `mail.send`, `draft.forward`, `mail.forward`, and `draft.update`, and the `cc` and `bcc` lists of `draft.reply` and `mail.reply`, which can be empty (a `mail.send` or `mail.forward` needs one address among them); the daemon sets no maximum on these lists, because the provider refuses what it does not accept and the request size limit bounds them.
IDs and search strings are limited to 4,096 bytes.
A Gmail forward carries at most 100 attachments and inline images together, a bound of the daemon (Gmail documents no count limit), and at most 36,700,160 bytes of their data as encoded in the forward (base64, with a line break every 76 characters, about 4/3 of the raw size), Gmail's 35 MB limit for a raw message through the upload endpoints of `drafts.create` and `messages.send`. The daemon stops reading attachments as soon as the encoded total passes the limit. A forward over either bound fails with `Gmail error SizeError` before anything is written. A Gmail draft update reads the draft's raw message once (bounded like every Gmail response) and saves it through the upload endpoint of `drafts.update`; it has no attachment bounds of its own.
Stats accepts 1 to 365,000 days.

The daemon admits at most 64 concurrent calls.
The daemon sets no overall deadline that would cut off a long `mail organize` scan; set the client `--timeout` instead.
SIGINT, SIGTERM, or SIGHUP stops network admission, allows five seconds to drain, then cancels work with a ten-second shutdown limit.
The daemon logs no request arguments, bodies, credentials, or panic payloads.
There is no scheduler and no automatic mailbox work.

## Verification and limits

Tests run offline: real loopback HTTP, temporary homes, the fake provider, fake Graph, Gmail, and Jev endpoints, and a fake Keychain `Platform`.
No test needs real mailbox data, a browser, OpenRouter, or Keychain access. CI (`.github/workflows/ci.yml`) runs `make` on Linux only. The macOS-only code (the Keychain backend and the `*_darwin_test.go` tests) runs only in a local `make` on a Mac.

The end-to-end tests in `internal/e2e` build the real `amg` and `amgd` binaries and run `amgd serve` as its own process, on a temporary home with a fake account, the fake screening engine, and the file credential backend. The other `amgd` commands and `amg` run as separate processes, and the tests approve and deny pending actions through the approval console over HTTP.
After each test, the daemon log, `audit.jsonl`, and every column of `cache.db` except the `args` and `draft` columns of `pending` must contain no seeded name, subject, body, or address.

Not verified against real providers or services:

- mail sends, replies, and forwards: Cc, Bcc, and `--html` bodies on new drafts and sends were checked live on 2026-10-26 on Microsoft 365 and Gmail (received copies show no Bcc, sent copies show it, and `mail get` shows the Bcc of drafts and sent mail, from Graph `bccRecipients` and the Gmail `Bcc` header); the other cases, such as no Bcc read of received mail (Gmail without the `SENT` label, Microsoft 365 `from` another address) and Cc, Bcc, and `--html` added to replies and forwards, are verified only against fake Graph and Gmail endpoints;
- forwards: Graph `createForward` with no recipients and an empty body, and the Gmail inline forward with its attachments and inline images, which follows Google's unsupported `gws` CLI, were checked live on 2026-10-26; the other cases are verified only against fake Graph and Gmail endpoints: the patch of the note into Graph's quoted forward and of its recipients, and the Gmail header block byte for byte;
- a Gmail forward of a message with an attached message (`message/*`) for which Gmail gives neither inline data nor an `attachmentId` fails with `Gmail error ValueError`: the daemon never sends such an attachment empty;
- a forward on Microsoft 365 that fails after `createForward` leaves its forward draft in Drafts;
- draft updates: the Microsoft 365 `PATCH` of the given fields, and the Gmail `drafts.update` rebuild from the raw message with a base64 body part that keeps the thread, the inline images, and the attachments, were checked live on 2026-10-26; the other cases are verified only against fake Graph and Gmail endpoints: kept headers byte for byte (folded `References`, `From`), dropped `Date` and `Message-ID`, attached messages and other kept parts, the body added to an attachment-only draft, and the refusals of a message that is not a draft;
- draft delete and draft send: the refusals of a message that is not a draft and the single write without retry are verified only against fake Graph and Gmail endpoints and the fake provider (checked live on 2026-10-26: Graph `DELETE` moves a draft out of Drafts into a folder that is neither Deleted Items nor in `mail folders`, likely Recoverable Items, where it stays readable by ID and so restorable; Graph `send` of a forward draft sends it and moves it to Sent Items; Gmail `drafts.delete` removes the draft, and later reads fail; Gmail `drafts.send` of an HTML forward draft with inline images sends it and removes the draft);
- waiting sends and draft changes: the replacement of a waiting send after a Gmail draft update that changed the message ID was checked live on 2026-10-26 (the new waiting send under the new ID was approved and sent); the other cases are verified end to end with the fake provider, which keeps the draft ID on update, and in the guard tests, and an approval that comes while a draft update holds the write lock in the server tests;
- reply recipients: Microsoft's `createReplyAll` list matched the reply rule, the patch of the agent's body went above Graph's quoted reply, and the Gmail reply-all rule, which follows Google's unsupported `gws` CLI and the Gmail web behavior that it approximates (no Gmail API page states it), held also for a reply to the account's own sent message; these were checked live on 2026-10-26. The other cases, such as Microsoft's `createReply` list and the patch of Cc and Bcc into Microsoft's lists, are verified only against fake Graph and Gmail endpoints;
- a reply on Microsoft 365 that fails after `createReply` leaves its reply draft in Drafts;
- meeting invites, updates, cancellations, and responses: `--html` event bodies on `calendar invite` and `calendar update` were checked live on 2026-10-26 on Microsoft 365 and Gmail; the other cases are verified only against fake Graph and Google Calendar endpoints and the fake provider;
- provider acceptance of generated drafts;
- conversation actions: checked live on 2026-10-31 on Microsoft 365 and Gmail, each action from the Inbox and with `--in` a user folder or label, Sent, and the trash (the Gmail move from a label, the skip of trashed messages on Gmail, the archive from the trash (to Archive on Microsoft 365, no change on Gmail), and the well-known names `archive`, `sentitems`, and `deleteditems` on Microsoft 365), except a move with `--in` the trash; not checked live: a move with `--in` the trash on both providers (Gmail removing `TRASH` with `messages.modify`, and Microsoft 365 from Deleted Items or a folder below it), spam or junk as `--in`, and conversations with more than a few messages;
- `mail thread` on a Microsoft 365 conversation with a message in Junk Email (checked live on 2026-10-26: Gmail threads with drafts, and Microsoft 365 conversations across Inbox, Sent Items, Drafts, Deleted Items, and other folders; `calendar search` on both providers was checked the same day);
- Microsoft 365 device-code login, which organization policies can also refuse;
- paging with `--page` and `next_page`, and the default page sizes, on `mail list`, `mail search`, and `calendar search`;
- `mail search` date bounds: the Gmail `after:` and `before:` query in Unix seconds one second wider on each side, and the Microsoft 365 KQL `received` days one day wider on each side, each with exact times filtered by the daemon;
- requests to Jev cloud, or to any screening endpoint or model other than the default;
- migration of a real Keychain record.

Offline verification does not establish production readiness.

## License

Copyright 2026 The agentmailguard authors.
Licensed under the [Apache License, Version 2.0](LICENSE) (`Apache-2.0`).
Dependencies retain their own licenses and attribution requirements.
Binary distributions must include the applicable third-party license notices.
