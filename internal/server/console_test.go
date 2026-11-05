package server_test

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

const consolePassword = "private-console-pass"
const consoleHost = "localhost:8788"

// consoleUser is the default console username, which the fixture browser signs in with.
const consoleUser = "admin"

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
	return newConsoleFixture(t, config, password, false)
}

// gmailConsole is consoleDaemon with the console password and a second account, the Gmail account gmail, whose
// provider calls reach no server.
func gmailConsole(t *testing.T, config string) *consoleFixture {
	t.Helper()
	return newConsoleFixture(t, config, true, true)
}

func newConsoleFixture(t *testing.T, config string, password, gmail bool) *consoleFixture {
	t.Helper()
	logins := map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant + " Calendars.ReadWrite Mail.Send")}
	accounts := workTable
	if gmail {
		logins["gmail/"+gmailEmail] = gmailLogin(gmailWriteGrant)
		accounts += gmailTable
	}
	r := record(logins)
	if password {
		r.ConsoleHash = consoleHash(t)
	}
	api := newNativeAPI()
	graph := httptest.NewServer(http.HandlerFunc(api.graph))
	t.Cleanup(graph.Close)
	f := &consoleFixture{api: api, now: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)}
	f.d = startDaemon(t, daemonSetup{config: "default_account = 'work'\n" + accounts + config, record: r, jev: api,
		options: server.Options{M365: m365Options(graph.URL), Now: func() time.Time { return f.now }}})
	f.console = f.d.s.Console()
	return f
}

// get asks the console for path as a signed-in browser on the console address. change edits the request first.
func (f *consoleFixture) get(t *testing.T, path string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return f.send(t, httptest.NewRequest(http.MethodGet, path, nil), change)
}

// decide posts a decision form for the action as a signed-in browser on the console address.
func (f *consoleFixture) decide(t *testing.T, id, decision string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return f.post(t, "/a/"+id, "decision="+decision, change)
}

// post posts the form body to path as a signed-in browser on the console address.
func (f *consoleFixture) post(t *testing.T, path, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+consoleHost)
	return f.send(t, r, change)
}

func (f *consoleFixture) send(t *testing.T, r *http.Request, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r.Host = consoleHost
	r.SetBasicAuth(consoleUser, consolePassword)
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	f.console.ServeHTTP(w, r)
	return w
}

func wrongPassword(r *http.Request) { r.SetBasicAuth(consoleUser, "private-wrong-pass") }

// signInAs signs in with user and the right password.
func signInAs(user string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(user, consolePassword) }
}

// The console needs the configured username, admin by default, with the password. A wrong username is a failed
// sign-in like a wrong password: the same answer and an audit record. A new username takes effect at the reload.
func TestConsoleNeedsTheConfiguredUsername(t *testing.T) {
	f := consoleDaemon(t, "", true)
	for user, want := range map[string]int{"admin": 200, "anyone": 401, "Admin": 401, "": 401, "admin ": 401} {
		if w := f.get(t, "/", signInAs(user)); w.Code != want {
			t.Errorf("default username, %q: %d; want %d", user, w.Code, want)
		}
		f.advance(time.Minute) // Stay below the failure limit.
	}
	if _, records := auditOf(t, f.d.home); len(records) != 4 {
		t.Fatalf("records=%d; want one for each wrong username", len(records))
	}
	f.d.writeConfig(t, "default_account = 'work'\n"+workTable+"[console]\nusername = 'admin@example.test'\n")
	if w := f.get(t, "/", signInAs("admin@example.test")); w.Code != 401 {
		t.Fatalf("before the reload: %d", w.Code)
	}
	f.advance(time.Minute)
	f.d.reload(t)
	for user, want := range map[string]int{"admin@example.test": 200, "admin": 401} {
		if w := f.get(t, "/", signInAs(user)); w.Code != want {
			t.Errorf("after the reload, %q: %d; want %d", user, w.Code, want)
		}
		f.advance(time.Minute)
	}
}

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

