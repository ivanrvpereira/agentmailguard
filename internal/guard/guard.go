// Package guard coordinates screened reads, validated writes, and content-free persistence.
package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

// Options configures a guard. A zero decision in Policy counts as deny. Nil Gates gives the guard its own gates.
type Options struct {
	Now    func() time.Time
	Policy config.Policy
	Gates  *Gates
}

// Gates are the mutation and security gates of one account. Guards of the same account share them, so a reload
// that builds new guards while old requests still run keeps every write of the account under the same gates.
type Gates struct{ mutation, security chan struct{} }

func NewGates() *Gates { return &Gates{make(chan struct{}, 1), make(chan struct{}, 1)} }

type Guard struct {
	account  string
	mailbox  model.Mailbox
	calendar model.Calendar
	screener model.Screener
	store    *cache.Cache
	log      *audit.Log
	now      func() time.Time
	policy   config.Policy
	mutation chan struct{}
	// Security writes and final automatic authorization share this gate, never network screening/validation.
	security chan struct{}
}

// Format hides nested adapters and their cached credentials for values and pointers.
func (Guard) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "Guard{redacted}") }

func New(account string, mailbox model.Mailbox, calendar model.Calendar, screener model.Screener, store *cache.Cache, log *audit.Log, opts Options) *Guard {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Gates == nil {
		opts.Gates = NewGates()
	}
	return &Guard{account, mailbox, calendar, screener, store, log, opts.Now, opts.Policy, opts.Gates.mutation, opts.Gates.security}
}

// GuardError contains only fixed refusal text or a safe failure code.
type GuardError string

// AuthFailed is the code of a provider auth failure: a refused or expired login, or a failed token request.
const AuthFailed = "AuthError"

func (e GuardError) Error() string { return string(e) }

//go:fix inline
func pointer[T any](v T) *T { return new(v) }
func safeCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "CancelledError"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	if err == nil {
		return "Error"
	}
	// A refused login keeps its code, so the server can tell the user to sign in again.
	if err == model.MailboxError("AuthError") || err == model.MailboxError("Gmail error AuthError") {
		return AuthFailed
	}
	if safe, ok := err.(GuardError); ok {
		code := string(safe)
		if code != "" && model.ValidateFailureText(&code) == nil {
			return code
		}
	}
	typ := reflect.TypeOf(err)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	name := typ.Name()
	if model.ValidateFailureText(&name) == nil && name != "" {
		return name
	}
	return "Error"
}

// screen is the part of a cache row that a view shows.
func screen(row cache.Row) views.Screen {
	return views.Screen{Verdict: row.Verdict, Auth: row.Auth, Error: row.Error}
}

func safeErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	return GuardError(safeCode(err))
}
func safeText(text *string) *string {
	if text == nil {
		return nil
	}
	if model.ValidateFailureText(text) != nil {
		return new("Error")
	}
	return new(*text)
}
func failure(code string) model.Screening {
	return model.Screening{Verdict: model.Error, Error: new(code)}
}
func validScreen(s model.Screening) model.Screening {
	switch s.Verdict {
	case model.Green, model.Yellow, model.Red, model.Sealed:
		if s.Score == nil || !(*s.Score >= 0 && *s.Score <= 1) || s.Error != nil {
			return failure("ValidationError")
		}
		if (*s.Score >= .4 && s.Verdict != model.Red) || (*s.Score < .4 && s.Verdict == model.Red) || (s.Verdict == model.Green && *s.Score >= .3) || (s.Verdict == model.Yellow && *s.Score < .3) {
			return failure("ValidationError")
		}
	case model.Error:
		s.Score = nil
		s.Error = safeText(s.Error)
		if s.Error == nil {
			s.Error = new("Error")
		}
	default:
		return failure("ValidationError")
	}
	return s
}
func (g *Guard) cached(ctx context.Context, ids []string) (map[string]cache.Row, error) {
	rows, err := g.store.Get(ctx, g.account, ids)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	for id, row := range rows {
		s := validScreen(model.Screening{Verdict: row.Verdict, Score: row.Score, Error: row.Error})
		if row.Auth.Validate() != nil {
			row.Auth = model.AuthResults{}
			s = failure("ValidationError")
		}
		row.Verdict, row.Score, row.Error = s.Verdict, s.Score, s.Error
		rows[id] = row
	}
	return rows, nil
}

