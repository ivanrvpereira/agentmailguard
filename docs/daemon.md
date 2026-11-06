# Running the daemon

How `amgd` keeps its home, accounts, and settings, how it starts and reloads, and how `amg` reaches it.

## Daemon configuration

The supported deployment is one daemon per machine, with multiple accounts in one daemon home.
Use one home per machine. All homes share one Keychain item, so separate homes do not isolate credentials.
`--home` and `AMG_HOME` exist for tests and for a home that you moved.

The daemon owns the configuration, OAuth credentials, the screening key, `cache.db`, and `audit.jsonl`.
`amg` never reads these files. It sends every command to the daemon over HTTP.
The daemon home is `--home`, then `AMG_HOME`, then `~/.agentmailguard`.
Every `amgd` command runs on the daemon host and accepts `--home`.
To move a home from the earlier single `agentmailguard` program, see [Moving an existing home](setup.md#moving-an-existing-home).

### Accounts

An account has an alias, for example `work`, and an email.
The lowercase email is the account key: the name under which the daemon keeps the data of the account.
The cache, pending actions, stats, audit records, `rules.toml`, and the credential store use the account key.
The alias is only a short name for the command line. It appears only in `config.toml`, in commands, and in their output.
Every sign-in must return the email that `config.toml` has for the account. If it returns another email, `amgd` saves nothing.
Microsoft 365 sign-in uses the `userPrincipalName` of the user, else `mail`. Gmail sign-in uses the `emailAddress` of the profile.

Prefer a restricted provider application that grants only the required scopes.
The [setup guide](setup.md) lists the exact scopes that login requests.
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
- The optional `[policy]` tables in `config.toml` set the [write policy](write-policy.md#write-policy).

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
| `amgd config set console.password` | Reads the [console password](approval-console.md#console-password) from stdin and saves its hash in the credential store. |
| `amgd config unset KEY` | Removes a setting so that its default applies again, and reloads the daemon. |
| `amgd config check` | Validates `config.toml` and `rules.toml` with the code of the daemon. |

A key is a dotted path:

- `default_account`.
- `policy.KEY`, `policy.sealed.mail_trash`, `policy.sealed.mail_spam`, `accounts.ALIAS.policy.KEY`, `accounts.ALIAS.policy.sealed.mail_trash`, and `accounts.ALIAS.policy.sealed.mail_spam`, for example `policy.mail_send`. See [Write policy](write-policy.md#write-policy).
- `accounts.ALIAS.client_id`, `accounts.ALIAS.tenant_id` (Microsoft 365), and `accounts.ALIAS.client_secret` (Gmail).
- `screening.provider`, `screening.endpoint`, and `screening.model`. See [Screening](screening.md#screening).
- `console.listen`, `console.hosts`, and `console.username`. `console.hosts` takes a comma-separated list. See [Console configuration](approval-console.md#console-configuration).
- `credentials.backend`. See [Credential storage](credentials.md#credential-storage).

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
If no daemon runs, they print `no daemon runs for this home. Now run: amgd serve`; the change applies when the daemon starts. When the command got `--home` or `AMG_HOME`, the line ends with `--home HOME`.
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
For systemd and launchd, use the service files in the release archive and follow [Run as a service](setup.md#run-as-a-service).

## Sign-in and daemon start

Run sign-in on the daemon host. `amgd account add` signs in to a new account. `amgd account login` signs in again to an existing account:

```sh
amgd account add work --provider m365 --client-id '<client-id>' --tenant-id '<tenant-id>' --no-browser
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
A missing console password also refuses the start. On a terminal, `amgd serve` first asks for one. Without a terminal, its line is `console password: not set; run amgd config set console.password`, and the start ends with `start refused: run amgd config set console.password, then start the daemon again`. See [Console password](approval-console.md#console-password).

After the start, the daemon keeps credentials in memory. A reload reads the store again.
A token refresh saves to the store only if the stored login did not change since the daemon read it. Otherwise the daemon uses the stored login. Thus a login that `amgd account login` saved while the daemon runs is never lost.
A token refresh saves without losing the entries of the other accounts or the screening key.

Browser login is the default.
Microsoft 365 supports device-code login, but many organizations block it. When it is refused, the error names `--no-browser`.
Gmail refuses device-code login because that flow does not support the required scopes.
`--device-code` and `--no-browser` cannot be used together.

On a headless host, use `--no-browser`. It needs no SSH tunnel; the steps are in [Headless login](setup.md#headless-login).
The pasted address gets the same checks as the loopback callback: host, port, path, and `state` must match this sign-in.
If the address does not match, login asks for it again. A provider error in a matching address ends the login.
The PKCE verifier stays on the daemon host, so the code in the address cannot be exchanged anywhere else.
The program does not print or log the pasted address. The terminal shows it as you paste it.
Without `--no-browser`, browser login prints the URL and opens it with `open` on macOS or `xdg-open` on Linux. If that fails, login continues and accepts the loopback callback or a pasted address.

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

**Exposure warning:** the API listener has no authentication and no built-in TLS.
Anyone who can reach the listener can read guarded mailbox data and change mail.
They can also do every action that the [write policy](write-policy.md) allows, for example create drafts, send mail, and send meeting invitations, updates, cancellations, and responses.
The default listener is `127.0.0.1:8787`.
A non-loopback listener requires an explicit `amgd serve --listen` option.
At start, `amgd serve` prints the listener URL and how agents connect.
For a non-loopback listener it then prints a warning that names the address and the `mail_send` decisions of the accounts (`allow`, `approve`, or `deny`), and the fixes:

```text
daemon listening: http://100.64.0.10:8787
  agents connect with: AMG_HOST=http://100.64.0.10:8787 amg account list
WARNING: this API has no login and no TLS. Anyone who can reach 100.64.0.10:8787 can read screened mail and run every action that the write policy allows (sends: approve).
  Restrict it: allow only the agent machine (Tailscale grant or firewall), or keep --listen on 127.0.0.1 and use an SSH tunnel. See docs/daemon.md#local-and-remote-clients
```

For a wildcard listener such as `0.0.0.0`, the hint shows `DAEMON-HOST` in place of a host name: the daemon does not guess one.
Browser requests with an `Origin` header are refused, and the daemon does not enable CORS.
These restrictions are not a substitute for a protected network or encrypted tunnel.

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
Forward only the API port (8787). Do not forward the [approval console](approval-console.md#approval-console) port through a tunnel that the agent can use.
