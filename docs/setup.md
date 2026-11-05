# Operator setup

This guide supports macOS and Linux, with one daemon per machine and multiple accounts in one private home directory.
The human operator owns every setup step. An AI agent must not perform registration, consent, login, key entry, or credential approval.
Run every `amgd` command in this guide on the daemon host: the machine that runs `amgd serve`.

## Install

Install `amg` and `amgd` as described in [Install](install.md#install), or build them as described in [Build and checks](install.md#build-and-checks).
The examples use `amg` and `amgd` as if they are on your PATH. With a source build, use `./bin/amg` and `./bin/amgd` if they are not.

## Choose one private home

The default home is `~/.agentmailguard`. Use one home per machine: all homes share one Keychain item.
`--home` and `AMG_HOME` exist for tests and for a home that you moved. If you use another path, set `AMG_HOME` to it for every `amgd` command.

1. Create the directory without overwriting an existing setup:

```sh
umask 077
mkdir -p ~/.agentmailguard
chmod 700 ~/.agentmailguard
```

2. Choose the credential backend. The default is the password-encrypted file backend, which works on macOS and Linux. To use the macOS Keychain instead, select it before you add an account:

```sh
amgd config set credentials.backend keychain
```

The home contains the account configuration, credential lock, cache, and audit file. See [Credential storage](credentials.md#credential-storage) for both backends.

## Before you register

Each user registers their own provider application. agentmailguard has no shared application.
The type of account tells you who can register the application and which audience to select.

| Account | Who registers the application | What to do |
| --- | --- | --- |
| Microsoft 365 work or school | Usually you. Your organization can turn off app registration and user consent. | Follow [Register Microsoft 365](#register-microsoft-365). If the portal or the sign-in refuses, see [Microsoft administrator approval](#microsoft-administrator-approval). |
| Google Workspace | Usually you. Your administrator can block new projects or unknown apps. | Follow [Register Gmail](#register-gmail) and select the **Internal** audience. Google does not verify Internal apps, and their logins do not expire after seven days. |
| Personal Gmail | You. | Follow [Register Gmail](#register-gmail) and select the **External** audience. Then follow [Stop the weekly Gmail sign-in](#stop-the-weekly-gmail-sign-in). |

## Register Microsoft 365

1. In the [Microsoft Entra admin center](https://entra.microsoft.com/), open **App registrations → New registration**.
2. Select the supported account audience for your users. For one organization, select **Accounts in this organizational directory only**.
3. Under **Authentication → Add a platform → Mobile and desktop applications**, register exactly `http://localhost`.
4. Under **Authentication → Advanced settings**, set **Allow public client flows** to **Yes**. Save the change.
5. Under **API permissions → Add a permission → Microsoft Graph → Delegated permissions**, add the grants listed next. Record the application and tenant IDs.

Graph grants: `User.Read`, `Mail.ReadWrite`, `Mail.Send`, `Calendars.Read`, `Calendars.ReadWrite`, and `MailboxSettings.Read`.
Also include `offline_access`, `openid`, and `profile` under the delegated OpenID permissions. If your organization requires administrator consent, obtain it.
Do not create an application secret or add application permissions.

The exact requested scope string is:

```text
https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/Calendars.ReadWrite https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile
```

Browser login requests `http://localhost:PORT` and accepts the callback path `/`.
The listener binds to `127.0.0.1` on a random available port. This is not the daemon port `8787`.
Microsoft ignores the port for registered localhost redirects.
Microsoft browser login opens the browser with macOS `open`, so on Linux or a headless host use `--device-code`.
Device-code login requires no callback, but organization policies can refuse it.

Sources: [desktop registration](https://learn.microsoft.com/en-us/entra/identity-platform/scenario-desktop-app-registration), [redirect rules](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url#localhost-exceptions), and [Graph permissions](https://learn.microsoft.com/en-us/graph/permissions-reference).

### Microsoft administrator approval

By default, a member of an organization can register an application and consent to access to their own mailbox. Many organizations turn off one or both of these permissions.

- If **New registration** is not available, ask your administrator to register the application for you, or to give you the Application Developer role.
- If the sign-in shows **Need admin approval**, an administrator must consent to the permissions. The administrator opens **API permissions** in the application and selects **Grant admin consent**. `Mail.ReadWrite` and `Mail.Send` often need this consent.
- The permissions are delegated. The application can access only the mailbox of the person who signs in.

Sources: [default user permissions](https://learn.microsoft.com/en-us/entra/fundamentals/users-default-permissions) and [user consent settings](https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/configure-user-consent).

## Register Gmail

1. In the [Google Cloud console](https://console.cloud.google.com/), select or create a project.
2. Under **APIs & Services → Library**, enable **Gmail API** and **Google Calendar API**.
3. Under **Google Auth platform → Branding**, enter the required application and contact information.
4. Under **Audience**, select **Internal** for a Google Workspace account, or **External** for a personal Gmail account. For **External**, add each account as a test user.
5. Under **Data Access**, add the exact scopes next. Under **Clients**, create a **Desktop app** OAuth client. Record its client ID and client secret.

```text
https://www.googleapis.com/auth/gmail.modify
https://www.googleapis.com/auth/calendar.events.owned
```

The Desktop client uses the installed-app loopback flow, not a Web application redirect registration.
Login requests `http://127.0.0.1:PORT/`, with a random available port and path `/`. It uses PKCE, `access_type=offline`, and `prompt=consent`.
Gmail device-code login does not support these scopes.

Sources: [installed-app OAuth](https://developers.google.com/identity/protocols/oauth2/native-app), [consent and test users](https://developers.google.com/workspace/guides/configure-oauth-consent), [Gmail scopes](https://developers.google.com/workspace/gmail/api/auth/scopes), and [Calendar scopes](https://developers.google.com/workspace/calendar/api/auth).

### Stop the weekly Gmail sign-in

A new **External** app has the publishing status **Testing**. Google gives a Testing app refresh tokens that expire after seven days, so the login stops after one week.
An **Internal** app does not have this limit. Do these steps only for an **External** app:

1. In **Google Auth platform → Audience**, select **Publish app**, and then select **Confirm**. The status changes to **In production**. This step does not send the app to Google for verification.
2. On the daemon host, sign in again once:

```sh
amgd account login ALIAS
```

3. When the browser shows "Google hasn't verified this app", select **Advanced**, and then select the link to your app. You made this app, so this warning is expected.

An unverified app accepts 100 users at most, for all time. For your own accounts, this limit has no effect.
Do not submit the app for verification. Google requires verification, and for `gmail.modify` a security assessment, only for an app that you give to other people.

Sources: [publishing status and limits](https://developers.google.com/identity/protocols/oauth2/production-readiness/overview) and [refresh-token expiration](https://developers.google.com/identity/protocols/oauth2#expiration).

## Approve external screening

**CAUTION: Review the data transfers before consent or the first read.** Screening sends email content, calendar event content, and the text of actions that the agent writes outside your machine.
Organization requests also send rule instructions and criteria to OpenRouter/Jev.
The default endpoint is `https://openrouter.ai/api/v1/systemone`, with model `typesafe/jev-1.14`. The [`[screening]`](screening.md#screening) table can select another endpoint and model, which then receives the same data.

OpenRouter documents [data collection](https://openrouter.ai/docs/guides/privacy/data-collection) and [provider logging policies](https://openrouter.ai/docs/guides/privacy/logging).
Retention and training policies differ by provider. Jev/System One-specific retention remains uncertain. This guide does not promise zero retention.
See [Privacy and external screening](screening.md#privacy-and-external-screening).

1. Review the applicable policies for your mailbox and screening provider.
2. Create an [OpenRouter API key](https://openrouter.ai/settings/keys) with an appropriate credit limit. See [key documentation](https://openrouter.ai/docs/api/reference/authentication).
3. Keep the key for the hidden terminal prompt in [Save the secrets and start](#save-the-secrets-and-start). Do not send it to an assistant.

## Add the accounts

For the file backend, enter the password at the terminal prompts. The first command creates the credential file and asks for the password twice. For other password sources, see [File backend and password input](credentials.md#file-backend-and-password-input).

1. Add each account. Replace the placeholders with your application values. Each command opens the provider sign-in. Select the correct identity in the browser.
   On Linux or a host without a browser, use the options in [Headless login](#headless-login):

```sh
amgd account add work --provider m365 --client-id '<entra-application-client-id>' --tenant-id '<entra-directory-tenant-id>'
amgd account add personal --provider gmail --client-id '<google-desktop-client-id>' --client-secret '<google-desktop-client-secret>'
amgd account add other --provider gmail --client-id '<same-google-desktop-client-id-if-appropriate>' --client-secret '<same-google-desktop-client-secret-if-appropriate>'
```

2. Read the result. Each command prints `✓ Added account ALIAS: EMAIL (PROVIDER)`. The email comes from the sign-in, and you never type it.

`amgd account add` creates `config.toml` with mode 0600 if it does not exist. It writes one table for each account, with the email of the sign-in.
The first account becomes `default_account`, the account that `amg` uses without `--account`.
An alias such as `work`, `personal`, or `other` uses only letters, digits, `_`, and `-`, with a maximum of 64 characters. Each alias and each email can belong to one account only.
Same-provider accounts can share an appropriate application registration. Each account needs its own login and consent.
Microsoft accounts must match the application's audience and configured tenant. Google accounts must match its audience or test-user list.
Do not put tokens, the screening key, or the file password in `config.toml`. The Google client secret of a desktop application stays in `config.toml` on the daemon host.

To sign in again later, run `amgd account login ALIAS`. It accepts only the email that `config.toml` has for the account.
To remove an account, run `amgd account remove ALIAS` on a terminal and type the alias to confirm.
For a test account without a provider, run `amgd account add demo --provider fake`. See [Fake provider](fake-provider.md#fake-provider).

## Save the secrets and start

1. Save the screening key through the hidden terminal prompt:

```sh
amgd config set screening.api_key
```

2. Set the console password for the [approval console](approval-console.md#approval-console). The daemon does not start without it: on a terminal, `amgd serve` asks for it if you skip this step. Every action that waits for approval needs it. That includes a policy of `approve`, and a draft, send, or meeting write that the Jev action check flags or cannot check:

```sh
amgd config set console.password
```

You sign in to the console with the username `admin` and this password. To use another username, run `amgd config set console.username NAME`.

3. Start the daemon on its default loopback listener:

```sh
amgd serve
```

4. Read the check list that `amgd serve` prints. Each account must show `ok` or `no login needed`. Then the daemon prints where to go next:

```text
approval console: http://localhost:8788/ (user admin)
daemon listening: http://127.0.0.1:8787
  agents connect with: AMG_HOST=http://127.0.0.1:8787 amg account list
```

Open the console URL in a browser on the daemon host.

5. In a second terminal, list the accounts, inspect the status, and make the first read:

```sh
amg account list
amg --account work account status
amg --account work mail list --count 5
amg --account work mail get '<message-id-from-list>' --pretty
```

Use your configured alias instead of `work`. Reads do not change the mailbox. A successful Microsoft status alone does not prove current Graph access.

## Change the configuration

You do not have to restart the daemon after most changes.
`amgd config set`, `config unset`, `account add`, `account login`, and `account remove` reload the running daemon after the change. The two secret commands in the previous section also reload it.

1. Show the current configuration with `amgd config list`.
2. Change a setting with `amgd config set KEY VALUE`, for example `amgd config set policy.mail_send approve`.
3. After a hand edit of `config.toml` or `rules.toml`, run `amgd config check`, and then run `amgd reload`.
4. Read the daemon log. It shows `reload requested`, the check list, and `reload ok`.

If the new configuration is invalid, the daemon log shows `reload refused: CODE; old settings kept`, and the daemon keeps the old configuration.
A change of `console.listen` or `credentials.backend` prints `restart needed: KEY changed`. Stop the daemon, for example with Ctrl-C, and start it again.
After `amgd store migrate` or `amgd store change-password`, start the daemon again. Both commands refuse while the daemon runs.
See [Settings](daemon.md#settings) and [Reload](daemon.md#reload) for all keys and messages.

## Run as a service

Each release archive has a service file in `service/`: `amgd.service` for systemd on Linux, and `io.github.ivanrvpereira.agentmailguard.plist` for launchd on macOS. Homebrew installs it in `$(brew --prefix)/share/agentmailguard/service`. The same files are in [`packaging/`](../packaging/).

A service has no terminal, so `amgd serve` cannot ask for a missing secret: the start fails and the log names the command to run. Prepare the home first, as the user who will run the daemon, on a terminal of the daemon host (an SSH session is enough):

1. If the daemon uses another home than `~/.agentmailguard`, set `AMG_HOME` for every command, and later for the service.
2. On macOS, select the Keychain backend: `amgd config set credentials.backend keychain`. On Linux, keep the file backend, and choose its password at the first command.
3. Add the accounts, as in [Add the accounts](#add-the-accounts). On a host without a browser, add `--no-browser`, as in [Headless login](#headless-login).
4. Save the screening key with `amgd config set screening.api_key`, unless `[screening]` selects the fake engine.
5. Set the console password with `amgd config set console.password`.
6. Optional on Linux, required on macOS: run `amgd serve` once in the terminal, read the check list, and stop it with Ctrl-C. On macOS, this is when you answer the Keychain access prompt.
7. Install the service file, as below.

`amgd serve` reloads at SIGUSR1. SIGINT, SIGTERM, and SIGHUP stop it. Do not send SIGHUP to reload the daemon.

### systemd

In `amgd.service`, replace `CHANGE-ME` with the user who owns the home (in `User=`, `AMG_HOME`, and `ReadWritePaths=`), and the path of `amgd` in `ExecStart=` if it is not `/usr/local/bin/amgd`. Then, as root:

```sh
systemd-creds encrypt --name=amgd-credentials-password - /etc/credstore.encrypted/amgd-credentials-password
install -m 0644 amgd.service /etc/systemd/system/amgd.service
systemctl daemon-reload
systemctl enable --now amgd
journalctl -u amgd
```

`systemd-creds` reads the credential file password from stdin: type it, then press Ctrl-D twice. The unit gives it to `amgd serve` on stdin from the encrypted systemd credential, so the password is never in the environment or in a plain-text file. The unit needs systemd 253 or later for `Type=notify-reload` with `ReloadSignal=SIGUSR1`: the daemon tells systemd when it is ready and when a reload starts and ends, and `systemctl reload amgd` reloads it. With an older systemd, use `Type=notify` and `ExecReload=/usr/local/bin/amgd reload`.

The unit lets the daemon write only its home, and it restarts the daemon after a failure. A refused start, for example a missing console password, restarts every 10 seconds, at most 5 times in 5 minutes. Then the unit stays failed: fix the cause that the journal names, then run `systemctl reset-failed amgd` and `systemctl start amgd`.

### launchd

The plist runs `amgd serve` as a launch agent in your login session, so the daemon can use the Keychain. In the plist, set the path of `amgd` (`/opt/homebrew/bin/amgd` on Apple silicon, `/usr/local/bin/amgd` on Intel), replace `CHANGE-ME` in the log path, and set `AMG_HOME` if you use another home. launchd does not expand `~` or `$HOME`. Then:

```sh
cp io.github.ivanrvpereira.agentmailguard.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.ivanrvpereira.agentmailguard.plist
```

The daemon log is in `~/Library/Logs/agentmailguard.log`. launchd restarts the daemon after a crash, but not after a refused start. launchd has no reload action: `amgd config set` and the `amgd account` commands reload the daemon themselves; after a hand edit, run `amgd reload`. To stop the daemon, run `launchctl bootout gui/$(id -u)/io.github.ivanrvpereira.agentmailguard`.

## Headless login

For Microsoft 365, add `--device-code` on the daemon host:

```sh
amgd account add work --provider m365 --client-id '<entra-application-client-id>' --tenant-id '<entra-directory-tenant-id>' --device-code
amgd account login work --device-code
```

Open the printed verification URL on another computer. Enter the displayed device code yourself.

For Gmail:

1. Run `amgd account login personal --no-browser` in the SSH terminal of the daemon host. For a new account, add `--no-browser` to `amgd account add`.
2. Open the printed sign-in URL on another computer. Approve both scopes.
3. Copy the full `http://127.0.0.1:PORT/?state=...&code=...` address after the expected connection error.
4. Paste the address into the operator terminal within five minutes. Never send this address or code to an assistant.

The pasted address must match this login's host, port, path, and state. No callback tunnel is necessary.

## Moving an existing home

The earlier single `agentmailguard` program used the alias as the account key. `amgd` uses the email. No migration code exists, so you move a home once by hand.
The operator does steps 1, 7, 9, and 10: they use the console, the credential store, or `amgd`. An AI agent can do the other file steps, but it never runs `amgd` and never reads or changes the Keychain item or the credential file.

1. In the approval console of the old daemon, approve or deny every pending item. Then stop the old daemon.
2. Delete `cache.db` from the home. The new cache uses a new schema, and `amgd` refuses the old file. The daemon makes a new `cache.db` and screens mail again.
3. In `config.toml`, add `version = 1` and `default_account = "ALIAS"` at the top. Use the alias that was first in the file, because that account was the default.
4. In each account table, add `email = "..."` with the email of the mailbox in lowercase. For a fake account, use `ALIAS@fake.invalid`.
5. Rename the table `approvals` to `console`. Its keys `listen` and `hosts` stay the same.
6. In `rules.toml`, rename each `[accounts.ALIAS.RULE]` table to `[accounts."EMAIL".RULE]`.
7. Sign in again to each provider account with `amgd account login ALIAS`. The login is then saved under the new `PROVIDER/EMAIL` entry. The old `PROVIDER/ALIAS` entry stays in the record without use. The screening key and the console password hash stay in the record and need no change.
8. Change scripts and service files to the new names: `amg`, `amgd`, and the environment variables `AMG_HOME`, `AMG_HOST`, and `AMG_CREDENTIALS_PASSWORD`.
9. Run `amgd config check`. Then start the daemon with `amgd serve`, and make sure that each account line shows `ok` or `no login needed`.
10. Delete the old `agentmailguard` binary and its `amg` alias in `mise.toml`, if you have one. On macOS, open Keychain Access, open the item `agentmailguard` (account `credentials`), and remove the old binary from its access list, so that only `amgd` can read the item.

The rows of a fake account in `fake-provider.db` use the alias in the `account` column. Change that column to the email in each table, or delete `fake-provider.db` and seed it again.

## Remote clients

Keep the daemon on its loopback listener and use an SSH tunnel for `amg` on another machine. See [Local and remote clients](daemon.md#local-and-remote-clients).

## Troubleshooting

| Problem | Operator action |
| --- | --- |
| Missing scopes or Gmail `missing_scopes` | Restore the exact required grants. Run `amgd account login ALIAS` with full consent. |
| `You signed in as ..., but ALIAS is ...` | Sign in again with the identity of the account. To add the other mailbox, run `amgd account add` with a new alias. |
| Microsoft Conditional Access or device-code refusal | Ask the tenant administrator about permitted flows. Do not bypass organization policy. |
| Gmail login stops after seven days | The app has the status Testing. Follow [Stop the weekly Gmail sign-in](#stop-the-weekly-gmail-sign-in). |
| Google sign-in shows "Access blocked" | The app has the status Testing, and the account is not a test user. Add the account under **Audience → Test users**, or publish the app. |
| Google Workspace sign-in says that your administrator blocks the app | Ask the Workspace administrator to mark the app as trusted under **API controls** in the Google Admin console. |
| Microsoft sign-in shows **Need admin approval** | Ask the tenant administrator for consent. See [Microsoft administrator approval](#microsoft-administrator-approval). |
| Missing account or stale login | Run `amg account list` and `amg account status`. After a hand edit of `config.toml`, run `amgd reload`. Use the same home each time. |
| `reload refused: CODE; old settings kept` in the daemon log | Fix the key that the `error:` line names. Run `amgd config check`, and then `amgd reload`. |
| `start refused: run amgd config set console.password, then start the daemon again` | Run the command that the line gives on the daemon host, then start the daemon again. |
| `no daemon runs for this home. Now run: amgd serve` | Run the command that the line gives. It includes `--home` when the command used `--home` or `AMG_HOME`. |
| Keychain approval or a blocked terminal | Approve access interactively. A rebuilt binary can require approval again. Choose “Always Allow” only for your trusted build of `amgd`. |

Administrator consent, Google verification requirements, and screening-service access depend on your deployment.
