package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// fakeHome writes a config with the fake account demo and seeds <home>/fake.db with the inbox message g,
// which the fake Jev screens green.
func fakeHome(t *testing.T, policy string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.demo]\nprovider='fake'\n"+policy), 0600); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../providers/fake/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	db := fakeDB(t, home)
	for _, s := range []string{string(schema), `INSERT INTO messages (account, id, sender_address, subject, body, received_at, spf, dkim, dmarc) VALUES ('demo', 'g', 'sender@example.test', 'private-subject-g', 'private-body-g', '2026-10-06T09:00:00Z', 'pass', 'pass', 'pass')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return home
}
func fakeDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(home, "fake.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeServer serves home with a credential store that records every access.
func fakeServer(t *testing.T, home string) (*server.Server, *preloadStore) {
	t.Helper()
	creds := &preloadStore{key: "private-jev-canary", records: map[string]credentials.OAuth{}}
	s, err := server.New(context.Background(), home, server.Options{Credentials: creds, Screener: newNativeAPI().screener(t), LookupEnv: func(string) string { return "" }, Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, creds
}

func sentSubjects(t *testing.T, home string) []string {
	t.Helper()
	rows, err := fakeDB(t, home).Query(`SELECT subject FROM messages WHERE account = 'demo' AND folder = 'sent'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestFakeAccountReadsScreenedMailAndSendsIntoSent(t *testing.T) {
	home := fakeHome(t, sendAllow)
	s, creds := fakeServer(t, home)
	status, body := accountRPC(t, s, "mail.list", "demo", `{}`)
	var list []struct{ ID, Verdict string }
	if status != 200 || json.Unmarshal([]byte(body), &list) != nil || !reflect.DeepEqual(list, []struct{ ID, Verdict string }{{"g", "green"}}) {
		t.Fatalf("list: %d %s", status, body)
	}
	if status, body := accountRPC(t, s, "mail.send", "demo", `{"to":["ana@example.test"],"subject":"Hello","body":"hi"}`); status != 200 || body != `{"ok":true}` {
		t.Fatalf("send: %d %s", status, body)
	}
	if got := sentSubjects(t, home); !reflect.DeepEqual(got, []string{"Hello"}) {
		t.Fatalf("sent=%v", got)
	}
	if reads, writes := creds.accesses(); len(reads)+len(writes) != 0 {
		t.Fatalf("credential reads=%v writes=%v", reads, writes)
	}
}

func TestFakeAccountSendIsRefusedByTheDefaultPolicy(t *testing.T) {
	home := fakeHome(t, "")
	s, _ := fakeServer(t, home)
	if status, body := accountRPC(t, s, "mail.send", "demo", `{"to":["ana@example.test"],"subject":"Hello","body":"hi"}`); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
		t.Fatalf("send: %d %s", status, body)
	}
	if got := sentSubjects(t, home); len(got) != 0 {
		t.Fatalf("sent=%v", got)
	}
}

func TestFakeAccountPreloadsAndReportsStatusWithoutCredentials(t *testing.T) {
	s, creds := fakeServer(t, fakeHome(t, ""))
	checks, err := s.Preload(context.Background(), nil)
	if err != nil || !slices.Contains(checks, server.CredentialCheck{Item: `account "demo" (fake)`, Status: "no login needed"}) {
		t.Fatalf("checks=%v err=%v", checks, err)
	}
	if reads, writes := creds.accesses(); !reflect.DeepEqual(reads, []string{"jev"}) || len(writes) != 0 {
		t.Fatalf("credential reads=%v writes=%v", reads, writes)
	}
	if status, body := accountRPC(t, s, "auth.status", "demo", `{}`); status != 200 || body != `{"account":"demo","ok":true,"logged_in":true,"username":"demo@fake.test","scopes":[]}` {
		t.Fatalf("status: %d %s", status, body)
	}
}
