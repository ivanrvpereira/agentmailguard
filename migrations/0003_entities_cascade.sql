PRAGMA foreign_keys = off;

CREATE TABLE entities_new (
  id            TEXT PRIMARY KEY,
  email_id      TEXT NOT NULL REFERENCES emails(id) ON DELETE CASCADE,
  entity_type   TEXT NOT NULL,
  value         TEXT NOT NULL,
  source_field  TEXT NOT NULL
);

INSERT INTO entities_new (id, email_id, entity_type, value, source_field)
SELECT id, email_id, entity_type, value, source_field
FROM entities;

DROP TABLE entities;
ALTER TABLE entities_new RENAME TO entities;

CREATE INDEX idx_entities_email ON entities(email_id);

PRAGMA foreign_keys = on;
