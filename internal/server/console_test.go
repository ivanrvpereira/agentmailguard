package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

const consolePassword = "private-console-pass"
const consoleHost = "localhost:8788"

// consoleFixture is a daemon with the M365 account work against the native fake Graph API and Jev, and its
// console. Its clock moves only when the test moves it.
type consoleFixture struct {
	d       *daemon
	api     *nativeAPI
	now     time.Time
	console http.Handler
}

func (f *consoleFixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// consoleHash is the bcrypt hash of consolePassword.
func consoleHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(consolePassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

// consoleDaemon starts a daemon whose credential record holds the console password when password is true.
// config is appended to the configuration.
func consoleDaemon(t *testing.T, config string, password bool) *consoleFixture {
	t.Helper()
	r := record(map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant + " Calendars.ReadWrite Mail.Send")})
	if password {
		r.ConsoleHash = consoleHash(t)
	}
	api := newNativeAPI()
	graph := httptest.NewServer(http.HandlerFunc(api.graph))
	t.Cleanup(graph.Close)
	f := &consoleFixture{api: api, now: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)}
	f.d = startDaemon(t, daemonSetup{config: "default_account = 'work'\n" + workTable + config, record: r, jev: api,
		options: server.Options{M365: m365Options(graph.URL), Now: func() time.Time { return f.now }}})
	f.console = f.d.s.Console()
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

const draftApprove = "[policy]\nmail_draft='approve'\nmail_trash='approve'\ncalendar_respond='approve'\n[policy.sealed]\nmail_trash='approve'\n"

// Without a console password the console refuses every request with the command that sets one. A password that
// amgd saves takes effect at the next reload.
func TestConsoleRefusesEverythingUntilAPasswordIsSetAndReloaded(t *testing.T) {
	f := consoleDaemon(t, draftApprove, false)
	id := pend(t, f.d.s, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`)[0]
	for _, path := range []string{"/", "/a/" + id} {
		if w := f.get(t, path, nil); w.Code != 503 || strings.TrimSpace(w.Body.String()) != "console password not set; run amgd config set console.password" {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := f.decide(t, id, "approve", nil); w.Code != 503 || f.writes() != 0 {
		t.Fatalf("decision without a password: %d writes=%d", w.Code, f.writes())
	}
	if err := f.d.store.SaveConsoleHash(context.Background(), consoleHash(t)); err != nil {
		t.Fatal(err)
	}
	if w := f.get(t, "/", nil); w.Code != 503 {
		t.Fatalf("before the reload: %d", w.Code)
	}
	f.d.reload(t)
	if w := f.get(t, "/", nil); w.Code != 200 {
		t.Fatalf("after the reload: %d %s", w.Code, w.Body)
	}
}

func TestConsoleNeedsThePasswordOnEveryPage(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`)[0]
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
	f := consoleDaemon(t, "", true)
	f.get(t, "/", func(r *http.Request) { r.Header.Del("Authorization") })
	f.get(t, "/", func(r *http.Request) { r.SetBasicAuth("private-user-canary", "private-pass-canary") })
	f.get(t, "/", func(r *http.Request) { r.SetBasicAuth("private-user-canary", "private-pass-canary2") })
	f.get(t, "/", nil)
	text, records := auditOf(t, f.d.home)
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
	f := consoleDaemon(t, "", true)
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
	if _, records := auditOf(t, f.d.home); len(records) != 5 {
		t.Fatalf("records=%d; refused requests are not password checks", len(records))
	}
	f.advance(11 * time.Second)
	if w := f.get(t, "/", nil); w.Code != 200 {
		t.Fatalf("after the oldest failure left the window: %d", w.Code)
	}
}

