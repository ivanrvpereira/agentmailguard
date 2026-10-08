package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

const consolePassword = "private-console-pass"
const consoleHost = "localhost:8788"

// consoleFixture is a preloaded server for the M365 account work, against the native fake Graph API and Jev.
// Its clock moves only when the test moves it.
type consoleFixture struct {
	s       *server.Server
	home    string
	api     *nativeAPI
	now     time.Time
	console http.Handler
}

func (f *consoleFixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// consoleServer starts a server whose credential record holds the approval password when password is true.
// table is appended to the configuration.
func consoleServer(t *testing.T, table string, password bool) *consoleFixture {
	t.Helper()
	home := t.TempDir()
	text := "[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n" + table
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	record := credentials.Record{Version: 1, OpenRouterKey: "synthetic-key", OAuth: map[string]credentials.OAuth{
		"m365/work": {AccessToken: statusJWT(m365Grant + " Calendars.ReadWrite"), RefreshToken: "private-refresh", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Scopes: strings.Fields(m365Grant + " Calendars.ReadWrite"), Username: "work@example.test"},
	}}
	if password {
		hash, err := bcrypt.GenerateFromPassword([]byte(consolePassword), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		record.ApprovalHash = string(hash)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	store := credentials.New(&recordPlatform{data: data}, home, nil)
	api := newNativeAPI()
	graph := httptest.NewServer(http.HandlerFunc(api.graph))
	t.Cleanup(graph.Close)
	jev := httptest.NewServer(http.HandlerFunc(api.jev))
	t.Cleanup(jev.Close)
	f := &consoleFixture{home: home, api: api, now: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)}
	screen := screening.New(screening.Options{Endpoint: jev.URL, Credentials: store, LookupEnv: func(string) string { return "synthetic-key" }})
	s, err := server.New(context.Background(), home, server.Options{Credentials: store, Screener: screen, M365: m365.Options{BaseURL: graph.URL, AuthorityURL: graph.URL}, Now: func() time.Time { return f.now }, LookupEnv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Preload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.s = s
	if handler, ok := s.Console(); ok {
		f.console = handler
	}
	return f
}

// get asks the console for path as a signed-in browser on the console address. change edits the request first.
func (f *consoleFixture) get(t *testing.T, path string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return f.send(t, httptest.NewRequest(http.MethodGet, path, nil), change)
}

// decide posts a decision form for the item as a signed-in browser on the console address.
func (f *consoleFixture) decide(t *testing.T, id, decision string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/a/"+id, strings.NewReader("decision="+decision))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+consoleHost)
	return f.send(t, r, change)
}

func (f *consoleFixture) send(t *testing.T, r *http.Request, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	if f.console == nil {
		t.Fatal("console is off")
	}
	r.Host = consoleHost
	r.SetBasicAuth("anyone", consolePassword)
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	f.console.ServeHTTP(w, r)
	return w
}

func wrongPassword(r *http.Request) { r.SetBasicAuth("anyone", "private-wrong-pass") }

const draftApprove = "[policy]\nmail_draft='approve'\nmail_trash='approve'\ncalendar_respond='approve'\n[policy.sealed]\nmail_draft='approve'\nmail_trash='approve'\n"

func TestConsoleIsOffWithoutAnApprovalPassword(t *testing.T) {
	f := consoleServer(t, "", false)
	if handler, ok := f.s.Console(); ok || handler != nil {
		t.Fatal("console is on without a password")
	}
}

func TestConsoleNeedsThePasswordOnEveryPage(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`)[0]
	for _, path := range []string{"/", "/a/" + id, "/missing"} {
		for name, change := range map[string]func(*http.Request){
			"no credentials":   func(r *http.Request) { r.Header.Del("Authorization") },
			"malformed header": func(r *http.Request) { r.Header.Set("Authorization", "Basic !!!") },
			"wrong password":   wrongPassword,
		} {
			w := f.get(t, path, change)
			if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Basic realm="agentmailguard approvals", charset="UTF-8"` || strings.Contains(w.Body.String(), "CANARY") {
				t.Errorf("%s %s: %d %q %s", path, name, w.Code, w.Header().Get("WWW-Authenticate"), w.Body)
			}
			f.advance(time.Minute) // Stay below the failure limit.
		}
	}
	if w := f.decide(t, id, "approve", wrongPassword); w.Code != 401 || f.writes() != 0 {
		t.Fatalf("decision without the password: %d writes=%d", w.Code, f.writes())
	}
	if w := f.get(t, "/", nil); w.Code != 200 {
		t.Fatalf("signed in: %d %s", w.Code, w.Body)
	}
}

// Each wrong password adds one record that holds no request detail. Missing credentials add none.
func TestConsoleAuditsEachFailedSignInWithoutDetails(t *testing.T) {
	f := consoleServer(t, "", true)
	f.get(t, "/", func(r *http.Request) { r.Header.Del("Authorization") })
	f.get(t, "/", func(r *http.Request) { r.SetBasicAuth("private-user-canary", "private-pass-canary") })
	f.get(t, "/", func(r *http.Request) { r.SetBasicAuth("private-user-canary", "private-pass-canary2") })
	f.get(t, "/", nil)
	text, records := auditOf(t, f.home)
	if len(records) != 2 || strings.Contains(text, "private-") || strings.Contains(text, "localhost") || strings.Contains(text, "192.0.2") {
		t.Fatalf("audit=%s", text)
	}
	for _, record := range records {
		if len(record) != 3 || record["event"] != "console_signin" || record["result"] != "failed" || record["time"] == nil {
			t.Fatalf("record=%v", record)
		}
	}
}

func TestConsoleRefusesEveryRequestAfterFiveFailedSignInsForAMinute(t *testing.T) {
	f := consoleServer(t, "", true)
	for i := range 5 {
		if w := f.get(t, "/", wrongPassword); w.Code != 401 {
			t.Fatalf("failure %d: %d", i, w.Code)
		}
		f.advance(10 * time.Second)
	}
	// The first failure was 50 seconds ago.
	for name, change := range map[string]func(*http.Request){"right password": nil, "wrong password": wrongPassword} {
		if w := f.get(t, "/", change); w.Code != 429 {
			t.Fatalf("%s while limited: %d", name, w.Code)
		}
	}
	if _, records := auditOf(t, f.home); len(records) != 5 {
		t.Fatalf("records=%d; refused requests are not password checks", len(records))
	}
	f.advance(11 * time.Second)
	if w := f.get(t, "/", nil); w.Code != 200 {
		t.Fatalf("after the oldest failure left the window: %d", w.Code)
	}
}

// Host names that are not the console address or an extra configured host are refused, which blocks DNS rebinding.
func TestConsoleAcceptsOnlyItsOwnHostNames(t *testing.T) {
	f := consoleServer(t, "[approvals]\nlisten='127.0.0.1:9000'\nhosts=['mac.tail0.ts.net','Box.example.test:8443']\n", true)
	for host, want := range map[string]int{
		"127.0.0.1:9000":        200,
		"mac.tail0.ts.net":      200,
		"MAC.tail0.ts.net":      200,
		"box.example.test:8443": 200,
		"mac.tail0.ts.net:443":  403,
		"box.example.test":      403,
		"127.0.0.1":             403,
		"localhost:9000":        403,
		"evil.example:9000":     403,
		"":                      403,
	} {
		w := f.get(t, "/", func(r *http.Request) { r.Host = host })
		if w.Code != want {
			t.Errorf("host %q: %d; want %d", host, w.Code, want)
		}
	}
	// A refused host never reaches the password check.
	for range 6 {
		f.get(t, "/", func(r *http.Request) { r.Host = "evil.example"; wrongPassword(r) })
	}
	if w := f.get(t, "/", func(r *http.Request) { r.Host = "127.0.0.1:9000" }); w.Code != 200 {
		t.Fatalf("after refused hosts: %d", w.Code)
	}
}

func TestConsoleResponsesCarryTheSecurityHeaders(t *testing.T) {
	f := consoleServer(t, "", true)
	for name, change := range map[string]func(*http.Request){
		"page":           nil,
		"no credentials": func(r *http.Request) { r.Header.Del("Authorization") },
		"foreign host":   func(r *http.Request) { r.Host = "evil.example" },
		"not found":      func(r *http.Request) { r.URL.Path = "/missing" },
	} {
		w := f.get(t, "/", change)
		for header, want := range map[string]string{
			"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
			"Cache-Control":           "no-store",
			"X-Frame-Options":         "DENY",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("%s: %s=%q", name, header, got)
			}
		}
	}
}

// hrefs are the link targets of a page; consoleLink is a link to the console's own list or item page.
var (
	hrefs       = regexp.MustCompile(`href="([^"]*)"`)
	consoleLink = regexp.MustCompile(`^/(a/[0-9a-f]{32})?$`)
)

// onlyConsoleLinks fails when a page links anywhere but the console's own list and item pages.
func onlyConsoleLinks(t *testing.T, page string) {
	t.Helper()
	for _, m := range hrefs.FindAllStringSubmatch(page, -1) {
		if !consoleLink.MatchString(m[1]) {
			t.Fatalf("foreign link %q in %s", m[1], page)
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "<a href=\"http") {
		t.Fatalf("active content in %s", page)
	}
}

func TestConsoleListShowsWaitingActionsNewestFirstThenResolved(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	add := func(name string) string {
		t.Helper()
		id := pend(t, f.s, "draft.new", `{"to":["`+name+`@example.test"],"subject":"`+name+`-subject","body":"`+name+`-body"}`)[0]
		f.advance(time.Minute)
		return id
	}
	older, denied, newer := add("older"), add("denied"), add("newer")
	if err := f.s.Deny(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	w := f.get(t, "/", nil)
	page := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", w.Code, page)
	}
	positions := []int{}
	for _, id := range []string{newer, older, denied} {
		positions = append(positions, strings.Index(page, `href="/a/`+id+`"`))
	}
	if positions[0] < 0 || positions[0] > positions[1] || positions[1] > positions[2] {
		t.Fatalf("order newer, older, denied: positions=%v page=%s", positions, page)
	}
	for _, want := range []string{"work", "draft.new", "older@example.test", "older-subject", "pending", "denied", "3m", "1m"} {
		if !strings.Contains(page, want) {
			t.Errorf("list misses %q: %s", want, page)
		}
	}
	if strings.Contains(page, "-body") {
		t.Errorf("list shows a body: %s", page)
	}
	onlyConsoleLinks(t, page)
}

// The item page shows the stored arguments as text and the referenced message through the guarded read,
// without its body.
func TestConsoleItemShowsStoredArgumentsAsTextAndTheMessageSummary(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.reply", `{"id":"g","body":"<script>alert(1)</script> see https://evil.example/x"}`)[0]
	w := f.get(t, "/a/"+id, nil)
	page := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, page)
	}
	for _, want := range []string{"work", "draft.reply", "pending", "&lt;script&gt;alert(1)&lt;/script&gt; see https://evil.example/x", "private-subject-g", "sender@example.test", "green", "2026-03-30", `name="decision" value="approve"`, `name="decision" value="deny"`} {
		if !strings.Contains(page, want) {
			t.Errorf("item page misses %q", want)
		}
	}
	if strings.Contains(page, "private-body-g") || strings.Contains(page, "example.test/report") {
		t.Errorf("item page shows the message body: %s", page)
	}
	onlyConsoleLinks(t, page)
}

