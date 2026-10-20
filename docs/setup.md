# Operator setup

This guide supports macOS and Linux, with one setup per machine and multiple accounts in one private home directory.
The human operator owns every setup step. An AI agent must not perform registration, consent, login, key entry, or credential approval.

## Build

1. Install a currently supported, patched Go release. The minimum language version is Go 1.26.
2. Provide a C compiler and CGO support. On macOS, use the Xcode Command Line Tools. On Linux, use your distribution's compiler toolchain.
3. From the repository, run the commands:

```sh
CGO_ENABLED=1 make
./bin/agentmailguard --help
```

`make` checks formatting, runs vet and race tests, and builds `bin/agentmailguard`.
Both credential backends require CGO for SQLite. Windows is unsupported.

## Choose one private home

1. Choose one absolute path outside the source checkout on the server host. Replace `/private/path/agentmailguard` in every example with that path.
2. Create the directory without overwriting an existing setup:

```sh
umask 077
mkdir -p /private/path/agentmailguard
chmod 700 /private/path/agentmailguard
```

3. Choose the credential backend. macOS defaults to Keychain. Linux requires the password-encrypted file backend.

The home contains the account configuration, credential lock, cache, and audit file. Separate homes are not supported deployment isolation.
Keychain uses one item under the current operating-system user, service `agentmailguard`, account `credentials`, across configured accounts.
The file backend stores all OAuth credentials, the screening key, and the approval password hash in `credentials.age`.

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
The listener binds to `127.0.0.1` on a random available port. This is not the server port `8787`.
Microsoft ignores the port for registered localhost redirects.
On a headless host or Linux, use `--device-code`. Microsoft browser login currently invokes macOS `open` by default.
Device-code login requires no callback, but organization policies can refuse it.

