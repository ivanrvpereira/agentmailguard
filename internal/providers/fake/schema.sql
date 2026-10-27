-- Schema of fake-provider.db, the mailbox and calendar of accounts with provider = "fake".
-- The server applies it when it first opens fake-provider.db. To seed by hand: sqlite3 fake-provider.db < schema.sql
-- Every row belongs to the account named in its account column: the account's email from config.toml, in lowercase.
-- Lists are JSON arrays in TEXT columns. Times are RFC 3339 text, for example 2026-04-01T09:30:00Z.
-- All mail and event text is untrusted, as with real providers.

-- The account's own address is its email: the sender of its drafts and sends, and its attendee row.

-- folders: the server adds inbox, drafts, sent, archive, and trash for each account.
-- parent is the folder ID of the parent folder, or NULL. trash and every folder below it are the trash.
CREATE TABLE IF NOT EXISTS folders (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	parent TEXT,
	PRIMARY KEY (account, id)
);

-- categories is the category list of each account.
CREATE TABLE IF NOT EXISTS categories (
	account TEXT NOT NULL,
	name TEXT NOT NULL,
	PRIMARY KEY (account, name)
);

-- messages holds all mail. Nothing is ever deleted: trash and archive are moves to those folders.
-- received_at is the time of the insert unless the row gives one.
-- to_addresses, cc_addresses, and bcc_addresses: JSON arrays of addresses, for example ["ana@example.test"].
-- content_type: text keeps the body as plain text with its line breaks; html marks the body as HTML source.
-- categories: JSON array of category names. attachments: JSON array of file names.
-- spf, dkim, dmarc, compauth: authentication results such as pass or fail; compauth_reason: digits.
-- origin and source_id: how the server made the message (new, reply, or forward) and the source message ID.
-- They are NULL for seeded mail. Any draft can be updated, whatever its origin.
-- thread_id: messages with the same thread_id are one conversation. NULL means the message is its own
-- conversation: its thread ID is its message ID. Replies and forwards join the conversation of their source.
CREATE TABLE IF NOT EXISTS messages (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	folder TEXT NOT NULL DEFAULT 'inbox',
	sender_name TEXT NOT NULL DEFAULT '',
	sender_address TEXT NOT NULL DEFAULT '',
	to_addresses TEXT NOT NULL DEFAULT '[]',
	cc_addresses TEXT NOT NULL DEFAULT '[]',
	bcc_addresses TEXT NOT NULL DEFAULT '[]',
	subject TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	content_type TEXT NOT NULL DEFAULT 'text' CHECK (content_type IN ('text', 'html')),
	received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	is_draft INTEGER NOT NULL DEFAULT 0 CHECK (is_draft IN (0, 1)),
	is_read INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
	flag TEXT NOT NULL DEFAULT 'none' CHECK (flag IN ('none', 'flagged', 'complete')),
	categories TEXT NOT NULL DEFAULT '[]',
	attachments TEXT NOT NULL DEFAULT '[]',
	spf TEXT,
	dkim TEXT,
	dmarc TEXT,
	compauth TEXT,
	compauth_reason TEXT,
	origin TEXT CHECK (origin IN ('new', 'reply', 'forward')),
	source_id TEXT,
	thread_id TEXT,
	PRIMARY KEY (account, id)
);

-- events is the calendar. An event is never deleted: a cancel sets is_cancelled and cancel_comment.
-- An empty organizer_address means that the account organizes the event. The account also organizes an
-- event whose organizer_address is its own address (ignoring case). Only the organizer can update or
-- cancel; only other accounts' events get a response.
-- recurrence: NULL for a single event, or a pattern such as weekly. Recurring events cannot be changed.
-- join_url: the online meeting link, or NULL. show_as: free, tentative, busy, oof, workingElsewhere, or unknown.
-- categories: JSON array of category names. attachments: JSON array of file names.
CREATE TABLE IF NOT EXISTS events (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	subject TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	content_type TEXT NOT NULL DEFAULT 'text' CHECK (content_type IN ('text', 'html')),
	location TEXT NOT NULL DEFAULT '',
	start_at TEXT NOT NULL,
	end_at TEXT NOT NULL,
	is_all_day INTEGER NOT NULL DEFAULT 0 CHECK (is_all_day IN (0, 1)),
	recurrence TEXT,
	organizer_name TEXT NOT NULL DEFAULT '',
	organizer_address TEXT NOT NULL DEFAULT '',
	join_url TEXT,
	is_private INTEGER NOT NULL DEFAULT 0 CHECK (is_private IN (0, 1)),
	show_as TEXT NOT NULL DEFAULT 'busy' CHECK (show_as IN ('free', 'tentative', 'busy', 'oof', 'workingElsewhere', 'unknown')),
	categories TEXT NOT NULL DEFAULT '[]',
	attachments TEXT NOT NULL DEFAULT '[]',
	is_cancelled INTEGER NOT NULL DEFAULT 0 CHECK (is_cancelled IN (0, 1)),
	cancel_comment TEXT,
	PRIMARY KEY (account, id)
);

-- attendees lists the attendees of each event. address is compared without case.
-- response: none, organizer, tentativelyAccepted, accepted, declined, or notResponded, as in Microsoft 365.
-- comment: the comment of the account's own response, or NULL.
-- optional: 1 for an optional attendee, 0 for a required one. Optional attendees are invited too.
CREATE TABLE IF NOT EXISTS attendees (
	account TEXT NOT NULL,
	event_id TEXT NOT NULL,
	address TEXT NOT NULL COLLATE NOCASE,
	name TEXT NOT NULL DEFAULT '',
	response TEXT NOT NULL DEFAULT 'none' CHECK (response IN ('none', 'organizer', 'tentativelyAccepted', 'accepted', 'declined', 'notResponded')),
	comment TEXT,
	optional INTEGER NOT NULL DEFAULT 0 CHECK (optional IN (0, 1)),
	PRIMARY KEY (account, event_id, address)
);

-- free_busy is the only source of calendar availability; events do not count. address is compared without
-- case. An address without rows is unknown, and so is an address with a row whose times cannot be read or
-- whose end_at is not after its start_at. Otherwise each 30-minute slot gets the strongest status of the
-- rows that overlap it (out_of_office, then busy, then tentative, then free), and a slot without rows is free.
CREATE TABLE IF NOT EXISTS free_busy (
	account TEXT NOT NULL,
	address TEXT NOT NULL COLLATE NOCASE,
	start_at TEXT NOT NULL,
	end_at TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('free', 'tentative', 'busy', 'out_of_office'))
);
