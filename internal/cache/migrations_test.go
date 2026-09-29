package cache_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

// SQL here creates input fixtures for the public Open API. Assertions use the cache
// API, except byte equality for the external contract that failed opens change no data.
const legacy = `CREATE TABLE mail_cache (
 account TEXT NOT NULL, message_id TEXT NOT NULL, received_at TEXT NOT NULL,
 score REAL CHECK (score BETWEEN 0 AND 1),
 verdict TEXT NOT NULL CHECK (verdict IN ('green','yellow','sealed','red','error')),
 error TEXT, spf TEXT, dkim TEXT, dmarc TEXT, compauth TEXT, compauth_reason TEXT,
 screened_at TEXT NOT NULL, PRIMARY KEY (account, message_id));
 CREATE INDEX mail_cache_received_at ON mail_cache (account, received_at);`
const v2 = `ALTER TABLE mail_cache ADD COLUMN organization TEXT; ALTER TABLE mail_cache ADD COLUMN applied_at TEXT;`
const v3 = `ALTER TABLE mail_cache ADD COLUMN tagged_verdict TEXT CHECK (tagged_verdict IN ('yellow','sealed','red'));`
const v4 = `ALTER TABLE mail_cache RENAME COLUMN applied_at TO organization_applied_at; ALTER TABLE mail_cache RENAME COLUMN tagged_verdict TO security_applied_verdict;`

func fixture(t *testing.T, version int, schema, after string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlText := schema + `INSERT INTO mail_cache (account,message_id,received_at,score,verdict,spf,dkim,dmarc,compauth,compauth_reason,screened_at)
 VALUES ('work','m1','2026-09-25T08:30:00+00:00',0.05,'green','pass','pass','fail','fail','000','2026-09-25T09:00:00+00:00');` + after + fmt.Sprintf("PRAGMA user_version = %d;", version)
	if _, err := db.Exec(sqlText); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestAllFourMigrationsPreserveRowsAndMarkers(t *testing.T) {
	for version := 0; version <= 4; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			schema, after := legacy, ""
			want := row("m1")
			if version >= 2 {
				schema += v2
				after = `UPDATE mail_cache SET organization='{}', applied_at='2026-09-25T08:30:00+00:00';`
				want.Organization = &model.Organization{}
				want.OrganizationAppliedAt = &received
			}
			if version >= 3 {
				schema += v3
				after += `UPDATE mail_cache SET tagged_verdict='yellow';`
				want.SecurityAppliedVerdict = ptr(model.Yellow)
			}
			if version >= 4 {
				schema += v4
				after = strings.ReplaceAll(strings.ReplaceAll(after, "applied_at", "organization_applied_at"), "tagged_verdict", "security_applied_verdict")
			}
			path := fixture(t, version, schema, after)
			c, err := cache.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Get(ctx, "work", []string{"m1"})
			if err != nil || !reflect.DeepEqual(got["m1"], want) {
				t.Fatalf("lost state: %+v %v", got, err)
			}
			if err := c.MarkSecurityApplied(ctx, "work", "m1", model.Red); err != nil {
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
			got, err = c.Get(ctx, "work", []string{"m1"})
			if err != nil || !reflect.DeepEqual(got["m1"], want) {
				t.Fatalf("reopen changed state: %+v %v", got, err)
			}
		})
	}
}
func TestInvalidStoredAnswersFailClosedOnRead(t *testing.T) {
	for _, organization := range []string{
		`{"folder":{"choice":"stay","confidence":1,"probabilities":{"stay":0.2}}}`,
		`{"categories":{"RAW CONTENT CANARY":0.9}}`,
		`{} {"raw":"RAW CONTENT CANARY"}`,
	} {
		path := fixture(t, 4, legacy+v2+v3+v4, `UPDATE mail_cache SET organization='`+organization+`';`)
		c, err := cache.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "work", []string{"m1"})
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
		{"newer legacy", 5, legacy}, {"newer current", 5, legacy + v2 + v3 + v4},
		{"negative version", -1, legacy},
		{"second statement fails", 0, legacy + `ALTER TABLE mail_cache ADD COLUMN applied_at TEXT;`},
		{"claimed current missing columns", 4, legacy},
		{"pending wrong upsert key", 1, strings.ReplaceAll(legacy, ", PRIMARY KEY (account, message_id)", "")},
		{"current wrong upsert key", 4, strings.ReplaceAll(legacy, ", PRIMARY KEY (account, message_id)", ", UNIQUE (account, message_id, verdict)") + v2 + v3 + v4},
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
