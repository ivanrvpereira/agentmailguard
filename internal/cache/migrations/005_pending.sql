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
