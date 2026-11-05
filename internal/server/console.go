package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
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
	"github.com/ivanrvpereira/agentmailguard/internal/config"
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
// it refuses every request. The username, the password hash, and the hosts come from the state of each request.
func (s *Server) Console() http.Handler { return &console{s: s} }

// ConsoleAddress is the [console].listen address that the first Load read.
func (s *Server) ConsoleAddress() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listen
}

// Current is the configuration of the current state, with the console address that the first Load fixed, and
// whether that state has a console password. Before the first Load succeeds it is the zero Config and false.
func (s *Server) Current() (config.Config, bool) {
	if st := s.current.Load(); st != nil {
		return st.config, st.consoleHash != ""
	}
	return config.Config{}, false
}

// consoleCSP allows only the console's own script and no Trusted Types policy, so that every HTML sink throws. The
// Rendered preview frame inherits it and loads nothing.
const consoleCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; require-trusted-types-for 'script'; trusted-types 'none'"

func (c *console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", consoleCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	// same-origin, not no-referrer: under no-referrer a browser sends `Origin: null` on the console's own form posts, which
	// sameOrigin must refuse (https://fetch.spec.whatwg.org/#origin-header). Other sites still get no referrer.
	h.Set("Referrer-Policy", "same-origin")
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
	if status := c.signIn(ctx, r, st.config.Console.Username, st.consoleHash); status != 0 {
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

// signIn returns 0 for the configured username with the right password, 429 while the sign-in limit holds, and 401
// otherwise. A wrong username or password counts as a failure; each one gets an audit record without request details.
// The password is checked also for a wrong username, so the answer time does not tell which one was wrong.
func (c *console) signIn(ctx context.Context, r *http.Request, username, hash string) int {
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
	user, password, ok := r.BasicAuth()
	if !ok {
		return 401
	}
	right := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	if subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1 && right {
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
		c.list(ctx, w, r)
		return
	}
	if r.URL.Path == "/assets/console.js" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			plain(w, 405, "method refused")
			return
		}
		write(w, 200, "text/javascript; charset=utf-8", consoleScript)
		return
	}
	if r.URL.Path == "/batch" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			plain(w, 405, "method refused")
			return
		}
		c.batch(ctx, w, r)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/a/")
	if !ok || !approvalIDPattern.MatchString(id) {
		plain(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		c.action(ctx, w, id, 200, "")
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

// readForm reads a urlencoded form body of at most limit bytes whose fields are all in allowed. ok is false for any
// other content type, a larger body, a malformed body, or another field.
func readForm(w http.ResponseWriter, r *http.Request, limit int64, allowed ...string) (url.Values, bool) {
	typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/x-www-form-urlencoded" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, false
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, false
	}
	for key := range form {
		if !slices.Contains(allowed, key) {
			return nil, false
		}
	}
	return form, true
}

// confirmReviewed is the only value of the confirm field: the owner ticked "I reviewed it".
const confirmReviewed = "flagged"

// decide approves or denies the action from a form with one decision, an optional confirm, and an optional return,
// then shows the action page again. An approve of an action that needs review runs only with the confirm.
func (c *console) decide(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	form, ok := readForm(w, r, maxDecisionBytes, "decision", "confirm", "return")
	if !ok || len(form["decision"]) != 1 || len(form["confirm"]) > 1 || len(form["return"]) > 1 || (form.Has("confirm") && form.Get("confirm") != confirmReviewed) {
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
	if form.Get("decision") == "approve" && !form.Has("confirm") {
		item, err := c.s.cache.GetPending(ctx, id)
		if err != nil && !errors.Is(err, cache.ErrPendingNotFound) {
			plain(w, 500, "approvals unavailable")
			return
		}
		if err == nil && item.State == cache.StatePending && needsReview(item) {
			plain(w, 400, "review required")
			return
		}
	}
	// A run that started finishes even when the browser goes away; Close still cancels it.
	err := run(context.WithoutCancel(ctx), id)
	switch {
	case errors.Is(err, ErrApprovalNotFound):
		plain(w, 404, "not found")
	case errors.Is(err, ErrAlreadyHandled):
		c.action(ctx, w, id, 409, "This action was already handled. Nothing ran again.")
	case errors.Is(err, ErrUnavailable):
		plain(w, 503, err.Error())
	default:
		// Done, or a failed run whose state and code the action page shows, or the list that the form came from.
		back := "/a/" + id
		if q, ok := c.formReturn(form); ok {
			back = q.url()
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
}

// formReturn is the list filter of the form's return field; ok is false without a valid one.
func (c *console) formReturn(form url.Values) (listQuery, bool) {
	st := c.s.current.Load()
	if st == nil || !form.Has("return") {
		return listQuery{}, false
	}
	return returnQuery(form.Get("return"), st.config)
}

type field struct {
	Label string
	Lines []string
}

// actionPage is the action page of a stored action. Row is the action as the list's open row shows it: the label,
// alias, Score, request fields, draft fields, and the body under its caption.
type actionPage struct {
	Row                                                        listRow
	ID, Op, StateLabel, Created, Updated, Result, Code, Notice string
	State                                                      cache.PendingState
	// Ref is the referenced message or event, RefKind names it, RefMissing is true when the read failed.
	RefKind    string
	Ref        []field
	RefMissing bool
	// Reply is true for a reply. Recipients are who it goes to; RecipientsMissing is true when they could not be read.
	Reply             bool
	Recipients        []field
	RecipientsMissing bool
	// DraftSend is true for a draft send, whose stored draft fields are in Row.Draft.
	DraftSend bool
	Pending   bool
}

// longTime is a full date and time, as in "Tue 31 Mar 2026, 12:00".
const longTime = "Mon 2 Jan 2006, 15:04"

// longTimeText is the RFC 3339 time v as longTime: in loc, or in the offset of v when loc is nil. A v that is not
// RFC 3339 stays as it is.
func longTimeText(v string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format(longTime)
}

// referenceTimeout bounds the guarded read of the referenced message or event.
const referenceTimeout = 30 * time.Second

func (c *console) action(ctx context.Context, w http.ResponseWriter, id string, status int, notice string) {
	item, err := c.s.cache.GetPending(ctx, id)
	if err != nil {
		if errors.Is(err, cache.ErrPendingNotFound) {
			plain(w, 404, "not found")
		} else {
			plain(w, 500, "approvals unavailable")
		}
		return
	}
	var aliases map[string]string
	if st := c.s.current.Load(); st != nil {
		aliases = accountAliases(st.config)
	}
	now := c.s.options.Now()
	page := actionPage{Row: listedRow(item, aliases, now), ID: item.ID, Op: item.Op, State: item.State, Notice: notice, Pending: item.State == cache.StatePending,
		Created: item.Created.In(now.Location()).Format(longTime), Updated: item.Updated.In(now.Location()).Format(longTime)}
	if page.Row.Label == "" {
		page.Row.Label = item.Op
	}
	page.StateLabel = stateLabel(item.State)
	// The Rendered preview shows the HTML above its source, so the source caption does not say "not rendered".
	if page.Row.BodyHTML {
		page.Row.Caption = strings.TrimSuffix(page.Row.Caption, notRendered)
	}
	if item.Result != nil {
		switch item.State {
		case cache.StateDone:
			page.Result = *item.Result
		case cache.StateFailed:
			page.Code = *item.Result
		}
	}
	args := page.Row.args
	page.RefKind, page.Ref, page.RefMissing = c.reference(ctx, item, args)
	if item.Op == "draft.reply" || item.Op == "mail.reply" {
		page.Reply = true
		page.Recipients, page.RecipientsMissing = c.replyRecipients(ctx, item, args)
	}
	page.DraftSend = item.Op == "draft.send"
	render(w, status, "action", page)
}

// replyRecipients works out who a reply goes to: the To and CC that the guard reads from the original message, with
// the request's cc and bcc added, bcc marked as hidden recipients. missing is true when they cannot be read.
func (c *console) replyRecipients(ctx context.Context, item cache.Pending, args map[string]any) (out []field, missing bool) {
	id, _ := args["id"].(string)
	all, _ := args["all"].(bool)
	st := c.s.current.Load()
	if st == nil || st.accounts[item.Account] == nil || id == "" {
		return nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, referenceTimeout)
	defer cancel()
	r, err := st.accounts[item.Account].guard.ReplyRecipients(ctx, id, all)
	if err != nil {
		return nil, true
	}
	r = r.With(texts(args["cc"]), texts(args["bcc"]))
	return openFields([]field{{"to", r.To}, {"cc", r.CC}, {"bcc (hidden recipients)", r.BCC}}), false
}

// requestFields shows each stored argument under its name: the recipients to, cc, and bcc first, bcc marked as
// hidden recipients.
func requestFields(args map[string]any) []field {
	labels := map[string]string{"bcc": "bcc (hidden recipients)"}
	recipients := []string{"to", "cc", "bcc"}
	keys := []string{}
	for _, key := range recipients {
		if _, ok := args[key]; ok {
			keys = append(keys, key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(args)) {
		if !slices.Contains(recipients, key) {
			keys = append(keys, key)
		}
	}
	out := make([]field, 0, len(keys))
	for _, key := range keys {
		label, ok := labels[key]
		if !ok {
			label = key
		}
		out = append(out, field{label, texts(args[key])})
	}
	return out
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
	case "draft.reply", "draft.forward", "draft.update", "draft.delete", "mail.reply", "mail.forward":
	default:
		return "", nil, false
	}
	if ref == "" {
		return kind, nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, referenceTimeout)
	defer cancel()
	result, err := c.guardedRead(ctx, op, item.Account, ref)
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
		add("Received", new(longTimeText(v.ReceivedAt, c.s.options.Now().Location())))
	case views.CalendarView:
		add("Verdict", new(string(v.Verdict)))
		add("Subject", v.Subject)
		add("Start", new(longTimeText(v.Start, nil)))
		add("End", new(longTimeText(v.End, nil)))
		add("Organizer", new(addressText(v.Organizer)))
	default:
		return kind, nil, true
	}
	return kind, out, false
}

// guardedRead reads the message or event id of the account through the guarded read path of op, mail.get or
// calendar.get, as the agent would. The result is the view of its verdict.
func (c *console) guardedRead(ctx context.Context, op, account, id string) (any, error) {
	params, err := json.Marshal(idParam{id})
	if err != nil {
		return nil, err
	}
	return c.s.runOn(ctx, op, account, params)
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

// consoleTemplates are the console pages, embedded in the binary.
//
//go:embed console/*.html
var consoleTemplates embed.FS

// consoleScript is the one script of the console pages, served at /assets/console.js. It only adds convenience: every
// page works without it, and the server enforces every rule.
//
//go:embed console/console.js
var consoleScript []byte

// pages are the console templates. html/template escapes every value; stored text is never markup, and the
// only links are the console's own pages.
var pages = template.Must(template.New("").ParseFS(consoleTemplates, "console/*.html"))
