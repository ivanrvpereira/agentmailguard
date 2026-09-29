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
  PRIMARY KEY (account, message_id)
);
CREATE INDEX IF NOT EXISTS mail_cache_received_at ON mail_cache (account, received_at);
