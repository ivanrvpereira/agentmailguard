package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
	"golang.org/x/crypto/bcrypt"
)

// Sign-in limit: after maxFailures wrong passwords within failureWindow, every request is refused until the
// oldest of them leaves the window.
const (
	maxFailures   = 5
	failureWindow = time.Minute
)

// console is the approval web console. It shows stored actions and approves or denies them.
type console struct{ s *Server }

// signinLimit counts wrong console passwords. It lives as long as the daemon, so a reload does not reset it.
type signinLimit struct {
	// mu serializes the limit check and the password check, so parallel guesses cannot pass the limit.
	mu       sync.Mutex
	failures []time.Time
}

// consoleUnset is the answer to every console request while no console password exists.
const consoleUnset = "console password not set; run amgd config set console.password"

// Console returns the approval console handler. It is always on: without a console password in the current state,
// it refuses every request. The password hash and the hosts come from the state of each request.
func (s *Server) Console() http.Handler { return &console{s: s} }

// ConsoleAddress is the [console].listen address that the first Load read.
func (s *Server) ConsoleAddress() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listen
}

func (c *console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	defer func() {
		if recover() != nil {
			plain(w, 500, "operation failed")
		}
	}()
	st := c.s.current.Load()
	if st == nil {
		plain(w, 503, ErrUnavailable.Error())
		return
	}
	hosts := append([]string{c.s.ConsoleAddress()}, st.config.Console.Hosts...)
	if !slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, r.Host) }) {
		plain(w, 403, "host refused")
		return
	}
	if st.consoleHash == "" {
		plain(w, 503, consoleUnset)
		return
	}
	ctx, done, err := c.s.admit(r.Context())
	if err != nil {
		plain(w, 503, err.Error())
		return
	}
	defer done()
	if status := c.signIn(ctx, r, st.consoleHash); status != 0 {
		if status == 401 {
			h.Set("WWW-Authenticate", `Basic realm="agentmailguard approvals", charset="UTF-8"`)
			plain(w, 401, "sign-in required")
		} else {
			plain(w, 429, "too many failed sign-ins; try again later")
		}
		return
	}
	c.route(ctx, w, r)
}

// signIn returns 0 for the right password, 429 while the sign-in limit holds, and 401 otherwise. Only a
// wrong password counts as a failure; each one gets an audit record without request details.
func (c *console) signIn(ctx context.Context, r *http.Request, hash string) int {
	l := c.s.signins
	l.mu.Lock()
	defer l.mu.Unlock()
	now := c.s.options.Now()
	recent := l.failures[:0]
	for _, at := range l.failures {
		if now.Sub(at) < failureWindow {
			recent = append(recent, at)
		}
	}
	l.failures = recent
	if len(l.failures) >= maxFailures {
		return 429
	}
	_, password, ok := r.BasicAuth()
	if !ok {
		return 401
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
		return 0
	}
	l.failures = append(l.failures, now)
	_ = c.s.audit.ConsoleSigninFailed(context.WithoutCancel(ctx))
	return 401
}

