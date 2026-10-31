-- Adds the final state withdrawn: a waiting draft send that a draft change or delete withdrew. SQLite cannot change
-- a CHECK constraint in place, so the table is rebuilt with the same columns, rows, and row order.
CREATE TABLE pending_withdrawn (
  id TEXT PRIMARY KEY NOT NULL,
  account TEXT NOT NULL,
  op TEXT NOT NULL,
  args TEXT NOT NULL,
  jev TEXT,
  state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'done', 'denied', 'failed', 'withdrawn')),
  result TEXT,
  created TEXT NOT NULL,
  updated TEXT NOT NULL,
  draft TEXT
);
INSERT INTO pending_withdrawn (rowid, id, account, op, args, jev, state, result, created, updated, draft)
  SELECT rowid, id, account, op, args, jev, state, result, created, updated, draft FROM pending;
DROP TABLE pending;
ALTER TABLE pending_withdrawn RENAME TO pending;