func TestConsoleItemShowsTheReferencedEvent(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "calendar.respond", `{"id":"e","response":"tentative","comment":"CANARY comment"}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	for _, want := range []string{"calendar.respond", "tentative", "CANARY comment", "private-subject-event", "2026-03-31T10:00:00", "2026-03-31T11:00:00"} {
		if !strings.Contains(page, want) {
			t.Errorf("item page misses %q: %s", want, page)
		}
	}
	if strings.Contains(page, "private-body-event") {
		t.Errorf("item page shows the event body: %s", page)
	}
}

// A referenced message that cannot be read does not block the decision.
func TestConsoleItemStillOffersTheDecisionWhenTheMessageIsUnavailable(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "mail.trash", `{"ids":["gone"]}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	if !strings.Contains(page, "not available") || !strings.Contains(page, `value="approve"`) || !strings.Contains(page, "gone") {
		t.Fatalf("page=%s", page)
	}
}

func TestConsoleRefusesUnknownPathsAndIDs(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	for _, path := range []string{"/a/" + strings.ToUpper(id), "/a/" + id[1:], "/a/" + id + "/x", "/a/0123456789abcdef0123456789abcdef", "/a/", "/v1/call", "/index.html"} {
		if w := f.get(t, path, nil); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if w := f.send(t, httptest.NewRequest(method, "/a/"+id, nil), nil); w.Code != 405 {
			t.Errorf("%s item: %d", method, w.Code)
		}
	}
	if w := f.send(t, httptest.NewRequest("POST", "/", strings.NewReader("decision=approve")), func(r *http.Request) {
		r.Header.Set("Origin", "http://"+consoleHost)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}); w.Code != 405 {
		t.Errorf("POST list: %d", w.Code)
	}
}

