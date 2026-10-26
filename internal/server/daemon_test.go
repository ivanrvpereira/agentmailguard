package server_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// The account keys of the tests. Their domain is not example.test, the domain of mail text, so a check for leaked
// recipients does not match the account keys that audit records carry.
const (
	workEmail  = "work@account.test"
	otherEmail = "other@account.test"
	gmailEmail = "gmail@account.test"
	demoEmail  = "demo@account.test"
)

const (
	workTable  = "[accounts.work]\nprovider='m365'\nemail='" + workEmail + "'\nclient_id='synthetic'\ntenant_id='synthetic'\n"
	otherTable = "[accounts.other]\nprovider='m365'\nemail='" + otherEmail + "'\nclient_id='other'\ntenant_id='synthetic'\n"
	gmailTable = "[accounts.gmail]\nprovider='gmail'\nemail='" + gmailEmail + "'\nclient_id='synthetic'\nclient_secret='private-config-canary'\n"
	demoTable  = "[accounts.demo]\nprovider='fake'\nemail='" + demoEmail + "'\n"
	// workConfig has the M365 accounts work, the default, and other.
	workConfig  = "default_account = 'work'\n" + workTable + otherTable
	gmailConfig = "default_account = 'gmail'\n" + gmailTable
	demoConfig  = "default_account = 'demo'\n" + demoTable
	fakeScreen  = "[screening]\nprovider='fake'\n"
)

// m365Grant is the grant of an M365 login without write scopes; writeGrant adds sends and meeting writes.
const (
	m365Grant  = "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
	writeGrant = "Mail.ReadWrite Mail.Send Calendars.ReadWrite"
)

// The Gmail grants: gmailReadGrant is that of logins made before calendar writes.
const (
	gmailReadGrant  = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"
	gmailWriteGrant = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned"
)

var farFuture = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// statusJWT is an M365 access token whose only claim is the scopes.
func statusJWT(scopes string) string {
	b, _ := json.Marshal(map[string]string{"scp": scopes})
	return "x." + base64.RawURLEncoding.EncodeToString(b) + ".x"
}

func m365Login(scopes string) credentials.OAuth {
	return credentials.OAuth{AccessToken: statusJWT(scopes), RefreshToken: "private-refresh-canary", ExpiresAt: farFuture, Scopes: strings.Fields(scopes)}
}
func gmailLogin(scopes string) credentials.OAuth {
	return credentials.OAuth{AccessToken: "private-gmail-canary", RefreshToken: "private-refresh-canary", ExpiresAt: farFuture, Scopes: strings.Fields(scopes)}
}

// record is a credential record with a screening key and the logins by store entry (provider/email).
func record(logins map[string]credentials.OAuth) *credentials.Record {
	if logins == nil {
		logins = map[string]credentials.OAuth{}
	}
	return &credentials.Record{Version: 1, OAuth: logins, ScreeningKey: "synthetic-screening-key"}
}

// fakeBackend is the credential backend of the tests: one record in memory, as the Keychain item or the file holds
// it. readErr fails every read, writeErr every write, and panics makes every read panic.
type fakeBackend struct {
	mu                sync.Mutex
	data              []byte // the record as JSON; nil: no record
	readErr, writeErr error
	panics            bool
}

func (b *fakeBackend) Read(context.Context) (credentials.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.panics {
		panic("private-backend-canary")
	}
	if b.readErr != nil {
		return credentials.Record{}, b.readErr
	}
	if b.data == nil {
		return credentials.Record{}, credentials.ErrNotFound
	}
	var r credentials.Record
	err := json.Unmarshal(b.data, &r)
	return r, err
}
func (b *fakeBackend) Write(_ context.Context, r credentials.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.writeErr != nil {
		return b.writeErr
	}
	data, err := json.Marshal(r)
	if err == nil {
		b.data = data
	}
	return err
}

// loopbackOnly refuses every request beyond this machine, so a provider or screening URL that a test leaves at its
// default never reaches a real service.
type loopbackOnly struct{}

func (loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return http.DefaultTransport.RoundTrip(r)
	}
	return nil, errors.New("request beyond loopback refused")
}

// daemonSetup is a home for newDaemon. config is config.toml without its version line. A nil record is a store
// without a record. jev is the fake Jev engine that screens; nil leaves the engine to the [screening] table.
type daemonSetup struct {
	home    string
	config  string
	record  *credentials.Record
	jev     *nativeAPI
	options server.Options
}

// daemon is the daemon of one test: its home, the server, and the credential store that amgd commands share with it.
type daemon struct {
	home    string
	s       *server.Server
	store   *credentials.RecordStore
	backend *fakeBackend
	// screening is the [screening] table of the fake Jev, kept across config rewrites.
	screening string
}

