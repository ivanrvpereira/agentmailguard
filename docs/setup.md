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

2. Set the console password for the [approval console](approval-console.md#approval-console). Until you set it, the console refuses every request, and every action that waits for approval stays pending. That includes a policy of `approve`, and a draft, send, or meeting write that the Jev action check flags or cannot check:

```sh
amgd config set console.password
```

You sign in to the console with the username `admin` and this password. To use another username, run `amgd config set console.username NAME`.

3. Start the daemon on its default loopback listener:

```sh
amgd serve
```

4. Read the check list that `amgd serve` prints. Each account must show `ok` or `no login needed`. Then the daemon prints `approval console listening` and `daemon listening`.
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

`amgd serve` reloads at SIGUSR1. SIGINT, SIGTERM, and SIGHUP stop it. Do not send SIGHUP to reload the daemon.
A service has no terminal, so prepare these items before the first start as a service:

- Save the screening key with `amgd config set screening.api_key`. Without a terminal, `amgd serve` does not prompt for a missing key, and the start fails.
- With the file backend, supply the password in `AMG_CREDENTIALS_PASSWORD` or on stdin. See [File backend and password input](credentials.md#file-backend-and-password-input).
- If the service uses another home than `~/.agentmailguard`, set the same `AMG_HOME` for the service and for your `amgd` commands.

For systemd, use one of these two reload methods in the service unit:

- `Type=notify-reload` with `ReloadSignal=SIGUSR1`. The daemon tells systemd when it is ready and when a reload starts and ends.
- `ExecReload=` with the full path of `amgd` and the `reload` argument, for example `ExecReload=/usr/local/bin/amgd reload`. `Type=notify` also works, because the daemon sends `READY=1`.

Then `systemctl reload` reloads the daemon.
launchd has no reload action. Run `amgd reload` after a hand edit.
On macOS, start `amgd serve` once in a terminal before you start it from launchd, so that you can answer the Keychain access prompt.

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
| `console password not set; run amgd config set console.password` | Run `amgd config set console.password` on the daemon host. |
| `no daemon runs for this home` | Start the daemon with `amgd serve`, or set the `AMG_HOME` of the daemon. |
| Keychain approval or a blocked terminal | Approve access interactively. A rebuilt binary can require approval again. Choose “Always Allow” only for your trusted build of `amgd`. |

Administrator consent, Google verification requirements, and screening-service access depend on your deployment.