func (f *consoleFixture) writes() int {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	return len(f.api.writes)
}

func TestConsoleApproveRunsTheActionOnceAndShowsDone(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	w := f.decide(t, id, "approve", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/a/"+id || f.writes() != 1 {
		t.Fatalf("approve: %d %q writes=%d", w.Code, w.Header().Get("Location"), f.writes())
	}
	page := f.get(t, "/a/"+id, nil).Body.String()
	if !strings.Contains(page, `>done<`) || !strings.Contains(page, "draft-1") || strings.Contains(page, `value="approve"`) {
		t.Fatalf("page after approve=%s", page)
	}
	w = f.decide(t, id, "approve", nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "already handled") || f.writes() != 1 {
		t.Fatalf("second approve: %d writes=%d %s", w.Code, f.writes(), w.Body)
	}
	if w := f.decide(t, id, "deny", nil); w.Code != 409 {
		t.Fatalf("deny after approve: %d", w.Code)
	}
}

func TestConsoleDenyRunsNothing(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	if w := f.decide(t, id, "deny", nil); w.Code != 303 || w.Header().Get("Location") != "/a/"+id {
		t.Fatalf("deny: %d", w.Code)
	}
	page := f.get(t, "/a/"+id, nil).Body.String()
	if !strings.Contains(page, `>denied<`) || strings.Contains(page, `value="approve"`) || f.writes() != 0 {
		t.Fatalf("writes=%d page=%s", f.writes(), page)
	}
	if w := f.decide(t, id, "approve", nil); w.Code != 409 || f.writes() != 0 {
		t.Fatalf("approve after deny: %d writes=%d", w.Code, f.writes())
	}
}