// consoleCSP is the content security policy of every console response: only the console's own script, and no
// Trusted Types policy, so that every HTML sink throws.
const consoleCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; require-trusted-types-for 'script'; trusted-types 'none'"

func TestConsoleResponsesCarryTheSecurityHeaders(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	for name, w := range map[string]*httptest.ResponseRecorder{
		"page":             f.get(t, "/", nil),
		"action page":      f.get(t, "/a/"+id, nil),
		"script":           f.get(t, "/assets/console.js", nil),
		"refresh redirect": f.get(t, "/?refresh=10m", nil),
		"no credentials":   f.get(t, "/", func(r *http.Request) { r.Header.Del("Authorization") }),
		"foreign host":     f.get(t, "/", func(r *http.Request) { r.Host = "evil.example" }),
		"not found":        f.get(t, "/missing", nil),
		"foreign origin":   f.decide(t, id, "deny", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }),
		"decision":         f.decide(t, id, "deny", nil),
	} {
		for header, want := range map[string]string{
			"Content-Security-Policy": consoleCSP,
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "same-origin",
			"Cache-Control":           "no-store",
			"X-Frame-Options":         "DENY",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("%s: %s=%q", name, header, got)
			}
		}
	}
}

// hrefs are the link targets of a page; consoleLink is a link to the console's own list page, with or without its
// filters and an Auto-refresh choice or a table to tick, or action page. tags are the tags of a page, and quoted the attribute values in a tag.
var (
	hrefs       = regexp.MustCompile(`href="([^"]*)"`)
	consoleLink = regexp.MustCompile(`^/(a/[0-9a-f]{32}|\?(account=[A-Za-z0-9_-]+&amp;)?(refresh=(5m|10m|30m|1h|off)&amp;)?(select=(messages|meetings|mailbox)&amp;)?(account=[A-Za-z0-9_-]+|refresh=(5m|10m|30m|1h|off)|select=(messages|meetings|mailbox)|status=(review|others)))?$`)
	tags        = regexp.MustCompile(`<[^>]*>`)
	quoted      = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	eventAttr   = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// consoleScriptTag is the one script tag of every console page.
const consoleScriptTag = `<script type="module" src="/assets/console.js"></script>`

// onlyConsoleLinks fails when a page links anywhere but the console's own pages, has a script but the one console
// script, or has an event handler attribute.
func onlyConsoleLinks(t *testing.T, page string) {
	t.Helper()
	for _, m := range hrefs.FindAllStringSubmatch(page, -1) {
		if !consoleLink.MatchString(m[1]) {
			t.Fatalf("foreign link %q in %s", m[1], page)
		}
	}
	if strings.Count(page, consoleScriptTag) != 1 || strings.Count(strings.ToLower(page), "<script") != 1 || strings.Contains(page, "<a href=\"http") {
		t.Fatalf("active content in %s", page)
	}
	for _, tag := range tags.FindAllString(page, -1) {
		if eventAttr.MatchString(quoted.ReplaceAllString(tag, `""`)) {
			t.Fatalf("event handler attribute in %s", tag)
		}
	}
}

// The action page shows the action label, the subject, and the operation, the account alias and key, the referenced
// message through the guarded read with its verdict explained, without its body, and then the stored arguments as
// text.
func TestConsoleActionShowsStoredArgumentsAsTextAndTheMessageSummary(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "draft.reply", `{"id":"g","all":true,"body":"<script>alert(1)</script> see https://evil.example/x"}`)[0]
	w := f.get(t, "/a/"+id, nil)
	page := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, page)
	}
	for _, want := range []string{
		`<h1>Draft reply <span class="what">private-subject-g</span> <span class="opname">draft.reply</span></h1>`,
		`<dt>State</dt><dd class="state pending">Pending</dd>`,
		"<dt>Account</dt><dd>work · " + workEmail + "</dd>",
		"<dt>Created</dt><dd>Tue 31 Mar 2026, 12:00</dd>",
		`<dt>ID</dt><dd><span class="ln">g</span></dd>`, `<dt>Reply all</dt><dd><span class="ln">yes</span></dd>`,
		`<dt>Verdict</dt><dd><span class="ln">Green: no sign of prompt injection</span></dd>`,
		"<figcaption>Body, plain text as the agent wrote it</figcaption><pre>&lt;script&gt;alert(1)&lt;/script&gt; see https://evil.example/x</pre>",
		"private-subject-g", "sender@example.test", "green", `<dt>Received</dt><dd><span class="ln">Mon 30 Mar 2026, 10:00</span></dd>`,
		`<p class="back"><a href="/">All approvals</a></p>`,
		`name="decision" value="approve"`, `name="decision" value="deny"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("action page misses %q: %s", want, page)
		}
	}
	if strings.Contains(page, "private-body-g") || strings.Contains(page, "example.test/report") || strings.Contains(page, "<iframe") {
		t.Errorf("action page shows the message body or a frame: %s", page)
	}
	message, recipients, request := strings.Index(page, "<h2>Message</h2>"), strings.Index(page, "<h2>Reply recipients</h2>"), strings.Index(page, "<h2>Request</h2>")
	if message < 0 || message > recipients || recipients > request {
		t.Errorf("section order: Message %d, Reply recipients %d, Request %d", message, recipients, request)
	}
	onlyConsoleLinks(t, page)
}

// The action page heading names what the action is about: the subject of the request, also the new subject of a
// meeting update, else the subject of the message or event that the guarded read shows. A red message shows no
// subject, so its heading has none.
func TestConsoleActionHeadingNamesTheSubject(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	for _, tc := range []struct{ op, params, want string }{
		{"mail.send", `{"to":["a@example.test"],"subject":"Plan","body":"b"}`, `<h1>Send <span class="what">Plan</span> <span class="opname">mail.send</span></h1>`},
		{"draft.new", `{"to":["a@example.test"],"subject":"","body":"b"}`, `<h1>New draft <span class="opname">draft.new</span></h1>`},
		{"calendar.cancel", `{"id":"o","comment":"c"}`, `<h1>Cancel <span class="what">private-subject-event</span> <span class="opname">calendar.cancel</span></h1>`},
		{"calendar.update", `{"id":"o","subject":"New title"}`, `<h1>Update <span class="what">New title</span> <span class="opname">calendar.update</span></h1>`},
		{"mail.trash", `{"ids":["r"]}`, `<h1>Trash <span class="opname">mail.trash</span></h1>`},
	} {
		id := pend(t, f.d.s, tc.op, tc.params)[0]
		if page := f.get(t, "/a/"+id, nil).Body.String(); !strings.Contains(page, tc.want) {
			t.Errorf("%s: action page misses %q: %s", tc.op, tc.want, page)
		}
	}
}

// The action page shows the same Risk as the list's open row: the value out of 1 and the label, or Check failed, the fixed
// code of a failed check after the label. A trash gets no action check, so its page has no Risk, whatever the verdict
// of its message.
// It never names the screening service.
func TestConsoleActionShowsTheRisk(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	for _, tc := range []struct {
		score float64
		want  string
	}{
		{0.62, `<dt>Risk</dt><dd class="bad">▲ 0.62 of 1 · Needs review</dd>`},
		{0.08, `<dt>Risk</dt><dd>0.08 of 1 · Checked</dd>`},
		{2, `<dt>Risk</dt><dd class="bad">▲ Check failed · Needs review · ValidationError</dd>`},
	} {
		f.setScore(tc.score)
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		page := f.get(t, "/a/"+id, nil).Body.String()
		if !strings.Contains(page, tc.want) {
			t.Errorf("action page misses %q: %s", tc.want, page)
		}
		if m := oldWord(page); m != "" {
			t.Errorf("action page shows %q: %s", m, page)
		}
	}
	for _, tc := range []struct{ op, params, want string }{
		{"draft.delete", `{"id":"draft-1"}`, `<dt>Risk</dt><dd>— · Not checked</dd>`},
		{"mail.trash", `{"ids":["g"]}`, ""},
		{"mail.trash", `{"ids":["r"]}`, ""},
		{"mail.trash", `{"ids":["s"]}`, ""},
		{"mail.trash", `{"ids":["gone"]}`, ""},
	} {
		id := pend(t, f.d.s, tc.op, tc.params)[0]
		w := f.get(t, "/a/"+id, nil)
		page := w.Body.String()
		if w.Code != 200 || !strings.Contains(page, "<dt>Account</dt>") {
			t.Errorf("%s %s: no action page: %d %s", tc.op, tc.params, w.Code, page)
		}
		if tc.want == "" && (strings.Contains(page, "<dt>Risk</dt>") || !strings.Contains(page, "<h1>Trash ")) || tc.want != "" && !strings.Contains(page, tc.want) {
			t.Errorf("%s %s: action page Risk is not %q: %s", tc.op, tc.params, tc.want, page)
		}
		if m := oldWord(page); m != "" {
			t.Errorf("%s %s: action page shows %q: %s", tc.op, tc.params, m, page)
		}
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

// A check made by the fake engine carries the fake tag before the value, and the page never names the screening
// service.
func TestConsoleActionTagsTheFakeCheck(t *testing.T) {
	f := fakeConsole(t, fakeHome(t), fakeScreen+draftApproveOnly)
	for body, want := range map[string]string{
		"See you at noon":        `<dt>Risk</dt><dd><span class="fake">fake</span>0.05 of 1 · Checked</dd>`,
		"Your new password is x": `<dt>Risk</dt><dd class="bad"><span class="fake">fake</span>▲ 0.90 of 1 · Needs review</dd>`,
		"SCREENFAIL":             `<dt>Risk</dt><dd class="bad"><span class="fake">fake</span>▲ Check failed · Needs review · TypeSafeAPIError 503</dd>`,
	} {
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"`+body+`"}`)[0]
		page := f.get(t, "/a/"+id, nil).Body.String()
		if !strings.Contains(page, want) || oldWord(page) != "" || strings.Contains(page, "keywords") {
			t.Errorf("%q: action page misses %q or names the engine: %s", body, want, page)
		}
	}
}

