# Security policy

## Reporting a vulnerability

Do not include vulnerability details, credentials, or real mailbox content in a public issue or pull request.
Use synthetic messages and fake provider endpoints for reproductions.

1. Open this project's GitHub repository, not a fork or the archived hackathon project.
2. Open **Security → Advisories**.
3. Select **Report a vulnerability** to submit a private report.

If the button is absent, private vulnerability reporting is not enabled.
Open a public issue requesting a private reporting channel, without vulnerability details or sensitive data.
Wait for a private channel before sending the report.
The maintainer must enable private reporting before announcing the public release.
Adding this file does not enable the GitHub feature.

A report can include the affected commit, operating system, synthetic reproduction, expected result, and actual result.
Do not attach OAuth tokens, passwords, callback URLs, provider responses, or server-home files.

## Support status

The project is experimental.
There is no published release-support schedule or guaranteed response time.
Report issues against the current source and identify the affected commit.
Offline checks do not establish production readiness.

## Supported deployment

The supported model is one server setup per machine, with multiple configured accounts in one server home.
Each account has a distinct configuration name.
Independent server homes with separate Keychain identities are not supported.
The macOS Keychain backend stores the accounts in one item under the current operating-system user.
A different `--home` does not create an independent Keychain namespace.

## Network boundary

Keep the server bound to loopback.
Use an SSH tunnel for clients on another machine.
The HTTP API has no application authentication or built-in TLS.
Anyone who can reach the listener can perform supported mailbox operations.
This includes other local users who can reach a loopback listener.
Origin refusal and absent CORS are not authentication controls.
A public listener or untrusted shared host is outside the supported security boundary.

## Mail and agent boundary

Treat all email-derived text as untrusted, including green output.
Screening is probabilistic and does not guarantee that content is safe.
A green verdict is not permission to obey instructions in a message.
The guard does not protect a human from fraud.

Missing or invalid screening fails closed.
Mail is sent only by `mail send`, `mail reply`, and `mail forward`, and only when the write policy allows it. The default policy denies sends.
Meeting invitations, updates, cancellations, and responses send notifications to attendees. The default policy denies them.
The command set never permanently deletes mail.
Manual mailbox operations still need the operator's or user's approval.
Trash is a provider move, not permanent deletion.

## Credentials and storage

Provider OAuth credentials and the Jev key are stored on the server host, not on clients.
The CLI uses the server API for data commands and does not load server credentials.
macOS supports the native Keychain backend.
Linux requires the password-encrypted file backend.
Both platforms require CGO for SQLite.

Keep the server home private and outside the source checkout.
Do not commit configuration, encrypted credential files, databases, audit files, or environment files.
Local caches and audit logs exclude mail bodies, subjects, sender identities, rule instructions, and raw provider responses.
One exception: an action that waits for approval stores its request arguments, such as recipients, subject, and body, in the `pending` table of `cache.db`.
Environment variables and process memory still expose secrets to sufficiently privileged local processes.
Another process under the same operating-system user is not an isolated trust domain.

## External screening and privacy

Screening sends rendered mail content, identities, calendar metadata, and rule instructions to OpenRouter's System One endpoint.
Private-event bodies are excluded, but private-event metadata is still sent.
Meeting join URLs can contain access secrets.
Content hidden from the agent can still reach the screening service before a verdict exists.
Encrypted credential storage does not prevent this transfer.

The project does not guarantee zero retention, no training, or a processing region.
Review the applicable service and provider policies before using real or organizational mail.
See [Privacy and external screening](README.md#privacy-and-external-screening) for the transferred fields and service references.
