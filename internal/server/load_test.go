package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// consoleNotSet is the console check while the store holds no console password.
const consoleNotSet = "not set"

// The load reports one fixed line per item. An account without a login does not stop the start; its calls fail.
func TestLoadChecksEveryItemAndStartsWithAnAccountThatIsNotLoggedIn(t *testing.T) {
	d := newDaemon(t, daemonSetup{config: "default_account = 'work'\n" + workTable + gmailTable, record: record(map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant)}), jev: newNativeAPI()})
	result, err := d.s.Load(context.Background(), func(context.Context) (string, error) { t.Fatal("prompted with a stored key"); return "", nil })
	want := []server.CredentialCheck{
		{Item: "credentials (file)", Status: "ok"},
		{Item: `account "gmail" (gmail)`, Status: "not logged in; commands for this account fail until amgd account login"},
		{Item: `account "work" (m365)`, Status: "ok"},
		{Item: "screening key", Status: "ok"},
		{Item: "console password", Status: consoleNotSet},
	}
	if err != nil || !reflect.DeepEqual(result.Checks, want) || len(result.Warnings)+len(result.Restart) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if status, body := accountRPC(t, d.s, "account.status", "gmail", `{}`); status != 200 || body != `{"account":"gmail","email":"gmail@account.test","ok":false,"logged_in":false}` {
		t.Fatalf("status: %d %s", status, body)
	}
	if status, body := accountRPC(t, d.s, "mail.folders", "gmail", `{}`); status != 500 || body != `{"error":"operation failed"}` {
		t.Fatalf("mail without login: %d %s", status, body)
	}
}

