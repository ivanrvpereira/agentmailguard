package cache_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// SQL here creates input fixtures for the public Open API. Assertions use the cache
// API, except byte equality for the external contract that failed opens change no data.
// mailCache and pending are the tables of 001_schema.sql, written out as a database at version 1 has them.
const mailCache = `CREATE TABLE mail_cache (
 account TEXT NOT NULL, message_id TEXT NOT NULL, received_at TEXT NOT NULL,
 score REAL CHECK (score BETWEEN 0 AND 1),
 verdict TEXT NOT NULL CHECK (verdict IN ('green','yellow','sealed','red','error')),
 error TEXT, spf TEXT, dkim TEXT, dmarc TEXT, compauth TEXT, compauth_reason TEXT,
 screened_at TEXT NOT NULL, organization TEXT, organization_applied_at TEXT,
 security_applied_verdict TEXT CHECK (security_applied_verdict IN ('yellow','sealed','red')), PRIMARY KEY (account, message_id));
 CREATE INDEX mail_cache_received_at ON mail_cache (account, received_at);`
const pendingTable = `CREATE TABLE pending (id TEXT PRIMARY KEY NOT NULL, account TEXT NOT NULL, op TEXT NOT NULL, args TEXT NOT NULL, jev TEXT,
 state TEXT NOT NULL CHECK (state IN ('pending','running','done','denied','failed')), result TEXT, created TEXT NOT NULL, updated TEXT NOT NULL);`

// draftColumn is what 002_pending_draft.sql adds: a database at version 2 has pendingTable and this column.
const draftColumn = `ALTER TABLE pending ADD COLUMN draft TEXT;`

func fixture(t *testing.T, version int, schema, after string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlText := schema + `INSERT INTO mail_cache (account,message_id,received_at,score,verdict,spf,dkim,dmarc,compauth,compauth_reason,screened_at)
 VALUES ('work@example.test','m1','2026-09-25T08:30:00+00:00',0.05,'green','pass','pass','fail','fail','000','2026-09-25T09:00:00+00:00');` + after + fmt.Sprintf("PRAGMA user_version = %d;", version)
	if _, err := db.Exec(sqlText); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestNewDatabaseGetsTheSchemaAndKeepsRowsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := row("m1")
	if err := c.Put(ctx, "work@example.test", want); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkSecurityApplied(ctx, "work@example.test", "m1", model.Red); err != nil {
		t.Fatal(err)
	}
	id, err := c.AddPending(ctx, "work@example.test", "mail.trash", []byte(`{}`), nil, nil, received)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want.SecurityAppliedVerdict = ptr(model.Red)
	got, err := c.Get(ctx, "work@example.test", []string{"m1"})
	if err != nil || !reflect.DeepEqual(got["m1"], want) {
		t.Fatalf("reopen changed state: %+v %v", got, err)
	}
	if p, err := c.GetPending(ctx, id); err != nil || p.State != cache.StatePending || p.Account != "work@example.test" {
		t.Fatalf("pending after reopen: %+v %v", p, err)
	}
}

// A cache at version 1 gains the draft column of the pending table. A stored action keeps its args and has no
// draft fields, and a new draft send stores them.
func TestVersionOneCacheGainsThePendingDraftColumn(t *testing.T) {
	path := fixture(t, 1, mailCache+pendingTable, `INSERT INTO pending VALUES ('0123456789abcdef0123456789abcdef','work@example.test','mail.send','{"subject":"CANARY"}',NULL,'pending',NULL,'2026-09-25T10:00:00Z','2026-09-25T10:00:00Z');`)
	c, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p, err := c.GetPending(ctx, "0123456789abcdef0123456789abcdef"); err != nil || string(p.Args) != `{"subject":"CANARY"}` || p.Draft != nil || p.State != cache.StatePending {
		t.Fatalf("stored action after migration: %+v %v", p, err)
	}
	id, err := c.AddPending(ctx, "work@example.test", "draft.send", []byte(`{"id":"d1"}`), []byte(`{"subject":"CANARY"}`), nil, received)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := c.GetPending(ctx, id); err != nil || string(p.Draft) != `{"subject":"CANARY"}` {
		t.Fatalf("draft send after migration: %+v %v", p, err)
	}
}

func TestInvalidStoredAnswersFailClosedOnRead(t *testing.T) {
	for _, organization := range []string{
		`{"folder":{"choice":"stay","confidence":1,"probabilities":{"stay":0.2}}}`,
		`{"categories":{"RAW CONTENT CANARY":0.9}}`,
		`{} {"raw":"RAW CONTENT CANARY"}`,
	} {
		path := fixture(t, 1, mailCache+pendingTable, `UPDATE mail_cache SET organization='`+organization+`';`)
		c, err := cache.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "work@example.test", []string{"m1"})
		c.Close()
		if err == nil || len(got) != 0 {
			t.Error("invalid stored answers returned as usable classification")
		}
	}
}

func TestRejectedMigrationsLeaveDatabaseUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version int
		schema  string
	}{
		// A home from before the email account key has a cache at version 5. It is refused, not changed.
		{"old program's cache", 5, mailCache + pendingTable},
		{"newer than this program", 3, mailCache + pendingTable + draftColumn},
		{"claimed current missing pending table", 2, mailCache},
		{"claimed current missing draft column", 2, mailCache + pendingTable},
		{"negative version", -1, mailCache},
		{"claimed current missing columns", 2, strings.ReplaceAll(mailCache, ", organization TEXT, organization_applied_at TEXT", "") + pendingTable + draftColumn},
		{"current wrong upsert key", 2, strings.ReplaceAll(mailCache, ", PRIMARY KEY (account, message_id)", ", UNIQUE (account, message_id, verdict)") + pendingTable + draftColumn},
		{"version 1 missing pending table", 1, mailCache},
		{"script fails on an existing table", 0, strings.ReplaceAll(mailCache, "score REAL", "score TEXT, verdict2 TEXT, score2 REAL") + "CREATE TABLE pending (x TEXT);"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := fixture(t, tt.version, tt.schema, "")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := cache.Open(ctx, path)
			if err == nil {
				c.Close()
				t.Fatal("incompatible database accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed migration modified database")
			}
		})
	}
}