func hidden(v model.Verdict) bool { return v == model.Red || v == model.Yellow || v == model.Sealed }
func checkPage(p model.PageRequest) error {
	if p.Size < 0 || !model.ValidPage(p.Page) {
		return GuardError("invalid request")
	}
	return nil
}
func render(body *string, kind *model.ContentType) (content.Rendered, error) {
	if kind == nil {
		return content.Rendered{}, GuardError("render ValueError")
	}
	return content.RenderBody(body, *kind)
}
func (g *Guard) put(ctx context.Context, m model.Message, s model.Screening) (cache.Row, error) {
	s = validScreen(s)
	auth := model.AuthResults{}
	if m.Auth != nil {
		auth = *m.Auth
	}
	if auth.Validate() != nil {
		auth = model.AuthResults{}
		s = failure("ValidationError")
	}
	row := cache.Row{MessageID: m.ID, ReceivedAt: m.ReceivedAt, Score: s.Score, Verdict: s.Verdict, Error: s.Error, Auth: auth, ScreenedAt: g.now()}
	if !m.IsDraft {
		if err := acquire(ctx, g.security); err != nil {
			return cache.Row{}, err
		}
		defer func() { <-g.security }()
		if err := g.store.Put(ctx, g.account, row); err != nil {
			return cache.Row{}, safeErr(ctx, err)
		}
	}
	return row, nil
}
func (g *Guard) screen(ctx context.Context, m model.Message, green *cache.Row) (cache.Row, *content.Rendered, error) {
	if err := ctx.Err(); err != nil {
		return cache.Row{}, nil, err
	}
	if m.Body == nil {
		return cache.Row{}, nil, GuardError("screening path needs a fetched body")
	}
	r, err := render(m.Body, m.ContentType)
	if err != nil {
		row, e := g.put(ctx, m, failure("render ValueError"))
		return row, nil, e
	}
	if green != nil && !m.IsDraft {
		return *green, &r, nil
	}
	s, err := g.screener.Screen(ctx, views.ScreeningState(m, r))
	if ctx.Err() != nil {
		return cache.Row{}, nil, ctx.Err()
	}
	if err != nil {
		s = failure(safeCode(err))
	}
	row, err := g.put(ctx, m, s)
	if row.Verdict != model.Green {
		return row, nil, err
	}
	return row, &r, err
}
func (g *Guard) fetch(ctx context.Context, ids []string, body bool) (map[string]model.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	messages, err := g.mailbox.Get(ctx, ids, body)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	out := map[string]model.Message{}
	for _, m := range messages {
		if !wanted[m.ID] {
			return nil, GuardError("ValueError")
		}
		if _, exists := out[m.ID]; exists {
			return nil, GuardError("ValueError")
		}
		out[m.ID] = m
	}
	if len(out) != len(wanted) {
		return nil, GuardError("ValueError")
	}
	return out, nil
}

// Each worker owns one result slot at a time. Drain siblings before returning an error.
func parallelItems(ctx context.Context, count int, work func(int) error) error {
	jobs := make(chan int)
	errs := make([]error, count)
	var workers sync.WaitGroup
	for range min(8, count) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() == nil {
					errs[i] = work(i)
				}
			}
		})
	}