Sources: [desktop registration](https://learn.microsoft.com/en-us/entra/identity-platform/scenario-desktop-app-registration), [redirect rules](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url#localhost-exceptions), and [Graph permissions](https://learn.microsoft.com/en-us/graph/permissions-reference).

## Register Gmail

1. In the [Google Cloud console](https://console.cloud.google.com/), select or create a project.
2. Under **APIs & Services → Library**, enable **Gmail API** and **Google Calendar API**.
3. Under **Google Auth platform → Branding**, enter the required application and contact information.
4. Under **Audience**, select **Internal** for eligible organization-only use, or **External**. For External Testing, add each account as a test user.
5. Under **Data Access**, add the exact scopes next. Under **Clients**, create a **Desktop app** OAuth client. Record its client ID and client secret.

```text
https://www.googleapis.com/auth/gmail.modify
https://www.googleapis.com/auth/calendar.events.owned
```

The Desktop client uses the installed-app loopback flow, not a Web application redirect registration.
Login requests `http://127.0.0.1:PORT/`, with a random available port and path `/`. It uses PKCE, `access_type=offline`, and `prompt=consent`.
Gmail device-code login does not support these scopes.

**Testing caveat:** External projects in Testing receive refresh tokens that expire after seven days for these scopes.
The basic-profile exception does not apply. See [Google's refresh-token expiration rules](https://developers.google.com/identity/protocols/oauth2#expiration).
Publishing the OAuth application for broader use can require Google verification or a security assessment because `gmail.modify` is restricted.
Publishing this Go source alone does not require Google verification.

Sources: [installed-app OAuth](https://developers.google.com/identity/protocols/oauth2/native-app), [consent and test users](https://developers.google.com/workspace/guides/configure-oauth-consent), [Gmail scopes](https://developers.google.com/workspace/gmail/api/auth/scopes), and [Calendar scopes](https://developers.google.com/workspace/calendar/api/auth).

## Create the account configuration

1. Create `config.toml` inside the chosen home. If it exists, preserve its contents and edit only the required account tables.
2. Replace the placeholders in this example with your application values. Remove unused account tables.
3. Restrict file access:

```sh
chmod 600 /private/path/agentmailguard/config.toml
```

```toml
[credentials]
backend = "file" # Required on Linux. Use "keychain" on macOS for the default.

[accounts.work]
provider = "m365"
client_id = "<entra-application-client-id>"
tenant_id = "<entra-directory-tenant-id>"

[accounts.personal]
provider = "gmail"
client_id = "<google-desktop-client-id>"
client_secret = "<google-desktop-client-secret>"

[accounts.other]
provider = "gmail"
client_id = "<same-google-desktop-client-id-if-appropriate>"
client_secret = "<same-google-desktop-client-secret-if-appropriate>"
```

Labels such as `work`, `personal`, and `other` must be unique. The first account in file order is the default.
Same-provider accounts can share an appropriate application registration. Each account needs its own login and consent.
Microsoft accounts must match the application's audience and configured tenant. Google accounts must match its audience or test-user list.
Do not put tokens, the screening key, or the file password in this file. Keep the Google client secret on the server host.

## Approve external screening

**CAUTION: Review the data transfers before consent or the first read.** Screening sends email content and calendar event content outside your machine.
Organization requests also send rule instructions and criteria to OpenRouter/Jev.
The default endpoint is `https://openrouter.ai/api/v1/systemone`, with model `typesafe/jev-1.13`. The [`[screening]`](../README.md#screening) table can select another endpoint and model, which then receives the same data.

OpenRouter documents [data collection](https://openrouter.ai/docs/guides/privacy/data-collection) and [provider logging policies](https://openrouter.ai/docs/guides/privacy/logging).
Retention and training policies differ by provider. Jev/System One-specific retention remains uncertain. This guide does not promise zero retention.
See [Privacy and external screening](../README.md#privacy-and-external-screening).

1. Review the applicable policies for your mailbox and screening provider.
2. Create an [OpenRouter API key](https://openrouter.ai/settings/keys) with an appropriate credit limit. See [key documentation](https://openrouter.ai/docs/api/reference/authentication).
3. Keep the key for the local terminal prompt in the next section. Do not send it to an assistant.

## Operator-only login and start

Local login and key commands refuse `--server` and `AGENTMAILGUARD_SERVER`. Remove that environment variable from the operator shell first.
`auth set-screening-key` and `auth set-approval-password` also refuse `--account`. Account selection belongs on login and account-specific client commands, not key setup.

For the file backend, enter the password at the terminal prompts. New credential files require password confirmation.
Do not put the password in command arguments, shell history, or configuration.
A secret manager can supply one password line through stdin. For piped key setup, supply the password line first, then the key line.
`AGENTMAILGUARD_CREDENTIALS_PASSWORD` takes precedence over stdin and can expose secrets to other processes.

1. Log in each configured account on the server host. Select the correct identity in the provider browser.
   On Linux, add `--device-code` to the Microsoft 365 login command:

```sh
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard
./bin/agentmailguard --account personal auth login --home /private/path/agentmailguard
./bin/agentmailguard --account other auth login --home /private/path/agentmailguard
```

2. Save the screening key through the hidden terminal prompt:

```sh
./bin/agentmailguard auth set-screening-key --home /private/path/agentmailguard
```

3. Optional: set the approval password to turn on the [approval console](../README.md#approval-console). You need it if the write policy uses `approve`:

```sh
./bin/agentmailguard auth set-approval-password --home /private/path/agentmailguard
```

4. Start the server on its default loopback listener:

```sh
./bin/agentmailguard serve --home /private/path/agentmailguard
```

5. In a second terminal, inspect status and make the first read:

```sh
./bin/agentmailguard --account work auth status
./bin/agentmailguard --account work mail list --count 5
./bin/agentmailguard --account work mail get '<message-id-from-list>' --pretty
```

6. After local login, account configuration changes, key replacement, or a password change, stop the server with Ctrl-C. Restart the server.

Use your configured label instead of `work`. Reads do not change the mailbox. A successful Microsoft status alone does not prove current Graph access.

## Headless login

For Microsoft 365, run this alternative login on the server host:

```sh
./bin/agentmailguard --account work auth login --home /private/path/agentmailguard --device-code
```

Open the printed verification URL on another computer. Enter the displayed device code yourself.

For Gmail:

1. Run `./bin/agentmailguard --account personal auth login --home /private/path/agentmailguard --no-browser` in the server's SSH terminal.
2. Open the printed sign-in URL on another computer. Approve both scopes.
3. Copy the full `http://127.0.0.1:PORT/?state=...&code=...` address after the expected connection error.
4. Paste the address into the operator terminal within five minutes. Never send this address or code to an assistant.

The pasted address must match this login's host, port, path, and state. No callback tunnel is necessary.

## Remote clients

Keep the server on `127.0.0.1:8787`. Its HTTP interface has no authentication or built-in TLS.
For remote reads, open an SSH tunnel from the client computer:

```sh
ssh -N -L 18787:127.0.0.1:8787 '<server-host>'
./bin/agentmailguard --server http://127.0.0.1:18787 --account work auth status
```

Other users who can reach either local listener can access the server. The tunnel does not replace machine access controls.

## Troubleshooting

| Problem | Operator action |
| --- | --- |
| Missing scopes or Gmail `missing_scopes` | Restore the exact required grants. Repeat local login with full consent. Restart the server. |
| Microsoft Conditional Access or device-code refusal | Ask the tenant administrator about permitted flows. Do not bypass organization policy. |
| Google Testing token expiry | Repeat local login and restart. Review Google's publication requirements before changing publication status. |
| Missing account or stale login | Restart after local login or account configuration changes. Use the same private home each time. |
| Keychain approval or a blocked terminal | Approve access interactively. A rebuilt binary can require approval again. Choose “Always Allow” only for your trusted build. |

Native provider acceptance remains unverified by this guide. Administrator consent, Google verification requirements, and screening-service access depend on your deployment.
