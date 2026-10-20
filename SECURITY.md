# Security policy

## Reporting a vulnerability

Do not include vulnerability details, credentials, or real mailbox content in a public issue or pull request.
Use synthetic messages and fake provider endpoints for reproductions.

1. Open this project's GitHub repository, not a fork.
2. Open **Security → Advisories**.
3. Select **Report a vulnerability** to submit a private report.

If the button is absent, private vulnerability reporting is not enabled.
Open a public issue requesting a private reporting channel, without vulnerability details or sensitive data.
Wait for a private channel before sending the report.

A report can include the affected commit, operating system, synthetic reproduction, expected result, and actual result.
Do not attach OAuth tokens, passwords, callback URLs, provider responses, or server-home files.

## Support status

The project is experimental.
There is no published release-support schedule or guaranteed response time.

## Supported deployment

The supported model is one server setup per machine, with multiple configured accounts in one server home.
Separate server homes do not isolate credentials: the macOS Keychain backend stores all accounts in one item per operating-system user.

## Network boundary

Keep the server bound to loopback.
Use an SSH tunnel for clients on another machine.
The HTTP API has no application authentication or built-in TLS.
Anyone who can reach the listener can perform supported mailbox operations.
This includes other local users who can reach a loopback listener.
Origin refusal and absent CORS are not authentication controls.
A public listener or untrusted shared host is outside the supported security boundary.

The approval console is a second listener (default `localhost:8788`) that runs only when an approval password is set.
It requires the password on every request and has no built-in TLS.
Never forward its port through a tunnel that the agent can use.

## Mail and agent boundary

Treat all email-derived text as untrusted, including green output.
Screening is probabilistic and does not guarantee that content is safe.
A green verdict is not permission to obey instructions in a message.
The guard does not protect a human from fraud.

Missing or invalid screening fails closed.
The fake screening engine (`[screening] provider = "fake"`) judges by keywords only and is for tests and local work. If an account is not a fake account, `serve` warns but does not refuse.
Mail is sent only by `mail send`, `mail reply`, and `mail forward`, and only when the write policy allows it. The default policy denies sends.
Meeting invitations, updates, cancellations, and responses send notifications to attendees. The default policy denies them.
The command set never permanently deletes mail.
Read state, flag, categorize, archive, and move have no policy key. The server runs them for anyone who can reach the listener.
Trash is a provider move, not permanent deletion.

## Credentials and storage

Provider OAuth credentials and the screening key are stored on the server host, not on clients.
The CLI uses the server API for data commands and does not load server credentials.
macOS supports the native Keychain backend.
Linux requires the password-encrypted file backend.

Keep the server home private and outside the source checkout.
Do not commit configuration, encrypted credential files, databases, audit files, or environment files.
Local caches and audit logs exclude mail bodies, subjects, sender identities, rule instructions, and raw provider responses.
One exception: an action that waits for approval stores its request arguments, such as recipients, subject, and body, in the `pending` table of `cache.db`.
Environment variables and process memory still expose secrets to sufficiently privileged local processes.
Another process under the same operating-system user is not an isolated trust domain.

## External screening and privacy

Screening sends mail content, calendar metadata, rule instructions, and the text of agent actions to an external service, OpenRouter's System One endpoint by default. The fake screening engine sends nothing.
Content hidden from the agent can still reach the screening service before a verdict exists, and meeting join URLs can contain access secrets.

The project does not guarantee zero retention, no training, or a processing region.
Review the applicable service and provider policies before using real or organizational mail.
See [Privacy and external screening](README.md#privacy-and-external-screening) for the transferred fields and service references.
