# agentmailguard

A guard between an AI agent and a person's Microsoft 365 or Gmail mailbox and calendar. It limits what the agent sees in mail and events, and decides which writes the agent can make.

## Language

### Programs and hosts

**Daemon**:
The long-lived process (`amgd serve`) that holds the credentials, talks to the providers, screens content, and applies the write policy.
_Avoid_: server, service, backend

**Client**:
The agent's program (`amg`). It has only API commands and sends each one to the daemon.
_Avoid_: CLI (both programs are CLIs), agent

**Daemon host**:
The machine where the daemon runs. Every management command runs here, never through the API.
_Avoid_: server, remote

**Home**:
The directory that holds one daemon's configuration, cache, audit log, and file credential store.
_Avoid_: data dir, profile

**Management command**:
An `amgd` command that changes the daemon's accounts, settings, credentials, or cache. The API has none.
_Avoid_: admin command

**API operation**:
One named call that the client can make to the daemon, such as `mail.list` or `calendar.invite`.
_Avoid_: endpoint, method, tool

**Reload**:
The daemon reading its configuration, rules, and credential store again without a restart. A refused reload keeps the old state.
_Avoid_: restart, refresh

### Accounts

**Account**:
One mailbox and calendar at one provider that the daemon guards.
_Avoid_: mailbox, user, profile

**Account key**:
The lowercase email of an account. It is the account's identity everywhere the daemon keeps data.
_Avoid_: account ID, user ID

**Alias**:
A short command-line name for an account, such as `work`. It is not the account's identity.
_Avoid_: account name, label

**Default account**:
The account that the client uses when it names no alias.

**Provider**:
The mail and calendar service behind an account: Microsoft 365, Gmail, or the fake provider.
_Avoid_: backend, service

**Fake provider**:
A local stand-in provider for tests and local work. Nothing leaves the machine.
_Avoid_: mock, stub, sandbox

**Login**:
The saved provider sign-in of an account. It is saved only when the provider returns the account's email.
_Avoid_: token, session, auth

**Extra grant**:
A provider permission that a login has but the daemon never asked for and never uses.
_Avoid_: extra scope

**Credential store**:
The one protected record that holds every login, the screening key, and the console password hash, in the macOS Keychain or a password-encrypted file.
_Avoid_: vault, secrets file, keyring

### Screening

**Screening**:
The judgement of a message or event by the decision engine before the agent sees any of its content.
_Avoid_: classification, scanning, filtering

**Decision engine**:
The service that screens content and checks actions: Jev, or the keyword-based fake engine.
_Avoid_: classifier, model, LLM

**Jev**:
The external decision model that the daemon calls for screening and action checks.

**Fake screening**:
The keyword-based decision engine for tests and local work. It is a real engine choice, not missing screening.
_Avoid_: no screening, disabled screening

**Verdict**:
The result of screening one message: green, yellow, red, sealed, or error. It decides what the agent can see.
_Avoid_: rating, score, label, classification

**Green**:
A verdict for content that shows no sign of prompt injection. The agent sees the cleaned body.

**Yellow**:
A verdict for content with some sign of prompt injection. The agent sees headers but no body.
_Avoid_: suspicious, warning

**Red**:
A verdict for content that is likely prompt injection. The agent sees only the sender address, date, and reason.
_Avoid_: malicious, blocked, spam

**Sealed**:
A verdict for account-security content, such as verification codes or password resets, that the agent must not see or act on freely.
_Avoid_: sensitive, private, locked

**Error verdict**:
A verdict for content that could not be screened. It hides everything and is screened again on the next read.
_Avoid_: unknown, failed

**Guarded view**:
What the agent receives for a message: only the fields that its verdict allows.
_Avoid_: sanitized message, safe view

**Untrusted text**:
Any text that comes from mail or events, at every verdict, including green.

### Writes and approval

**Write action**:
An agent request that changes the mailbox or calendar or sends something.
_Avoid_: mutation, command

**Write policy**:
The per-account settings that decide each write action with allow, approve, or deny.
_Avoid_: permissions, ACL, rules

**Policy key**:
The name of one decided group of write actions, such as `mail_send` or `calendar_invite`.
_Avoid_: permission

**Decision**:
One of allow, approve, or deny for a policy key. Anything else denies.
_Avoid_: verdict (that is screening)

**Sealed decision**:
The stricter decision that applies when an action acts on a sealed or error message.

**Action check**:
Jev's check of the text that the agent wrote for an action. It can only make an allowed action wait for approval.
_Avoid_: content check, outbound screening

**Pending action**:
A write action that the policy or the action check holds until a person approves or denies it. It keeps the name after the decision, with its state: pending, running, done, denied, withdrawn, or failed.
_Avoid_: pending item, item, approval request, queued action

**Approval console**:
The password-protected web page where a person approves or denies pending actions. It is the only place that can do so.
_Avoid_: dashboard, admin UI

**Send**:
Mail that leaves the account at once: a new message, reply, forward, or saved draft. A send is never retried.
_Avoid_: deliver, submit

**Meeting notification**:
The message that a provider sends to attendees or the organizer when a meeting is created, changed, cancelled, or answered.
_Avoid_: invite email, calendar update

**Trash**:
A move of a message into the provider's trash, never a permanent deletion.
_Avoid_: delete, remove

### Organization

**Rule**:
A named instruction in `rules.toml` for one account that files matching mail into a folder or a category.
_Avoid_: filter, policy

**Organize run**:
One pass over a date range that matches messages against the rules, and changes them only when asked to apply.
_Avoid_: sweep, sync, triage

**Security tag**:
An existing Yellow, Red, or Sealed category that an organize run can put on hidden mail.
_Avoid_: label, flag

### Audit

**Audit record**:
One content-free line in the audit log that names an event, the account key, and fixed codes, never mail content.
_Avoid_: log entry, event log

### Calendar

**Availability**:
The free/busy status of requested participants in fixed time slots. Missing data is unknown, never free.
_Avoid_: free/busy lookup, schedule

**Organizer**:
The person who owns a meeting. Only the organizer can update or cancel it.

**Attendee**:
A person invited to a meeting, required or optional. Only an attendee can respond.
_Avoid_: guest, participant
