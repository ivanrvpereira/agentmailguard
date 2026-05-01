CREATE TABLE emails (
  id            TEXT PRIMARY KEY,
  message_id    TEXT UNIQUE,
  received_at   TEXT NOT NULL,
  sender_addr   TEXT NOT NULL,
  sender_name   TEXT,
  subject       TEXT,
  recipients    TEXT NOT NULL,
  cleaned_body  TEXT,
  has_attachments INTEGER DEFAULT 0,

  risk_level    TEXT NOT NULL,
  risk_reasons  TEXT NOT NULL,
  labels        TEXT,
  threat_flags  TEXT,

  spf_result    TEXT,
  dkim_result   TEXT,
  dmarc_result  TEXT,

  embedding_id  TEXT
);

CREATE TABLE entities (
  id            TEXT PRIMARY KEY,
  email_id      TEXT NOT NULL REFERENCES emails(id),
  entity_type   TEXT NOT NULL,
  value         TEXT NOT NULL,
  source_field  TEXT NOT NULL
);

CREATE INDEX idx_emails_received ON emails(received_at DESC);
CREATE INDEX idx_emails_sender ON emails(sender_addr);
CREATE INDEX idx_emails_risk ON emails(risk_level);
CREATE INDEX idx_entities_email ON entities(email_id);