// newDaemon prepares a daemon as amgd serve does, up to its first load: it writes config.toml, puts a RecordStore on a
// fake backend that holds the record, and runs server.New. Provider clients without an HTTP client reach loopback only.
func newDaemon(t *testing.T, d daemonSetup) *daemon {
	t.Helper()
	if d.home == "" {
		d.home = t.TempDir()
	}
	backend := &fakeBackend{}
	if d.record != nil {
		data, err := json.Marshal(*d.record)
		if err != nil {
			t.Fatal(err)
		}
		backend.data = data
	}
	o := d.options
	dm := &daemon{home: d.home, backend: backend, store: credentials.NewStore(d.home, backend)}
	if d.jev != nil {
		jev := httptest.NewTLSServer(http.HandlerFunc(d.jev.jev))
		t.Cleanup(jev.Close)
		// Every dial goes to the fake Jev, so no screening request leaves the machine.
		transport := jev.Client().Transport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return new(net.Dialer).DialContext(ctx, network, jev.Listener.Addr().String())
		}
		o.ScreeningHTTPClient = &http.Client{Transport: transport}
		dm.screening = "[screening]\nendpoint='" + jev.URL + "'\n"
	}
	for _, c := range []**http.Client{&o.ScreeningHTTPClient, &o.M365.HTTPClient, &o.Gmail.HTTPClient} {
		if *c == nil {
			*c = &http.Client{Transport: loopbackOnly{}}
		}
	}
	o.Store = dm.store
	dm.writeConfig(t, d.config)
	s, err := server.New(context.Background(), d.home, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	dm.s = s
	return dm
}

// startDaemon is newDaemon and the first load, without a screening key prompt. A refused load fails the test.
func startDaemon(t *testing.T, d daemonSetup) *daemon {
	t.Helper()
	dm := newDaemon(t, d)
	if result, err := dm.s.Load(context.Background(), nil); err != nil {
		t.Fatalf("load: %v checks=%v", err, result.Checks)
	}
	return dm
}

// writeConfig replaces config.toml with version 1, config, and the [screening] table of the fake Jev.
func (d *daemon) writeConfig(t *testing.T, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.home, "config.toml"), []byte("version = 1\n"+config+d.screening), 0600); err != nil {
		t.Fatal(err)
	}
}

// reload runs Reload; a refused reload fails the test.
func (d *daemon) reload(t *testing.T) server.Result {
	t.Helper()
	result, err := d.s.Reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v checks=%v", err, result.Checks)
	}
	return result
}

// graphDaemon starts a daemon with the M365 accounts work and other, both logged in with scopes, and config appended
// to their configuration. graph is the Graph API and jev the fake Jev.
func graphDaemon(t *testing.T, config, scopes string, graph http.Handler, jev *nativeAPI) *daemon {
	t.Helper()
	api := httptest.NewServer(graph)
	t.Cleanup(api.Close)
	return startDaemon(t, daemonSetup{
		config:  workConfig + config,
		record:  record(map[string]credentials.OAuth{"m365/" + workEmail: m365Login(scopes), "m365/" + otherEmail: m365Login(scopes)}),
		jev:     jev,
		options: server.Options{M365: m365Options(api.URL), Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }},
	})
}

// m365Options point the Graph API and the token endpoint at url.
func m365Options(url string) m365.Options { return m365.Options{BaseURL: url, AuthorityURL: url} }

// fakeHome is a home whose fake-provider.db holds the inbox message g of the fake account demo, which the fake Jev
// screens green.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	schema, err := os.ReadFile("../providers/fake/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	fakeSQL(t, home, string(schema))
	fakeSQL(t, home, `INSERT INTO messages (account, id, sender_address, subject, body, received_at, spf, dkim, dmarc) VALUES (?1, 'g', 'sender@example.test', 'private-subject-g', 'private-body-g', '2026-10-06T09:00:00Z', 'pass', 'pass', 'pass')`)
	return home
}

func fakeDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(home, "fake-provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeSQL runs a statement on fake-provider.db of home. ?1 in the statement is the account key of demo.
func fakeSQL(t *testing.T, home, statement string) {
	t.Helper()
	db := fakeDB(t, home)
	var args []any
	if strings.Contains(statement, "?1") {
		args = append(args, demoEmail)
	}
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

// fakeDaemon starts a daemon for the fake account demo on home with config appended, screened by the fake Jev.
func fakeDaemon(t *testing.T, home, config string) *daemon {
	t.Helper()
	return startDaemon(t, daemonSetup{home: home, config: demoConfig + config, record: record(nil), jev: newNativeAPI(),
		options: server.Options{Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }}})
}

// accountRPC makes one API call through the handler. An empty account leaves the account out of the request.
func accountRPC(t *testing.T, s *server.Server, op, account, params string) (int, string) {
	t.Helper()
	field := ""
	if account != "" {
		field = `"account":"` + account + `",`
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/call", strings.NewReader(`{"operation":"`+op+`",`+field+`"params":`+params+`}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// rpc posts body to the API of a listening server.
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

func response(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		t.Fatalf("invalid JSON: %s %s", a, b)
	}
	return reflect.DeepEqual(x, y)
}

// auditOf returns the audit log of home as text and as records.
func auditOf(t *testing.T, home string) (string, []map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	return string(data), out
}

// assertAudited checks that home's audit log has the operation and none of the request content.
func assertAudited(t *testing.T, home, operation string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil || strings.Contains(string(data), "CANARY") || strings.Contains(string(data), "example.test") || !strings.Contains(string(data), `"operation":"`+operation+`"`) {
		t.Fatalf("audit=%s error=%v", data, err)
	}
}

// assertNoAudit checks that home has no audit log: nothing was written, decided, or refused by policy.
func assertNoAudit(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatal("refusal wrote audit")
	}
}