queue:
	for i := range count {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *Guard) summaries(ctx context.Context, messages []model.Message, categories []string) ([]views.MailSummary, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range messages {
		if m.ID == "" || seen[m.ID] {
			return nil, GuardError("ValueError")
		}
		seen[m.ID] = true
		if !m.IsDraft {
			ids = append(ids, m.ID)
		}
	}
	rows, err := g.cached(ctx, ids)
	if err != nil {
		return nil, err
	}
	missing := []string{}
	for _, m := range messages {
		row, ok := rows[m.ID]
		if m.IsDraft || !ok || row.Verdict == model.Error {
			if m.Body == nil {
				missing = append(missing, m.ID)
			}
		}
	}
	fetched := map[string]model.Message{}
	if len(missing) > 0 {
		fetched, err = g.fetch(ctx, missing, true)
		if err != nil {
			return nil, err
		}
	}
	out := make([]views.MailSummary, len(messages))
	err = parallelItems(ctx, len(messages), func(i int) error {
		m := messages[i]
		row, ok := rows[m.ID]
		if m.IsDraft || !ok || row.Verdict == model.Error {
			if full, ok := fetched[m.ID]; ok {
				full.IsDraft = full.IsDraft || m.IsDraft
				m = full
			}
			var err error
			row, _, err = g.screen(ctx, m, nil)
			if err != nil {
				return err
			}
		}
		out[i] = views.Summary(m, screen(row), categories)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List screens one page of folder. The provider's next-page marker is provider data: one that model.ValidPage
// refuses fails the page.
func (g *Guard) List(ctx context.Context, folder string, page model.PageRequest) (views.MailPage, error) {
	if err := checkPage(page); err != nil {
		return views.MailPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return views.MailPage{}, err
	}
	found, err := g.mailbox.List(ctx, folder, page, false)
	if err != nil {
		return views.MailPage{}, safeErr(ctx, err)
	}
	return g.mailPage(ctx, found)
}
func (g *Guard) mailPage(ctx context.Context, found model.MessagePage) (views.MailPage, error) {
	if !model.ValidPage(found.Next) {
		return views.MailPage{}, GuardError("ValueError")
	}
	categories, err := g.Categories(ctx)
	if err != nil {
		return views.MailPage{}, err
	}
	out, err := g.summaries(ctx, found.Messages, categories)
	if err != nil {
		return views.MailPage{}, err
	}
	return views.MailPage{Messages: out, NextPage: found.Next}, nil
}
func (g *Guard) read(ctx context.Context, id string) (model.Message, cache.Row, *content.Rendered, error) {
	rows, err := g.cached(ctx, []string{id})
	if err != nil {
		return model.Message{}, cache.Row{}, nil, safeErr(ctx, err)
	}
	row, ok := rows[id]
	fetched, err := g.fetch(ctx, []string{id}, !ok || !hidden(row.Verdict))
	if err != nil {
		return model.Message{}, cache.Row{}, nil, err
	}
	m := fetched[id]
	// A draft, or a message with BCC, is screened on every read: a draft changes, and a cached verdict may predate
	// BCC in the screened fields.
	fresh := m.IsDraft || len(m.BCC) > 0
	if ok && hidden(row.Verdict) && !fresh {
		return m, row, nil, nil
	}
	if fresh && m.Body == nil {
		draft := m.IsDraft
		fetched, err = g.fetch(ctx, []string{id}, true)
		if err != nil {
			return model.Message{}, cache.Row{}, nil, err
		}
		m = fetched[id]
		m.IsDraft = m.IsDraft || draft
	}
	var green *cache.Row
	if ok && row.Verdict == model.Green && !fresh {
		green = &row
	}
	row, r, err := g.screen(ctx, m, green)
	return m, row, r, err
}
func (g *Guard) Get(ctx context.Context, id string) (views.MailView, error) {
	m, row, r, err := g.read(ctx, id)
	if err != nil {
		return views.MailView{}, err
	}
	categories, err := g.Categories(ctx)
	if err != nil {
		return views.MailView{}, err
	}
	return views.View(m, screen(row), categories, r)
}
func (g *Guard) Categories(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := g.mailbox.Categories(ctx)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