// The tag follows the engine that made the check, not the engine of the running server: an action checked by the
// fake engine keeps its tag after a restart with Jev screening.
func TestConsoleActionKeepsTheFakeTagAfterARestartWithJev(t *testing.T) {
	home := fakeHome(t)
	before := fakeConsole(t, home, fakeScreen+draftApproveOnly)
	id := pend(t, before.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"See you at noon"}`)[0]
	if err := before.d.s.Close(); err != nil {
		t.Fatal(err)
	}
	after := fakeConsole(t, home, draftApproveOnly)
	page := after.get(t, "/a/"+id, nil).Body.String()
	if want := `<dt>Risk</dt><dd><span class="fake">fake</span>0.05 of 1 · Checked</dd>`; !strings.Contains(page, want) || oldWord(page) != "" {
		t.Fatalf("action page misses %q or names the engine: %s", want, page)
	}
}

// iframes are the frame tags of a page; frameTag is a frame whose attribute values hold no quote or angle bracket,
// so that no value can end its attribute or the tag, and frameAttr one of its attributes.
var (
	iframes   = regexp.MustCompile(`<iframe[^>]*>`)
	frameTag  = regexp.MustCompile(`^<iframe( [a-z]+="[^"<>]*")*>$`)
	frameAttr = regexp.MustCompile(` ([a-z]+)="([^"<>]*)"`)
)

