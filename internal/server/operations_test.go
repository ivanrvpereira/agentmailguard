package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type store struct{ loads atomic.Int32 }

func (s *store) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	s.loads.Add(1)
	return credentials.OAuth{AccessToken: "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"}`)) + ".x", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Username: "synthetic@example.test"}, nil
}
func (*store) SaveOAuth(context.Context, string, string, credentials.OAuth) error { return nil }
func (*store) LoadScreeningKey(context.Context) (string, error) {
	return "", errors.New("private-key-canary")
}
func (*store) SaveScreeningKey(context.Context, string) error { return nil }
func configFile(t *testing.T, home string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n[accounts.other]\nprovider='m365'\nclient_id='other'\ntenant_id='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
}
func rpc(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, e := http.Post(url+"/v1/call", "application/json", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	return resp.StatusCode, strings.TrimSpace(string(b))
}
func TestValidatedAccountOperationsAndReload(t *testing.T) {
	home := t.TempDir()
	configFile(t, home)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/mailFolders/deleteditems":
			io.WriteString(w, `{"id":"deleted"}`)
		case "/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"f1","displayName":"Correspondência 日本","childFolderCount":0}]}`)
		case "/me/outlook/masterCategories":
			io.WriteString(w, `{"value":[{"displayName":"Work"}]}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer api.Close()
	creds := &store{}
	s, e := server.New(context.Background(), home, server.Options{Credentials: creds, M365: m365.Options{BaseURL: api.URL}, Now: func() time.Time { return time.Date(2026, 3, 31, 17, 0, 0, 0, time.UTC) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	for _, op := range []string{"rules.list", "mail.organize"} {
		status, b := rpc(t, h.URL, `{"operation":"`+op+`","params":{}}`)
		if status != 200 {
			t.Fatalf("%s: %d %s", op, status, b)
		}
	}
	if calls.Load() != 0 || creds.loads.Load() != 0 {
		t.Fatal("empty rules accessed provider credentials")
	}
	for _, body := range []string{
		`{"operation":"mail.get","params":{}}`, `{"operation":"mail.get","params":{"id":""}}`,
		`{"operation":"draft.update","params":{"id":"x"}}`, `{"operation":"mail.archive","params":{"ids":[]}}`,
		`{"operation":"mail.list","params":{"count":-1}}`, `{"operation":"mail.list","params":{"page":"a b"}}`, `{"operation":"mail.search","params":{"text":"x","page":"a\u0000"}}`, `{"operation":"calendar.search","params":{"text":"x","page":"\u00e9"}}`, `{"operation":"mail.flag","params":{"ids":["x"],"state":"urgent"}}`,
		`{"operation":"mail.organize","params":{"since":"0d"}}`, `{"operation":"mail.organize","params":{"since":"2026-04-01"}}`,
		`{"operation":"calendar.list","params":{"start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:00:00Z"}}`,
	} {
		status, _ := rpc(t, h.URL, body)
		if status != 400 {
			t.Errorf("invalid request status %d: %s", status, body)
		}
	}
	if calls.Load() != 0 || creds.loads.Load() != 0 {
		t.Fatal("invalid input accessed provider")
	}
	for range 2 {
		status, b := rpc(t, h.URL, `{"operation":"mail.folders","params":{}}`)
		if status != 200 || b != `[{"id":"f1","name":"Correspondência 日本"}]` {
			t.Fatalf("folders %d %s", status, b)
		}
	}
	if creds.loads.Load() != 1 {
		t.Fatal("provider was not reused")
	}
	if e := os.WriteFile(filepath.Join(home, "rules.toml"), []byte("[accounts.work.receipts]\nfolder='f1'\ninstructions='private-instructions-canary'\nmin_age_hours=24\n[accounts.work.missing]\ncategory='Absent'\ninstructions='secret'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	status, b := rpc(t, h.URL, `{"operation":"rules.list","params":{}}`)
	if status != 200 || strings.Contains(b, "instructions") || !strings.Contains(b, `"min_age_hours":24`) || !strings.Contains(b, `"problem":"category not found"`) {
		t.Fatalf("rules %d %s", status, b)
	}
	var obj map[string]any
	if json.Unmarshal([]byte(b), &obj) != nil {
		t.Fatal("invalid output")
	}
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='changed'\ntenant_id='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	status, b = rpc(t, h.URL, `{"operation":"mail.folders","params":{}}`)
	if status != 500 || b != `{"error":"account configuration changed; restart required"}` {
		t.Fatalf("changed identity %d %s", status, b)
	}
}

func TestMoveIntoDeletedItemsIsRefusedWithPublicReason(t *testing.T) {
	home := t.TempDir()
	configFile(t, home)
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/mailFolders/deleteditems":
			io.WriteString(w, `{"id":"del"}`)
		case "/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"f1","displayName":"Finance","childFolderCount":0},{"id":"del","displayName":"Deleted Items","childFolderCount":1}]}`)
		case "/me/mailFolders/del/childFolders":
			io.WriteString(w, `{"value":[{"id":"old","displayName":"Old","childFolderCount":1}]}`)
		case "/me/mailFolders/old/childFolders":
			io.WriteString(w, `{"value":[{"id":"older","displayName":"Older","childFolderCount":0}]}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer api.Close()
	s, e := server.New(context.Background(), home, server.Options{Credentials: &store{}, M365: m365.Options{BaseURL: api.URL}, Now: func() time.Time { return time.Date(2026, 3, 31, 17, 0, 0, 0, time.UTC) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	for _, dest := range []string{"del", "older"} {
		status, b := rpc(t, h.URL, `{"operation":"mail.move","params":{"ids":["m1"],"folder":"`+dest+`"}}`)
		if status != 500 || b != `{"error":"move refused: use mail.trash"}` {
			t.Fatalf("move to %s: %d %s", dest, status, b)
		}
	}
	if writes.Load() != 0 {
		t.Fatal("refused move reached the provider")
	}
	status, b := rpc(t, h.URL, `{"operation":"mail.folders","params":{}}`)
	if status != 200 || b != `[{"id":"f1","name":"Finance"},{"id":"del","name":"Deleted Items"},{"id":"old","name":"Old"},{"id":"older","name":"Older"}]` {
		t.Fatalf("folders %d %s", status, b)
	}
}