// A failed run shows failed and its safe code.
func TestConsoleShowsTheCodeOfAFailedRun(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "mail.trash", `{"ids":["gone"]}`)[0]
	if w := f.decide(t, id, "approve", nil); w.Code != 303 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	page := f.get(t, "/a/"+id, nil).Body.String()
	if !strings.Contains(page, `>failed<`) || !strings.Contains(page, "Graph error 404") || strings.Contains(page, "private-provider-canary") {
		t.Fatalf("page=%s", page)
	}
}

// A decision needs an Origin of the console itself, which blocks cross-site form posts.
func TestConsoleDecisionNeedsTheConsoleOrigin(t *testing.T) {
	f := consoleServer(t, "[approvals]\nhosts=['mac.tail0.ts.net']\n"+draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	for name, change := range map[string]func(*http.Request){
		"no origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"null origin":    func(r *http.Request) { r.Header.Set("Origin", "null") },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"other host":     func(r *http.Request) { r.Header.Set("Origin", "https://mac.tail0.ts.net") },
		"port missing":   func(r *http.Request) { r.Header.Set("Origin", "http://localhost") },
	} {
		if w := f.decide(t, id, "approve", change); w.Code != 403 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if f.writes() != 0 || storedArgs(t, f.home, id).State != "pending" {
		t.Fatal("refused decision changed the item")
	}
	// Behind an HTTPS tunnel the browser sends the https origin of the tunnel host.
	w := f.decide(t, id, "deny", func(r *http.Request) {
		r.Host = "mac.tail0.ts.net"
		r.Header.Set("Origin", "HTTPS://MAC.tail0.ts.net")
	})
	if w.Code != 303 {
		t.Fatalf("tunnel origin: %d", w.Code)
	}
}

func TestConsoleRefusesMalformedDecisions(t *testing.T) {
	f := consoleServer(t, draftApprove, true)
	id := pend(t, f.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	for name, change := range map[string]func(*http.Request){
		"unknown decision": func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("decision=maybe")) },
		"empty body":       func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("")) },
		"two decisions":    func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("decision=deny&decision=approve")) },
		"extra field":      func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("decision=approve&x=1")) },
		"query decision": func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(""))
			r.URL.RawQuery = "decision=approve"
		},
		"json": func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		"too large": func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader("decision=approve" + strings.Repeat("&", 4096)))
		},
	} {
		if w := f.decide(t, id, "approve", change); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if f.writes() != 0 || storedArgs(t, f.home, id).State != "pending" {
		t.Fatal("refused decision changed the item")
	}
	if w := f.decide(t, "0123456789abcdef0123456789abcdef", "approve", nil); w.Code != 404 {
		t.Fatalf("unknown id: %d", w.Code)
	}
}