// renderedCaption is the fixed caption of the Rendered preview.
const renderedCaption = "<figcaption>Rendered preview: links, images, and forms are off. Hidden text shows only in the source.</figcaption>"

// renderedFrame is the attributes of the one frame of an action page, or fails.
func renderedFrame(t *testing.T, page string) map[string]string {
	t.Helper()
	frames := iframes.FindAllString(page, -1)
	if len(frames) != 1 || !frameTag.MatchString(frames[0]) {
		t.Fatalf("frames=%q", frames)
	}
	attrs := map[string]string{}
	for _, m := range frameAttr.FindAllStringSubmatch(frames[0], -1) {
		attrs[m[1]] = m[2]
	}
	return attrs
}

// An HTML body shows as a Rendered preview above its escaped source: one frame with an empty sandbox, no referrer,
// a title, and the body as an escaped srcdoc that hostile text cannot leave, under the fixed caption. Approve and
// Deny come after the source. A plain-text body gets no frame.
func TestConsoleActionShowsAnHTMLBodyInASandboxedFrameAboveItsSource(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	body := `<p class="x">Hi &amp; bye</p>" onload="alert(1)" x='<script>alert(1)</script><img src=x onerror=alert(1)>`
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	id := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","html":true,"body":`+string(b)+`}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	attrs := renderedFrame(t, page)
	if attrs["sandbox"] != "" || attrs["referrerpolicy"] != "no-referrer" || attrs["title"] == "" || html.UnescapeString(attrs["srcdoc"]) != body {
		t.Fatalf("frame=%q", attrs)
	}
	if _, ok := attrs["sandbox"]; !ok {
		t.Fatalf("frame has no sandbox: %q", attrs)
	}
	source := "<figcaption>Body, HTML source as the agent wrote it</figcaption><pre>&lt;p class=&#34;x&#34;&gt;Hi &amp;amp; bye&lt;/p&gt;&#34; onload=&#34;alert(1)&#34; x=&#39;&lt;script&gt;"
	caption, frame, pre, decision := strings.Index(page, renderedCaption), strings.Index(page, "<iframe"), strings.Index(page, source), strings.Index(page, `name="decision"`)
	if caption < 0 || frame < caption || pre < frame || decision < pre {
		t.Fatalf("caption=%d frame=%d source=%d decision=%d: %s", caption, frame, pre, decision, page)
	}
	if strings.Contains(page, "<img") || strings.Contains(strings.Replace(page, consoleScriptTag, "", 1), "<script") {
		t.Fatalf("body is markup outside the frame: %s", page)
	}
	plain := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"<p>Hi</p>"}`)[0]
	page = f.get(t, "/a/"+plain, nil).Body.String()
	if strings.Contains(page, "<iframe") || strings.Contains(page, "Rendered preview") || !strings.Contains(page, "<pre>&lt;p&gt;Hi&lt;/p&gt;</pre>") {
		t.Fatalf("plain-text body: %s", page)
	}
}

// A draft send whose stored draft is HTML shows the draft body in the Rendered preview too.
func TestConsoleActionShowsTheRenderedPreviewOfAnHTMLDraftSend(t *testing.T) {
	f := fakeConsole(t, fakeHome(t), fakeScreen+"[policy]\nmail_draft='allow'\nmail_send='approve'\n")
	body := "<p>See you at <b>noon</b></p>"
	status, made := accountRPC(t, f.d.s, "draft.new", "", `{"to":["a@example.test"],"subject":"s","html":true,"body":"`+body+`"}`)
	var draft struct{ ID string }
	if status != 200 || json.Unmarshal([]byte(made), &draft) != nil || draft.ID == "" {
		t.Fatalf("draft: %d %s", status, made)
	}
	id := pend(t, f.d.s, "draft.send", `{"id":"`+draft.ID+`"}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	if attrs := renderedFrame(t, page); html.UnescapeString(attrs["srcdoc"]) != body || !strings.Contains(page, renderedCaption) {
		t.Fatalf("frame=%q page=%s", attrs, page)
	}
	for _, want := range []string{"<figcaption>Body, HTML source as the agent wrote it</figcaption><pre>&lt;p&gt;See you at &lt;b&gt;noon&lt;/b&gt;&lt;/p&gt;</pre>",
		"The draft as the daemon read it for the action check when the agent asked to send it."} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q: %s", want, page)
		}
	}
}

