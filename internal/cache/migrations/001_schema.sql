-- The account column holds the account key: the account's email in lowercase.
-- mail_cache holds security metadata and explicit organization answers, never mail content.
CREATE TABLE IF NOT EXISTS mail_cache (
  account TEXT NOT NULL,
  message_id TEXT NOT NULL,
  received_at TEXT NOT NULL,
  score REAL CHECK (score BETWEEN 0 AND 1),
  verdict TEXT NOT NULL CHECK (verdict IN ('green', 'yellow', 'sealed', 'red', 'error')),
  error TEXT,
  spf TEXT,
  dkim TEXT,
  dmarc TEXT,
  compauth TEXT,
  compauth_reason TEXT,
  screened_at TEXT NOT NULL,
  organization TEXT,
  organization_applied_at TEXT,
  security_applied_verdict TEXT CHECK (security_applied_verdict IN ('yellow', 'sealed', 'red')),
  PRIMARY KEY (account, message_id)
);
CREATE INDEX IF NOT EXISTS mail_cache_received_at ON mail_cache (account, received_at);
-- pending holds write actions that wait for approval. args can contain message content.
CREATE TABLE IF NOT EXISTS pending (
  id TEXT PRIMARY KEY NOT NULL,
  account TEXT NOT NULL,
  op TEXT NOT NULL,
  args TEXT NOT NULL,
  jev TEXT,
  state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'done', 'denied', 'failed')),
  result TEXT,
  created TEXT NOT NULL,
  updated TEXT NOT NULL
);
