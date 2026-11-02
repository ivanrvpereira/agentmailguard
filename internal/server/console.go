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
// it refuses every request. The username, the password hash, and the hosts come from the state of each request.
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
	// Reply is true for a reply. Recipients are who it goes to; RecipientsMissing is true when they could not be read.
	Reply             bool
	Recipients        []field
	RecipientsMissing bool
	// DraftSend is true for a draft send. Draft is the stored draft fields that Jev checked, empty when none are stored.
	DraftSend bool
	Draft     []field
	Pending   bool
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
	page.Args = requestFields(args)
	page.RefKind, page.Ref, page.RefMissing = c.reference(ctx, item, args)
	if item.Op == "draft.reply" || item.Op == "mail.reply" {
		page.Reply = true
		page.Recipients, page.RecipientsMissing = c.replyRecipients(ctx, item, args)
	}
	if item.Op == "draft.send" {
		page.DraftSend, page.Draft = true, requestFields(storedFields(item.Draft))
	}
	render(w, status, "item", page)
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
	return []field{{"to", r.To}, {"cc", r.CC}, {"bcc (hidden recipients)", r.BCC}}, false
}

// requestFields shows each stored argument under its name: the recipients to, cc, and bcc first, bcc marked as
// hidden recipients, and an HTML body marked as source text, which the page escapes and never renders.
func requestFields(args map[string]any) []field {
	labels := map[string]string{"bcc": "bcc (hidden recipients)"}
	if args["html"] == true {
		labels["body"] = "body (HTML source, not rendered)"
	}
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

// consoleTemplates are the console pages, embedded in the binary.
//
//go:embed console/*.html
var consoleTemplates embed.FS

// pages are the console templates. html/template escapes every value; stored text is never markup, and the
// only links are the console's own pages.
var pages = template.Must(template.New("").ParseFS(consoleTemplates, "console/*.html"))