// A failed credential store stops the load at its first check, with a fixed status and no prompt.
func TestLoadRefusesAFailedCredentialStoreWithAFixedStatus(t *testing.T) {
	for _, tc := range []struct {
		name, config, item, status string
		broken                     func(*fakeBackend)
	}{
		{"denied", "", "credentials (file)", "access denied", func(b *fakeBackend) { b.readErr = errors.New("private-backend-canary") }},
		{"write denied", "", "credentials (file)", "access denied", func(b *fakeBackend) { b.writeErr = errors.New("private-backend-canary") }},
		{"panic", "", "credentials (file)", "access denied", func(b *fakeBackend) { b.panics = true }},
		{"wrong password", "[credentials]\nbackend='file'\n", "credentials (file)", "wrong password or damaged file", func(b *fakeBackend) { b.readErr = credentials.ErrDecrypt }},
		{"password missing", "[credentials]\nbackend='file'\n", "credentials (file)", "password missing", func(b *fakeBackend) { b.readErr = credentials.ErrPasswordMissing }},
		{"unsupported version", "", "credentials (file)", "unsupported version", func(b *fakeBackend) {
			b.data = []byte(`{"version":2,"oauth":{},"openrouter_key":"private-key-canary"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDaemon(t, daemonSetup{config: workConfig + tc.config, record: record(map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant)})})
			tc.broken(d.backend)
			result, err := d.s.Load(context.Background(), func(context.Context) (string, error) { t.Fatal("failed store prompted for a key"); return "", nil })
			if e, ok := errors.AsType[server.LoadError](err); !ok || e.Code != "credentials_failed" || e.Err != nil {
				t.Fatalf("err=%v", err)
			}
			if want := []server.CredentialCheck{{Item: tc.item, Status: tc.status}}; !reflect.DeepEqual(result.Checks, want) {
				t.Fatalf("checks=%v", result.Checks)
			}
			if status, body := accountRPC(t, d.s, "account.list", "", `{}`); status == 200 {
				t.Fatalf("refused load serves calls: %s", body)
			}
		})
	}
}

// Only the first load asks for a missing screening key. The entered key is saved and audited; without a prompt, or
// without an answer, the load stops.
func TestLoadAsksForAMissingScreeningKeyOnce(t *testing.T) {
	noKey := &credentials.Record{Version: 1, OAuth: map[string]credentials.OAuth{}}
	d := newDaemon(t, daemonSetup{config: workConfig, record: noKey})
	result, err := d.s.Load(context.Background(), func(context.Context) (string, error) { return " sk-entered \n", nil })
	if err != nil || !slices.Contains(result.Checks, server.CredentialCheck{Item: "screening key", Status: "saved"}) {
		t.Fatalf("checks=%v err=%v", result.Checks, err)
	}
	if key, err := d.store.LoadScreeningKey(context.Background()); err != nil || key != "sk-entered" {
		t.Fatalf("stored key=%q err=%v", key, err)
	}
	if text, records := auditOf(t, d.home); len(records) != 1 || records[0]["event"] != "screening_api_key_set" || strings.Contains(text, "sk-entered") {
		t.Fatalf("audit=%s", text)
	}
	for name, prompt := range map[string]func(context.Context) (string, error){
		"no prompt": nil,
		"no answer": func(context.Context) (string, error) { return "", errors.New("private-prompt-canary") },
	} {
		t.Run(name, func(t *testing.T) {
			d := newDaemon(t, daemonSetup{config: workConfig, record: noKey})
			want := map[string]string{"no prompt": "missing; run amgd config set screening.api_key", "no answer": "not entered; run amgd config set screening.api_key"}[name]
			result, err := d.s.Load(context.Background(), prompt)
			if e, ok := errors.AsType[server.LoadError](err); !ok || e.Code != "credentials_failed" || !slices.Contains(result.Checks, server.CredentialCheck{Item: "screening key", Status: want}) {
				t.Fatalf("checks=%v err=%v", result.Checks, err)
			}
		})
	}
}

// A reload that is refused keeps the old state and names the refusal in its audit record. Config and rules
// refusals carry the detail for the daemon log.
func TestRefusedReloadKeepsTheOldStateAndIsAudited(t *testing.T) {
	d := startDaemon(t, daemonSetup{config: workConfig + fakeScreen, record: record(nil)})
	_, before := accountRPC(t, d.s, "account.list", "", `{}`)
	refuse := func(code, detail string) {
		t.Helper()
		_, err := d.s.Reload(context.Background())
		e, ok := errors.AsType[server.LoadError](err)
		if !ok || e.Code != code || (detail == "") != (e.Err == nil) || e.Err != nil && !strings.Contains(e.Err.Error(), detail) {
			t.Fatalf("reload=%v; want %s", err, code)
		}
		if _, after := accountRPC(t, d.s, "account.list", "", `{}`); after != before {
			t.Fatalf("%s changed the accounts: %s", code, after)
		}
	}
	d.writeConfig(t, "default_account = 'other'\n"+otherTable+"[screening]\nendpoint='http://jev.example.test/'\n")
	refuse("config_invalid", "config.toml: screening.endpoint: must be an https URL")
	d.writeConfig(t, "default_account = 'other'\n"+otherTable+fakeScreen)
	if err := os.WriteFile(filepath.Join(d.home, "rules.toml"), []byte("[accounts.\""+otherEmail+"\".x]\ncategory=7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refuse("rules_invalid", "rules.toml")
	if err := os.Remove(filepath.Join(d.home, "rules.toml")); err != nil {
		t.Fatal(err)
	}
	d.backend.readErr = errors.New("private-backend-canary")
	refuse("credentials_failed", "")
	d.backend.readErr = nil
	d.reload(t)
	if _, after := accountRPC(t, d.s, "account.list", "", `{}`); after != `[{"account":"other","provider":"m365","email":"other@account.test","default":true}]` {
		t.Fatalf("accounts after the reload: %s", after)
	}
	var results []any
	_, records := auditOf(t, d.home)
	for _, r := range records {
		if r["event"] == "reload" {
			results = append(results, r["result"])
		}
	}
	if want := []any{"config_invalid", "rules_invalid", "credentials_failed", "ok"}; !reflect.DeepEqual(results, want) {
		t.Fatalf("reload records=%v", records)
	}
}

// A reload applies accounts, policy, rules, and logins that amgd saved. The console address and the credential
// backend keep their values until a restart, and the result names them.
func TestReloadAppliesChangesButRestartKeysWait(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(newNativeAPI().graph))
	defer graph.Close()
	d := startDaemon(t, daemonSetup{config: workConfig + fakeScreen + draftApproveOnly, record: record(nil), options: server.Options{M365: m365Options(graph.URL)}})
	d.writeConfig(t, workConfig+fakeScreen+"[policy]\nmail_draft='deny'\n[console]\nlisten='127.0.0.1:9000'\n[credentials]\nbackend='keychain'\n")
	if err := os.WriteFile(filepath.Join(d.home, "rules.toml"), []byte("[accounts.\""+otherEmail+"\".receipts]\ncategory='Receipts'\ninstructions='private-rule-canary'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.store.SaveOAuth(context.Background(), "m365", otherEmail, m365Login(m365Grant)); err != nil {
		t.Fatal(err)
	}
	result := d.reload(t)
	if !reflect.DeepEqual(result.Restart, []string{"console.listen", "credentials.backend"}) || result.Checks[0] != (server.CredentialCheck{Item: "credentials (file)", Status: "ok"}) {
		t.Fatalf("result=%+v", result)
	}
	if d.s.ConsoleAddress() != "localhost:8788" {
		t.Fatalf("console address=%s", d.s.ConsoleAddress())
	}
	if status, body := accountRPC(t, d.s, "draft.new", "work", `{"to":["a@example.test"],"subject":"s","body":"b"}`); status != 500 || body != `{"error":"refused by policy: mail_draft"}` {
		t.Fatalf("policy: %d %s", status, body)
	}
	if status, body := accountRPC(t, d.s, "account.status", "other", `{}`); status != 200 || !strings.Contains(body, `"logged_in":true`) {
		t.Fatalf("login: %d %s", status, body)
	}
	if status, body := accountRPC(t, d.s, "rules.list", "other", `{}`); status != 200 || !strings.Contains(body, `"rule":"receipts"`) || strings.Contains(body, "private-") {
		t.Fatalf("rules: %d %s", status, body)
	}
}

// A token refresh saves the new login through the store. A login that amgd saved since the snapshot wins: the
// refresh never overwrites it, and the next refresh uses it.
func TestRefreshNeverOverwritesALoginSavedSinceTheLoad(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func() { mu.Lock(); defer mu.Unlock(); now = now.Add(2 * time.Hour) }
	var refreshes []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		refreshes = append(refreshes, r.Form.Get("refresh_token"))
		next := "synthetic-refresh-" + string(rune('0'+len(refreshes)))
		mu.Unlock()
		response(w, map[string]any{"access_token": statusJWT(m365Grant), "refresh_token": next, "expires_in": 3600})
	}))
	defer api.Close()
	expired := func(refresh string) credentials.OAuth {
		return credentials.OAuth{AccessToken: statusJWT(m365Grant), RefreshToken: refresh, ExpiresAt: clock().Add(-time.Second)}
	}
	d := startDaemon(t, daemonSetup{config: workConfig + fakeScreen, record: record(map[string]credentials.OAuth{"m365/" + workEmail: expired("synthetic-refresh-0")}),
		options: server.Options{M365: m365Options(api.URL), Now: clock}})
	stored := func() string {
		t.Helper()
		v, err := d.store.LoadOAuth(context.Background(), "m365", workEmail)
		if err != nil {
			t.Fatal(err)
		}
		return v.RefreshToken
	}
	// The sign-in check of the load refreshed the expired login.
	if got := stored(); got != "synthetic-refresh-1" {
		t.Fatalf("stored after the load=%s", got)
	}
	if err := d.store.SaveOAuth(context.Background(), "m365", workEmail, expired("synthetic-amgd-login")); err != nil {
		t.Fatal(err)
	}
	status := func() {
		t.Helper()
		advance()
		if status, body := accountRPC(t, d.s, "account.status", "work", `{}`); status != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("status: %d %s", status, body)
		}
	}
	status()
	if got := stored(); got != "synthetic-amgd-login" {
		t.Fatalf("refresh overwrote the newer login: %s", got)
	}
	status()
	if got := stored(); got != "synthetic-refresh-3" {
		t.Fatalf("refresh of the newer login not saved: %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"synthetic-refresh-0", "synthetic-refresh-1", "synthetic-amgd-login"}; !reflect.DeepEqual(refreshes, want) {
		t.Fatalf("refreshes=%v", refreshes)
	}
}
