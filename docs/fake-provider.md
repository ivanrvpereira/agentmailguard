# Fake provider

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

All fake accounts share `fake-provider.db` in the daemon home, for example `~/.agentmailguard/fake-provider.db`. Each row has an `account` column with the account key: the email of the account in lowercase. The daemon creates the file with mode 0600 and applies the schema in [`internal/providers/fake/schema.sql`](../internal/providers/fake/schema.sql) when it first opens the file. The schema file explains each table and column. Each account gets the folders `inbox`, `drafts`, `sent`, `archive`, `trash`, and `spam`. The address of the account is its email.

The daemon reads and writes `fake-provider.db` on every call, so you can seed and inspect it with `sqlite3` while the daemon runs:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db < internal/providers/fake/schema.sql
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, to_addresses, subject, body, received_at, spf, dkim, dmarc) VALUES ('demo@fake.invalid', 'm1', 'Ana', 'ana@example.test', '[\"demo@fake.invalid\"]', 'Lunch on Friday?', 'Are you free at noon?', '2026-04-01T09:30:00Z', 'pass', 'pass', 'pass');"
sqlite3 ~/.agentmailguard/fake-provider.db "SELECT id, folder, subject, origin FROM messages WHERE account = 'demo@fake.invalid';"
```

Trash, spam, and archive are moves to the `trash`, `spam`, and `archive` folders. Only `mail trash` and `mail spam` put mail in `trash` and `spam`, under `mail_trash` and `mail_spam`; ordinary moves into either folder, or a folder below one through its `parent` chain, are refused. Only `draft delete` deletes a row, the draft's. Drafts go to `drafts`; `draft send` moves the draft to `sent` with `is_draft` 0 and the send time, and it keeps its ID. `mail send`, `mail reply`, and `mail forward` store the message in `sent`, with `origin` (`new`, `reply`, or `forward`) and `source_id`. Nothing leaves the machine. New drafts, replies, and sends keep their To, Cc, and Bcc lists in `to_addresses`, `cc_addresses`, and `bcc_addresses`, and `content_type` is `html` for an `--html` body. A reply picks its recipients by the Gmail rule (see [Mail send](sending.md)); the fake has no Reply-To, and a message in `sent` is one that the account sent. A plain-text reply body is the agent's text; an `--html` reply body is the agent's HTML with the original body below it in a `<blockquote>`. A forward goes to its `--to`, `--cc`, and `--bcc`, has the subject `Fw: ` and the original subject, and carries the original's `attachments`; its body follows the same rule as a reply, with the note as the agent's text. A draft update changes only the given columns of any row with `is_draft` 1, whatever its `origin`, and keeps its ID; a message that is not a draft is refused. A `fake-provider.db` made before the `bcc_addresses` column existed gets the column when the daemon first opens it.

To seed a conversation, give its messages the same `thread_id`. A message whose `thread_id` is NULL is its own conversation, and its `thread_id` in results is its message ID. Reply and forward drafts, and sent replies and forwards, join the conversation of their source message; new drafts and new sends are their own conversation. A `fake-provider.db` made before the `thread_id` column existed gets the column when the daemon first opens it. For example, a reply from Ana in the conversation of `m1`:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO messages (account, id, sender_name, sender_address, subject, body, thread_id) VALUES ('demo@fake.invalid', 'm2', 'Ana', 'ana@example.test', 'Re: Lunch on Friday?', 'Noon works.', 'm1');"
```

The calendar uses three tables. `events` holds the events, `attendees` holds the attendees of each event and their responses, and `free_busy` holds the free/busy times that `calendar availability` reads. The account organizes an event when its `organizer_address` is empty or is the account address; only then can it update or cancel the event. It can respond only to the other events. Availability comes only from `free_busy`, never from `events`. An address without `free_busy` rows is `unknown`, and a slot that no row covers is `free`. For example, an event that Ana organizes with the demo account as attendee, and Ana's busy time:

```sh
sqlite3 ~/.agentmailguard/fake-provider.db "INSERT INTO events (account, id, subject, start_at, end_at, organizer_name, organizer_address) VALUES ('demo@fake.invalid', 'e1', 'Planning', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'Ana', 'ana@example.test'); INSERT INTO attendees (account, event_id, address, name) VALUES ('demo@fake.invalid', 'e1', 'demo@fake.invalid', 'Demo'); INSERT INTO free_busy (account, address, start_at, end_at, status) VALUES ('demo@fake.invalid', 'ana@example.test', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'busy');"
```

`calendar invite`, `update`, `cancel`, and `respond` change only `fake-provider.db`, under the write policy as for other accounts. No notification leaves the machine. A cancel sets `is_cancelled` and `cancel_comment` and deletes nothing. A response sets the account's own row in `attendees`; `--no-notify` changes nothing else. An invite stores `--optional` attendees with `optional` set to 1 in `attendees`. An event `body` is HTML (`content_type` `html`): an `--html` body as given, plain text escaped. A `fake-provider.db` made before the `optional` column existed gets the column, with 0 for every existing attendee, when the daemon first opens it.

Fake mail is untrusted like real mail. The configured [screening](screening.md#screening) engine screens it. With the fake screening engine, the daemon needs no screening key.