// Agent text renders as literal text everywhere on the action page: request fields, the body and its frame, the
// meeting comment, and the stored draft fields. The page has no script, no event handler attribute, and links only to
// the console.
func TestConsoleActionShowsHostileAgentTextAsLiteralText(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	h, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	escaped := `&lt;img src=x onerror=alert(1)&gt; {{7*7}} ${7*7}`
	for _, tc := range []struct{ op, params string }{
		{"mail.send", `{"to":[` + string(h) + `],"cc":[` + string(h) + `],"subject":` + string(h) + `,"body":` + string(h) + `}`},
		{"draft.new", `{"to":["a@example.test"],"subject":` + string(h) + `,"html":true,"body":` + string(h) + `}`},
		{"calendar.invite", `{"to":[` + string(h) + `],"subject":` + string(h) + `,"body":` + string(h) + `,"location":` + string(h) + `,"start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`},
		{"calendar.respond", `{"id":"e","response":"decline","comment":` + string(h) + `}`},
		{"mail.reply", `{"id":"g","cc":[` + string(h) + `],"bcc":[` + string(h) + `],"body":` + string(h) + `}`},
	} {
		id := pend(t, f.d.s, tc.op, tc.params)[0]
		page := f.get(t, "/a/"+id, nil).Body.String()
		if strings.Contains(page, "<img") || !strings.Contains(page, escaped) {
			t.Errorf("%s: hostile text is markup or missing: %s", tc.op, page)
		}
		onlyConsoleLinks(t, page)
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

func TestConsoleActionShowsTheReferencedEvent(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	id := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"tentative","comment":"CANARY comment"}`)[0]
	page := f.get(t, "/a/"+id, nil).Body.String()
	for _, want := range []string{"calendar.respond", "tentative", "CANARY comment", "private-subject-event", `<dt>Start</dt><dd><span class="ln">Tue 31 Mar 2026, 10:00</span></dd>`, `<dt>End</dt><dd><span class="ln">Tue 31 Mar 2026, 11:00</span></dd>`} {
		if !strings.Contains(page, want) {
			t.Errorf("action page misses %q: %s", want, page)
		}
	}
	if strings.Contains(page, "private-body-event") {
		t.Errorf("action page shows the event body: %s", page)
	}
}

// A referenced message that cannot be read does not block the decision.
func TestConsoleActionStillOffersTheDecisionWhenTheMessageIsUnavailable(t *testing.T) {
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
	if !strings.Contains(page, `<dd class="state done">Done</dd>`) || !strings.Contains(page, "draft-1") || strings.Contains(page, `value="approve"`) {
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
	if !strings.Contains(page, `<dd class="state denied">Denied</dd>`) || strings.Contains(page, `value="approve"`) || f.writes() != 0 {
		t.Fatalf("writes=%d page=%s", f.writes(), page)
	}
	if w := f.decide(t, id, "approve", nil); w.Code != 409 || f.writes() != 0 {
		t.Fatalf("approve after deny: %d writes=%d", w.Code, f.writes())
	}
}

// approvalStates are the states of the approval records of the action in the audit log, in order.
func (f *consoleFixture) approvalStates(t *testing.T, id string) []string {
	t.Helper()
	_, records := auditOf(t, f.d.home)
	out := []string{}
	for _, r := range records {
		if r["event"] == "approval" && r["approval_id"] == id {
			out = append(out, r["state"].(string))
		}
	}
	return out
}

// Approving an action that needs review takes the confirm field of the "I reviewed it" box: without it the console
// refuses and nothing runs or is recorded; with it the action runs once. Deny never needs it, nor does a Mailbox
// operation, even of a red message.
func TestConsoleApproveOfAnActionThatNeedsReviewNeedsTheConfirm(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	f.setScore(.62)
	risk := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	denied := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(2)
	failed := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"c"}`)[0]
	f.setScore(.1)
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["r"]}`)[0]
	for _, id := range []string{risk, failed} {
		for body, want := range map[string]string{
			"decision=approve":                                       "review required",
			"decision=approve&confirm=yes":                           "invalid request",
			"decision=approve&confirm=":                              "invalid request",
			"decision=approve&confirm=flagged&confirm=flagged":       "invalid request",
			"decision=approve&return=%2F&return=%2F&confirm=flagged": "invalid request",
		} {
			if w := f.post(t, "/a/"+id, body, nil); w.Code != 400 || strings.TrimSpace(w.Body.String()) != want {
				t.Errorf("%q: %d %s; want 400 %s", body, w.Code, w.Body, want)
			}
		}
		assertState(t, f.d.s, id, `{"state":"pending"}`)
		if got := f.approvalStates(t, id); !slices.Equal(got, []string{"created"}) || f.writes() != 0 {
			t.Fatalf("refused approve: records=%q writes=%d", got, f.writes())
		}
	}
	if w := f.post(t, "/a/"+risk, "decision=approve&confirm=flagged", nil); w.Code != 303 || w.Header().Get("Location") != "/a/"+risk || f.writes() != 1 {
		t.Fatalf("confirmed approve: %d %q writes=%d", w.Code, w.Header().Get("Location"), f.writes())
	}
	if w := f.post(t, "/a/"+risk, "decision=approve&confirm=flagged", nil); w.Code != 409 || f.writes() != 1 {
		t.Fatalf("second confirmed approve: %d writes=%d", w.Code, f.writes())
	}
	if got := f.approvalStates(t, risk); !slices.Equal(got, []string{"created", "approved", "done"}) {
		t.Fatalf("records=%q", got)
	}
	if w := f.decide(t, denied, "deny", nil); w.Code != 303 || f.writes() != 1 {
		t.Fatalf("deny: %d writes=%d", w.Code, f.writes())
	}
	assertState(t, f.d.s, denied, `{"state":"denied"}`)
	if w := f.decide(t, trash, "approve", nil); w.Code != 303 || f.writes() != 2 {
		t.Fatalf("trash of a red message: %d writes=%d %s", w.Code, f.writes(), w.Body)
	}
}

// hostileReturns are return fields that are not a list page of the console with valid filters, or are too long.
var hostileReturns = []string{
	"//evil.example", `/\evil.example`, "http://evil.example", "https://evil.example/?account=work", "/?x=1&select=messages",
	"/a/0123456789abcdef0123456789abcdef", "%2F%2Fevil", "/%2F%2Fevil", "/?account=<script>", "/?account=work\r\nLocation: http://evil.example",
	"/?account=work\x00", "/?account=work&account=work", "/?account=work//evil", "/?account=work" + strings.Repeat("&", 600), "", " /", "?account=work",
}

// A decision from the list returns to the list with its filters, rebuilt from the validated filters so that nothing
// else of the field reaches the redirect, select included. Any other return field is ignored: the decision returns
// to the action page, and the response never repeats the field.
func TestConsoleDecisionReturnsToTheListWithItsFilters(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	decide := func(ret string) *httptest.ResponseRecorder {
		t.Helper()
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		w := f.post(t, "/a/"+id, "decision=deny&return="+url.QueryEscape(ret), nil)
		if w.Code != 303 || strings.Contains(w.Body.String(), "evil") {
			t.Fatalf("return %q: %d %s", ret, w.Code, w.Body)
		}
		assertState(t, f.d.s, id, `{"state":"denied"}`)
		if loc := w.Header().Get("Location"); loc == "/a/"+id {
			w.Header().Set("Location", "action page")
		}
		return w
	}
	for ret, want := range map[string]string{
		"/":                                    "/",
		"/?":                                   "/",
		"/?account=work":                       "/?account=work",
		"/?status=others&account=work":         "/?account=work&status=others",
		"/?account=work&select=messages":       "/?account=work",
		"/?status=review&approved=3&handled=1": "/?status=review",
		"/?account=work" + strings.Repeat("&", 400): "/?account=work",
	} {
		if got := decide(ret).Header().Get("Location"); got != want {
			t.Errorf("return %q: Location %q; want %q", ret, got, want)
		}
	}
	for _, ret := range hostileReturns {
		if got := decide(ret).Header().Get("Location"); got != "action page" {
			t.Errorf("return %q: Location %q; want the action page", ret, got)
		}
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
	if !strings.Contains(page, `<dd class="state failed">Failed</dd>`) || !strings.Contains(page, `<dt>Error</dt><dd class="bad">Graph error 404</dd>`) || strings.Contains(page, "private-provider-canary") {
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