// Host names that are not the console address or an extra configured host are refused, which blocks DNS rebinding.
func TestConsoleAcceptsOnlyItsOwnHostNames(t *testing.T) {
	f := consoleDaemon(t, "[console]\nlisten='127.0.0.1:9000'\nhosts=['mac.tail0.ts.net','Box.example.test:8443']\n", true)
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
	f := consoleDaemon(t, "", true)
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
	f := consoleDaemon(t, draftApprove, true)
	add := func(name string) string {
		t.Helper()
		id := pend(t, f.d.s, "draft.new", `{"to":["`+name+`@example.test"],"subject":"`+name+`-subject","body":"`+name+`-body"}`)[0]
		f.advance(time.Minute)
		return id
	}
	older, denied, newer := add("older"), add("denied"), add("newer")
	if err := f.d.s.Deny(context.Background(), denied); err != nil {
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
	for _, want := range []string{workEmail, "draft.new", "older@example.test", "older-subject", "pending", "denied", "3m", "1m"} {
		if !strings.Contains(page, want) {
			t.Errorf("list misses %q: %s", want, page)
		}
	}
	if strings.Contains(page, "-body") {
		t.Errorf("list shows a body: %s", page)
	}
	onlyConsoleLinks(t, page)
}

// Optional attendees receive the invitation, so the list shows them with the required ones.
func TestConsoleListShowsOptionalAttendeesAsRecipients(t *testing.T) {
	f := consoleDaemon(t, approveAll, true)
	pend(t, f.d.s, "calendar.invite", `{"to":["required@example.test"],"optional":["optional@example.test"],"subject":"s","body":"","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)
	if page := f.get(t, "/", nil).Body.String(); !strings.Contains(page, "required@example.test, optional@example.test") {
		t.Fatalf("list misses the recipients: %s", page)
	}
}

// A reply goes to the original's recipients too, which only the item page reads, so the list marks a reply row as a
// reply or a reply all before the cc and bcc of the request.
func TestConsoleListMarksReplyRows(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	pend(t, f.d.s, "draft.reply", `{"id":"g","body":"b","all":true,"cc":["c@example.test"],"bcc":["d@example.test"]}`)
	pend(t, f.d.s, "draft.reply", `{"id":"g","body":"b"}`)
	page := f.get(t, "/", nil).Body.String()
	for _, want := range []string{"<td>reply all; cc: c@example.test; bcc (hidden): d@example.test</td>", "<td>reply</td>"} {
		if !strings.Contains(page, want) {
			t.Errorf("list misses %q: %s", want, page)
		}
	}
}

// The item page shows the stored arguments as text and the referenced message through the guarded read,
// without its body.
func TestConsoleItemShowsStoredArgumentsAsTextAndTheMessageSummary(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.reply", `{"id":"g","body":"<script>alert(1)</script> see https://evil.example/x"}`)[0]
	w := f.get(t, "/a/"+id, nil)
	page := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, page)
	}
	for _, want := range []string{workEmail, "draft.reply", "pending", "&lt;script&gt;alert(1)&lt;/script&gt; see https://evil.example/x", "private-subject-g", "sender@example.test", "green", "2026-03-30", `name="decision" value="approve"`, `name="decision" value="deny"`} {
		if !strings.Contains(page, want) {
			t.Errorf("item page misses %q", want)
		}
	}
	if strings.Contains(page, "private-body-g") || strings.Contains(page, "example.test/report") {
		t.Errorf("item page shows the message body: %s", page)
	}
	onlyConsoleLinks(t, page)
}

// The item page shows the Jev action check result; an action without a check shows none. The list does not show it.
func TestConsoleItemShowsTheJevCheck(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	for _, tc := range []struct {
		score float64
		want  string
	}{
		{0.62, "Jev: flagged (score 0.62)"},
		{0.08, "Jev: not flagged (score 0.08)"},
		{2, "Jev: check failed (ValidationError)"},
	} {
		f.api.mu.Lock()
		f.api.actionScore = tc.score
		f.api.mu.Unlock()
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		if page := f.get(t, "/a/"+id, nil).Body.String(); !strings.Contains(page, tc.want) {
			t.Errorf("item page misses %q: %s", tc.want, page)
		}
	}
	id := pend(t, f.d.s, "mail.trash", `{"ids":["g"]}`)[0]
	if page := f.get(t, "/a/"+id, nil).Body.String(); strings.Contains(page, "Jev") {
		t.Errorf("unchecked action shows a Jev result: %s", page)
	}
	if page := f.get(t, "/", nil).Body.String(); strings.Contains(page, "Jev") {
		t.Errorf("list shows a Jev result: %s", page)
	}
}

// fakeConsole starts a daemon for the fake account demo on home, with config appended, whose credential record
// holds a screening key and the console password. The jev engine reaches no network.
func fakeConsole(t *testing.T, home, config string) *consoleFixture {
	t.Helper()
	r := record(nil)
	r.ConsoleHash = consoleHash(t)
	d := startDaemon(t, daemonSetup{home: home, config: demoConfig + config, record: r})
	return &consoleFixture{d: d, console: d.s.Console()}
}

const draftApproveOnly = "[policy]\nmail_draft='approve'\n"

// A check made by the fake engine is labeled as a keyword check, never as a Jev check.
func TestConsoleItemLabelsTheFakeCheck(t *testing.T) {
	f := fakeConsole(t, fakeHome(t), fakeScreen+draftApproveOnly)
	for body, want := range map[string]string{
		"See you at noon":        "Fake check (keywords only): not flagged (score 0.05)",
		"Your new password is x": "Fake check (keywords only): flagged (score 0.90)",
		"SCREENFAIL":             "Fake check (keywords only): check failed (TypeSafeAPIError 503)",
	} {
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"`+body+`"}`)[0]
		if page := f.get(t, "/a/"+id, nil).Body.String(); !strings.Contains(page, want) || strings.Contains(page, "Jev") {
			t.Errorf("%q: item page misses %q or names Jev: %s", body, want, page)
		}
	}
}

