-- Schema of fake.db, the mailbox of accounts with provider = "fake".
-- The server applies it when it first opens fake.db. To seed by hand: sqlite3 fake.db < schema.sql
-- Every row belongs to the account named in its account column (the name of [accounts.NAME]).
-- Lists are JSON arrays in TEXT columns. Times are RFC 3339 text, for example 2026-04-01T09:30:00Z.
-- All mail text is untrusted, as with real providers.

-- identities gives an account its own address. Without a row the address is <account>@fake.test.
CREATE TABLE IF NOT EXISTS identities (
	account TEXT PRIMARY KEY,
	address TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT ''
);

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
-- to_addresses and cc_addresses: JSON arrays of addresses, for example ["ana@example.test"].
-- categories: JSON array of category names. attachments: JSON array of file names.
-- spf, dkim, dmarc, compauth: authentication results such as pass or fail; compauth_reason: digits.
-- origin and source_id: how the server made the message (new, reply, or forward) and the source message ID.
-- They are NULL for seeded mail. Only drafts with origin new can be updated.
CREATE TABLE IF NOT EXISTS messages (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	folder TEXT NOT NULL DEFAULT 'inbox',
	sender_name TEXT NOT NULL DEFAULT '',
	sender_address TEXT NOT NULL DEFAULT '',
	to_addresses TEXT NOT NULL DEFAULT '[]',
	cc_addresses TEXT NOT NULL DEFAULT '[]',
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
	PRIMARY KEY (account, id)
);
