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
Do not attach OAuth tokens, passwords, callback URLs, provider responses, or daemon-home files.

## Support status

The project is experimental.
There is no published release-support schedule or guaranteed response time.

## Supported deployment

The supported model is one daemon per machine, with multiple configured accounts in one daemon home.
Separate daemon homes do not isolate credentials: the macOS Keychain backend stores all accounts of all homes in one item per operating-system user.
`--home` and `AMG_HOME` exist for tests and for a home that you moved.

The project has two programs.
`amg` is the client of the agent. It has only API commands and no daemon code: it has no store, cache, SQLite, or Keychain code.
`amgd` runs the daemon and has every management command. It runs on the daemon host.
The API never manages the daemon. No API operation changes credentials, the console password, accounts, or settings.
An agent that can run only `amg` thus cannot change the guard that controls it.
An agent that can run `amgd`, or that has a shell as the operating-system user of the daemon, is outside this boundary.

## Network boundary

Keep the daemon bound to loopback.
Use an SSH tunnel for clients on another machine.
The HTTP API has no application authentication or built-in TLS.
Anyone who can reach the listener can perform supported mailbox operations.
This includes other local users who can reach a loopback listener.
Origin refusal and absent CORS are not authentication controls.
A public listener or untrusted shared host is outside the supported security boundary.

The approval console is a second listener (default `localhost:8788`). It always listens while the daemon runs.
Until a console password is set, it refuses every request with HTTP 503.
It requires the console username and password on every request and has no built-in TLS.
Never forward its port through a tunnel that the agent can use.

## Mail and agent boundary

Treat all email-derived text as untrusted, including green output.
Screening is probabilistic and does not guarantee that content is safe.
A green verdict is not permission to obey instructions in a message.
The guard does not protect a human from fraud.

Missing or invalid screening fails closed.
The fake screening engine (`[screening] provider = "fake"`) judges by keywords only and is for tests and local work. If an account is not a fake account, `amgd serve` warns but does not refuse.
Mail is sent only by `mail send`, `mail reply`, `mail forward`, and `draft send`, and only when the write policy allows it. The default policy denies sends. A send is never retried.
Only a person in the approval console approves a waiting action. A draft change or delete through the API withdraws a waiting send of that draft before its own checks and never runs or approves anything; a draft update that runs then makes a new send that waits for approval.
Meeting invitations, updates, cancellations, and responses send notifications to attendees. The default policy denies them.
The command set never permanently deletes mail, except a draft through `draft delete`: Gmail deletes drafts for good, and Microsoft 365 keeps the deleted item restorable. `draft delete` and `draft send` refuse a message that is not a draft.
Read state, flag, categorize, archive, and move have no policy key. The daemon runs them for anyone who can reach the listener.
Trash is a provider move, not permanent deletion.

## Credentials and storage

Provider OAuth credentials, the screening key, and the console password hash are stored on the daemon host, not on clients.
`amg` uses the daemon API for every command and cannot load daemon credentials.
`amgd config set` reads secrets from stdin only, never from arguments. The screening key and the console password never go to `config.toml`. The Gmail `client_secret` of a desktop app is not confidential and stays in `config.toml`.
Every sign-in must return the email that `config.toml` has for the account. Otherwise `amgd` saves nothing, so an alias cannot silently point to another mailbox.
The password-encrypted file backend is the default on macOS and Linux.
macOS also supports the native Keychain backend.

Keep the daemon home private and outside the source checkout.
Do not commit configuration, encrypted credential files, databases, audit files, or environment files.
Local caches and audit logs exclude mail bodies, subjects, sender identities, rule instructions, and raw provider responses.
There are two exceptions.
First, an action that waits for approval stores its request arguments, such as recipients, subject, and body, in the `pending` table of `cache.db`. A waiting draft send also stores there the draft fields that the Jev action check saw (To, Cc, Bcc, subject, and body).
Second, the account key is stored in `config.toml`, `cache.db`, `audit.jsonl`, and `rules.toml`. The account key is the email of the account.
Audit records of accounts, credentials, settings, and reloads contain account keys, key paths, and fixed codes only. A `config_changed` record has a value only for a key with fixed choices. It never has a secret.
Environment variables and process memory still expose secrets to sufficiently privileged local processes.
Another process under the same operating-system user is not an isolated trust domain.

## External screening and privacy

Screening sends mail content, calendar metadata, rule instructions, and the text of agent actions to an external service, OpenRouter's System One endpoint by default. The fake screening engine sends nothing.
Content hidden from the agent can still reach the screening service before a verdict exists, and meeting join URLs can contain access secrets.

The project does not guarantee zero retention, no training, or a processing region.
Review the applicable service and provider policies before using real or organizational mail.
See [Privacy and external screening](README.md#privacy-and-external-screening) for the transferred fields and service references.