func (c *console) route(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !sameOrigin(r) {
		plain(w, 403, "origin refused")
		return
	}
	if r.URL.Path == "/" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			plain(w, 405, "method refused")
			return
		}
		c.list(ctx, w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/a/")
	if !ok || !approvalIDPattern.MatchString(id) {
		plain(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		c.item(ctx, w, id, 200, "")
	case http.MethodPost:
		c.decide(ctx, w, r, id)
	default:
		w.Header().Set("Allow", "GET, POST")
		plain(w, 405, "method refused")
	}
}

// sameOrigin reports whether the Origin header names the console at the Host of the request.
func sameOrigin(r *http.Request) bool {
	origin := strings.ToLower(r.Header.Get("Origin"))
	host := strings.ToLower(r.Host)
	return host != "" && (origin == "http://"+host || origin == "https://"+host)
}

// maxDecisionBytes bounds the decision form body.
const maxDecisionBytes = 4 << 10

// decide approves or denies the item from a form with the one field decision, then shows the item again.
func (c *console) decide(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/x-www-form-urlencoded" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		plain(w, 400, "invalid request")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDecisionBytes))
	if err != nil {
		plain(w, 400, "invalid request")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || len(form) != 1 || len(form["decision"]) != 1 {
		plain(w, 400, "invalid request")
		return
	}
	var run func(context.Context, string) error
	switch form.Get("decision") {
	case "approve":
		run = c.s.Approve
	case "deny":
		run = c.s.Deny
	default:
		plain(w, 400, "invalid request")
		return
	}
	// A run that started finishes even when the browser goes away; Close still cancels it.
	err = run(context.WithoutCancel(ctx), id)
	switch {
	case errors.Is(err, ErrApprovalNotFound):
		plain(w, 404, "not found")
	case errors.Is(err, ErrAlreadyHandled):
		c.item(ctx, w, id, 409, "This action was already handled. Nothing ran again.")
	case errors.Is(err, ErrUnavailable):
		plain(w, 503, err.Error())
	default:
		// Done, or a failed run whose state and code the item page shows.
		http.Redirect(w, r, "/a/"+id, http.StatusSeeOther)
	}
}

// resolvedListed is the number of handled actions that the list shows after the waiting ones.
const resolvedListed = 50

type listRow struct {
	ID, Account, Op, Recipients, Subject, Age string
	State                                     cache.PendingState
}

func (c *console) list(ctx context.Context, w http.ResponseWriter) {
	items, err := c.s.cache.ListPending(ctx, resolvedListed)
	if err != nil {
		plain(w, 500, "approvals unavailable")
		return
	}
	now := c.s.options.Now()
	rows := make([]listRow, 0, len(items))
	for _, item := range items {
		args := storedFields(item.Args)
		// Optional attendees receive the invitation too.
		recipients := append(texts(args["to"]), texts(args["optional"])...)
		if args["to"] == nil {
			recipients = texts(args["add"])
		}
		subject, _ := args["subject"].(string)
		rows = append(rows, listRow{ID: item.ID, Account: item.Account, Op: item.Op, Recipients: strings.Join(recipients, ", "), Subject: subject, Age: age(now.Sub(item.Created)), State: item.State})
	}
	render(w, 200, "list", rows)
}

type field struct {
	Label string
	Lines []string
}

type itemPage struct {
	// Jev is the result of the Jev action check, empty when no check ran.
	ID, Account, Op, Created, Updated, Result, Code, Notice, Jev string
	State                                                        cache.PendingState
	Args                                                         []field
	// Ref is the referenced message or event, RefKind names it, RefMissing is true when the read failed.
	RefKind    string
	Ref        []field
	RefMissing bool
	Pending    bool
}

// referenceTimeout bounds the guarded read of the referenced message or event.
const referenceTimeout = 30 * time.Second

func (c *console) item(ctx context.Context, w http.ResponseWriter, id string, status int, notice string) {
	item, err := c.s.cache.GetPending(ctx, id)
	if err != nil {
		if errors.Is(err, cache.ErrPendingNotFound) {
			plain(w, 404, "not found")
		} else {
			plain(w, 500, "approvals unavailable")
		}
		return
	}
	page := itemPage{ID: item.ID, Account: item.Account, Op: item.Op, State: item.State, Notice: notice, Pending: item.State == cache.StatePending,
		Created: item.Created.Format(time.RFC3339), Updated: item.Updated.Format(time.RFC3339)}
	if item.Result != nil {
		switch item.State {
		case cache.StateDone:
			page.Result = *item.Result
		case cache.StateFailed:
			page.Code = *item.Result
		}
	}
	page.Jev = jevText(item.Jev)
	args := storedFields(item.Args)
	for _, key := range slices.Sorted(maps.Keys(args)) {
		page.Args = append(page.Args, field{key, texts(args[key])})
	}
	page.RefKind, page.Ref, page.RefMissing = c.reference(ctx, item, args)
	render(w, status, "item", page)
}

// reference reads the message or event that the action names through the guarded read path. Only the
// summary fields are shown, never a body.
func (c *console) reference(ctx context.Context, item cache.Pending, args map[string]any) (string, []field, bool) {
	ref, _ := args["id"].(string)
	op, kind := "mail.get", "Message"
	switch item.Op {
	case "mail.trash":
		if ids, ok := args["ids"].([]any); ok && len(ids) > 0 {
			ref, _ = ids[0].(string)
		}
	case "calendar.update", "calendar.cancel", "calendar.respond":
		op, kind = "calendar.get", "Event"
	case "draft.reply", "draft.forward", "draft.update", "mail.reply", "mail.forward":
	default:
		return "", nil, false
	}
	if ref == "" {
		return kind, nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, referenceTimeout)
	defer cancel()
	params, err := json.Marshal(idParam{ref})
	if err != nil {
		return kind, nil, true
	}
	result, err := c.s.runOn(ctx, op, item.Account, params)
	if err != nil {
		return kind, nil, true
	}
	var out []field
	add := func(label string, value *string) {
		if value != nil && *value != "" {
			out = append(out, field{label, []string{*value}})
		}
	}
	switch v := result.(type) {
	case views.MailView:
		add("Verdict", new(string(v.Verdict)))
		if v.Sender != nil {
			add("Sender", new(addressText(*v.Sender)))
		}
		add("Subject", v.Subject)
		add("Received", new(v.ReceivedAt))
	case views.CalendarView:
		add("Verdict", new(string(v.Verdict)))
		add("Subject", v.Subject)
		add("Start", new(v.Start))
		add("End", new(v.End))
		add("Organizer", new(addressText(v.Organizer)))
	default:
		return kind, nil, true
	}
	return kind, out, false
}

// jevText describes the Jev action check of an item, or is empty when no check ran. A check that the fake
// engine made says so, so that nobody takes a keyword match for a Jev check.
func jevText(c *model.ActionCheck) string {
	if c == nil {
		return ""
	}
	engine := "Jev: "
	if c.Fake {
		engine = "Fake check (keywords only): "
	}
	switch {
	case c.Error != nil:
		return engine + "check failed (" + *c.Error + ")"
	case c.Score == nil:
		return engine + "check failed"
	case c.Flag:
		return engine + fmt.Sprintf("flagged (score %.2f)", *c.Score)
	}
	return engine + fmt.Sprintf("not flagged (score %.2f)", *c.Score)
}

func addressText(a views.Address) string {
	if a.Name != nil && *a.Name != "" && *a.Name != a.Address {
		return *a.Name + " <" + a.Address + ">"
	}
	return a.Address
}

// storedFields decodes stored arguments into a map. Arguments that are not a JSON object give an empty map.
func storedFields(args json.RawMessage) map[string]any {
	d := json.NewDecoder(bytes.NewReader(args))
	d.UseNumber()
	var out map[string]any
	if d.Decode(&out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// texts renders a stored value as lines of text: a list gives one line per entry, a boolean yes or no.
func texts(v any) []string {
	switch v := v.(type) {
	case nil:
		return nil
	case string:
		return []string{v}
	case bool:
		if v {
			return []string{"yes"}
		}
		return []string{"no"}
	case json.Number:
		return []string{v.String()}
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, texts(e)...)
		}
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return []string{string(b)}
}

// age is a short rounded duration such as 45s, 12m, 3h, or 2d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func render(w http.ResponseWriter, status int, name string, data any) {
	var b bytes.Buffer
	if pages.ExecuteTemplate(&b, name, data) != nil {
		plain(w, 500, "operation failed")
		return
	}
	write(w, status, "text/html; charset=utf-8", b.Bytes())
}

// plain writes a short fixed text response.
func plain(w http.ResponseWriter, status int, text string) {
	write(w, status, "text/plain; charset=utf-8", []byte(text+"\n"))
}

// write sends a response. A slow client cannot hold the request, and so Close, longer than the deadline.
func write(w http.ResponseWriter, status int, contentType string, body []byte) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// pages are the console templates. html/template escapes every value; stored text is never markup, and the
// only links are the console's own pages.
var pages = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="format-detection" content="telephone=no,email=no,address=no,date=no">
<title>agentmailguard approvals</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;margin:0;padding:1rem;background:#f6f6f4;color:#1d1d1b;line-height:1.4}
main{margin:0 auto}
h1{font-size:1.3rem;margin:0 0 1rem}
h2{font-size:1.05rem;margin:1.5rem 0 .5rem}
a{color:#0b57d0}
table{border-collapse:collapse;width:100%;background:#fff}
th,td{text-align:left;padding:.5rem;border-bottom:1px solid #ddd;vertical-align:top;overflow-wrap:anywhere}
th{font-size:.8rem;text-transform:uppercase;color:#555}
dl{display:grid;grid-template-columns:minmax(6rem,max-content) 1fr;gap:.4rem 1rem;background:#fff;padding:.75rem;margin:0}
dt{font-weight:600;color:#444}
dd{margin:0;min-width:0}
pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font-family:ui-monospace,Menlo,monospace;font-size:.9rem}
.state{font-weight:600}
.pending{color:#9a5b00}.done{color:#11722b}.failed,.denied{color:#a11}
.notice{background:#fff3cd;padding:.75rem;border:1px solid #e0c060}
form{display:flex;gap:1rem;margin-top:1.5rem}
button{font-size:1.1rem;padding:.8rem 1.6rem;border-radius:.4rem;border:1px solid #888;cursor:pointer;flex:1}
.approve{background:#11722b;color:#fff;border-color:#11722b}
.deny{background:#fff;color:#a11;border-color:#a11}
@media (max-width:40rem){.wide{display:none}dl{grid-template-columns:1fr}dt{margin-top:.4rem}}
</style></head><body><main>{{end}}

{{define "list"}}{{template "head"}}
<h1>Approvals</h1>
{{if .}}<table>
<tr><th>Action</th><th class="wide">Account</th><th>To</th><th>Subject</th><th>State</th><th class="wide">Age</th></tr>
{{range .}}<tr><td><a href="/a/{{.ID}}">{{.Op}}</a></td><td class="wide">{{.Account}}</td><td>{{.Recipients}}</td><td>{{.Subject}}</td><td class="state {{.State}}">{{.State}}</td><td class="wide">{{.Age}}</td></tr>
{{end}}</table>{{else}}<p>No actions wait for approval.</p>{{end}}
</main></body></html>{{end}}

{{define "item"}}{{template "head"}}
<p><a href="/">All approvals</a></p>
<h1>{{.Op}}</h1>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<dl>
<dt>State</dt><dd class="state {{.State}}">{{.State}}</dd>
<dt>Account</dt><dd>{{.Account}}</dd>
<dt>Created</dt><dd>{{.Created}}</dd>
<dt>Updated</dt><dd>{{.Updated}}</dd>
{{if .Code}}<dt>Error</dt><dd><pre>{{.Code}}</pre></dd>{{end}}
{{if .Result}}<dt>Result</dt><dd><pre>{{.Result}}</pre></dd>{{end}}
</dl>
{{if .Jev}}<p>{{.Jev}}</p>{{end}}
<h2>Request</h2>
{{template "fields" .Args}}
{{if .RefKind}}<h2>{{.RefKind}}</h2>
{{if .RefMissing}}<p>{{.RefKind}} not available.</p>{{else}}{{template "fields" .Ref}}{{end}}{{end}}
{{if .Pending}}<form method="post" action="/a/{{.ID}}">
<button class="approve" type="submit" name="decision" value="approve">Approve</button>
<button class="deny" type="submit" name="decision" value="deny">Deny</button>
</form>{{end}}
</main></body></html>{{end}}

{{define "fields"}}<dl>{{range .}}<dt>{{.Label}}</dt><dd>{{range .Lines}}<pre>{{.}}</pre>{{end}}</dd>{{end}}</dl>{{end}}
`))