// The label follows the engine that made the check, not the engine of the running server: an item checked by
// the fake engine keeps its label after a restart with Jev screening.
func TestConsoleItemKeepsTheFakeLabelAfterARestartWithJev(t *testing.T) {
	home := fakeHome(t)
	before := fakeConsole(t, home, fakeScreen+draftApproveOnly)
	id := pend(t, before.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"See you at noon"}`)[0]
	if err := before.d.s.Close(); err != nil {
		t.Fatal(err)
	}
	after := fakeConsole(t, home, draftApproveOnly)
	page := after.get(t, "/a/"+id, nil).Body.String()
	if want := "Fake check (keywords only): not flagged (score 0.05)"; !strings.Contains(page, want) || strings.Contains(page, "Jev") {
		t.Fatalf("item page misses %q or names Jev: %s", want, page)
	}
}

// The mark of the fake engine stays with the stored item: the agent's responses and the audit log never
// carry it.
func TestFakeCheckMarkStaysOutOfAgentResponsesAndTheAudit(t *testing.T) {
	f := fakeConsole(t, fakeHome(t), fakeScreen+draftApproveOnly)
	status, body := accountRPC(t, f.d.s, "draft.new", "demo", `{"to":["a@example.test"],"subject":"s","body":"Your new password is x"}`)
	match := approvalID.FindStringSubmatch(body)
	if status != 200 || match == nil {
		t.Fatalf("draft: %d %s", status, body)
	}
	if status, body := approvalStatus(t, f.d.s, match[1]); status != 200 || body != `{"state":"pending"}` {
		t.Fatalf("status: %d %s", status, body)
	}
	if text, _ := auditOf(t, f.d.home); strings.Contains(text, "fake") || !strings.Contains(text, `"jev_flag"`) {
		t.Fatalf("audit=%s", text)
	}
}

func TestConsoleItemShowsTheReferencedEvent(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"tentative","comment":"CANARY comment"}`)[0]
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
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "mail.trash", `{"ids":["gone"]}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	if !strings.Contains(page, "not available") || !strings.Contains(page, `value="approve"`) || !strings.Contains(page, "gone") {
		t.Fatalf("page=%s", page)
	}
}

func TestConsoleRefusesUnknownPathsAndIDs(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
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
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
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
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
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
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "mail.trash", `{"ids":["gone"]}`)[0]
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
	f := consoleDaemon(t, "[console]\nhosts=['mac.tail0.ts.net']\n"+draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
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
	if f.writes() != 0 {
		t.Fatal("refused decision reached the provider")
	}
	assertState(t, f.d.s, id, `{"state":"pending"}`)
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
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
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
	if f.writes() != 0 {
		t.Fatal("refused decision reached the provider")
	}
	assertState(t, f.d.s, id, `{"state":"pending"}`)
	if w := f.decide(t, "0123456789abcdef0123456789abcdef", "approve", nil); w.Code != 404 {
		t.Fatalf("unknown id: %d", w.Code)
	}
}
