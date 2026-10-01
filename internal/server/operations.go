package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

type provider interface {
	Mailbox() model.Mailbox
	Calendar() model.Calendar
	Status(context.Context) (string, []string, error)
}
type account struct {
	config   config.Account
	provider provider
	guard    *guard.Guard
}

var restart = errors.New("account configuration changed; restart required")
var configError = errors.New("configuration unavailable")
var rulesError = errors.New("rules unavailable")

func publicError(e error) (string, bool) {
	for _, fixed := range []error{restart, configError, rulesError} {
		if e == fixed {
			return fixed.Error(), true
		}
	}
	if e, ok := e.(guard.GuardError); ok {
		switch string(e) {
		case "move refused: the destination must be a folder ID from `folders`", "flag state must be one of flagged, complete, none", "categorize needs at least one category to add or remove", "categorize refused: not in the category list",
			"link refused: message is red, not green", "link refused: message is yellow, not green", "link refused: message is sealed, not green", "link refused: message is error, not green",
			"search needs at least one of text, from, subject, after, before":
			return string(e), true
		}
	}
	return "", false
}
func (s *Server) load(ctx context.Context, name *string) (config.Account, error) {
	a, e := config.LoadAccount(ctx, filepath.Join(s.home, "config.toml"), name)
	if e != nil {
		return config.Account{}, configError
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.accounts[a.Name]; (old != nil && old.config != a) || (old == nil && s.preloaded) {
		return config.Account{}, restart
	}
	return a, nil
}
func (s *Server) instance(a config.Account) (*account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.accounts[a.Name]; old != nil {
		if old.config != a {
			return nil, restart
		}
		return old, nil
	}
	if s.preloaded {
		return nil, restart
	}
	var p provider
	if a.Provider == "gmail" {
		o := s.options.Gmail
		if o.Now == nil {
			o.Now = s.options.Now
		}
		p = gmail.New(a, s.options.Credentials, o)
	} else {
		o := s.options.M365
		if o.Now == nil {
			o.Now = s.options.Now
		}
		p = m365.New(a, s.options.Credentials, o)
	}
	v := &account{a, p, guard.New(a.Name, p.Mailbox(), p.Calendar(), s.options.Screener, s.cache, s.audit, guard.Options{Now: s.options.Now})}
	s.accounts[a.Name] = v
	return v, nil
}
func params(b json.RawMessage, p any) error {
	typ := reflect.TypeOf(p).Elem()
	keys := make([]string, typ.NumField())
	for i := range keys {
		keys[i] = typ.Field(i).Tag.Get("json")
	}
	return strict(b, p, keys...)
}
func listValid(v []string, min, max int) bool {
	if len(v) < min || len(v) > max {
		return false
	}
	for _, s := range v {
		if !validString(s, 4096, false) {
			return false
		}
	}
	return true
}
func idValid(id string) bool { return validString(id, 4096, false) }

type idParam struct {
	ID string `json:"id"`
}
type idsParam struct {
	IDs []string `json:"ids"`
}
type listParam struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
	Skip   int    `json:"skip"`
}
type searchParam struct {
	Text    *string `json:"text"`
	Sender  *string `json:"from"`
	Subject *string `json:"subject"`
	After   *string `json:"after"`
	Before  *string `json:"before"`
	Folder  *string `json:"folder"`
	Count   int     `json:"count"`
}
type linkParam struct {
	ID    string   `json:"id"`
	Links []string `json:"links"`
	All   bool     `json:"all"`
}
type moveParam struct {
	IDs    []string `json:"ids"`
	Folder string   `json:"folder"`
}
type flagParam struct {
	IDs   []string        `json:"ids"`
	State model.FlagState `json:"state"`
}
type categoryParam struct {
	IDs    []string `json:"ids"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}
type organizeParam struct {
	Since string  `json:"since"`
	Until *string `json:"until"`
	Apply bool    `json:"apply"`
}
type calendarParam struct {
	Start string `json:"start"`
	End   string `json:"end"`
}
type newParam struct {
	To      []string `json:"to"`
	Subject *string  `json:"subject"`
	Body    *string  `json:"body"`
}
type bodyParam struct {
	ID   string  `json:"id"`
	Body *string `json:"body"`
}
type forwardParam struct {
	ID string   `json:"id"`
	To []string `json:"to"`
}
type mutationDTO struct {
	ID    string  `json:"id"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}

func mutations(v []model.MutationResult, e error) (any, error) {
	out := make([]mutationDTO, 0, len(v))
	for _, r := range v {
		out = append(out, mutationDTO{r.ID, r.OK, r.Error})
	}
	return out, e
}

type folderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type draftDTO struct {
	ID string `json:"id"`
}
type updateDTO struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
}
type statusDTO struct {
	Account       string    `json:"account"`
	OK            bool      `json:"ok"`
	LoggedIn      bool      `json:"logged_in"`
	Username      *string   `json:"username,omitempty"`
	Scopes        *[]string `json:"scopes,omitempty"`
	ExtraScopes   *[]string `json:"extra_scopes,omitempty"`
	MissingScopes *[]string `json:"missing_scopes,omitempty"`
}
type ruleDTO struct {
	Rule     string  `json:"rule"`
	Folder   *string `json:"folder,omitempty"`
	Category *string `json:"category,omitempty"`
	Name     *string `json:"name"`
	MinAge   *int64  `json:"min_age_hours,omitempty"`
}
type rulesDTO struct {
	Account  string          `json:"account"`
	Rules    []ruleDTO       `json:"rules"`
	Warnings []guard.Warning `json:"warnings"`
}

// Dispatch first validates a complete typed operation. Account and provider access occur only afterwards.
func (s *Server) accountCall(ctx context.Context, r request) (any, error) {
	var call func(*account) (any, error)
	switch r.Operation {
	case "mail.get", "calendar.get":
		var p idParam
		if params(r.Params, &p) != nil || !idValid(p.ID) {
			return nil, invalid
		}
		call = func(a *account) (any, error) {
			if r.Operation == "mail.get" {
				return a.guard.Get(ctx, p.ID)
			}
			return a.guard.CalendarGet(ctx, p.ID)
		}
	case "mail.list":
		p := listParam{Folder: "inbox", Count: 25}
		if params(r.Params, &p) != nil || !idValid(p.Folder) || p.Count < 1 || p.Count > 100 || p.Skip < 0 || p.Skip > 1000000 {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return a.guard.List(ctx, p.Folder, p.Count, p.Skip) }
	case "mail.search":
		p := searchParam{Count: 25}
		if params(r.Params, &p) != nil || p.Count < 1 || p.Count > 100 {
			return nil, invalid
		}
		for _, v := range []*string{p.Text, p.Sender, p.Subject, p.Folder} {
			if v != nil && !validString(*v, 4096, true) {
				return nil, invalid
			}
		}
		q := model.SearchQuery{Text: p.Text, Sender: p.Sender, Subject: p.Subject, Folder: p.Folder}
		for _, d := range []struct {
			v   *string
			out **time.Time
		}{{p.After, &q.After}, {p.Before, &q.Before}} {
			if d.v != nil {
				t, e := time.Parse("2006-01-02", *d.v)
				if e != nil {
					return nil, invalid
				}
				*d.out = &t
			}
		}
		call = func(a *account) (any, error) { return a.guard.Search(ctx, q, p.Count) }
	case "mail.link":
		var p linkParam
		if params(r.Params, &p) != nil || !idValid(p.ID) || p.All == (len(p.Links) > 0) || !listValid(p.Links, 0, 1000) {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return a.guard.Links(ctx, p.ID, p.Links) }
	case "mail.folders", "mail.categories", "auth.status", "rules.list":
		if params(r.Params, &struct{}{}) != nil {
			return nil, invalid
		}
		call = func(a *account) (any, error) {
			switch r.Operation {
			case "mail.folders":
				v, e := a.guard.Folders(ctx)
				out := make([]folderDTO, 0, len(v))
				for _, f := range v {
					out = append(out, folderDTO{f.ID, f.Name})
				}
				return out, e
			case "mail.categories":
				return a.guard.Categories(ctx)
			case "auth.status":
				return s.status(ctx, a)
			}
			return nil, invalid
		}
	case "mail.archive", "mail.trash", "mail.read", "mail.unread":
		var p idsParam
		if params(r.Params, &p) != nil || !listValid(p.IDs, 1, 50) {
			return nil, invalid
		}
		call = func(a *account) (any, error) {
			switch r.Operation {
			case "mail.archive":
				return mutations(a.guard.Archive(ctx, p.IDs))
			case "mail.trash":
				return mutations(a.guard.Trash(ctx, p.IDs))
			default:
				return mutations(a.guard.MarkRead(ctx, p.IDs, r.Operation == "mail.read"))
			}
		}
	case "mail.move":
		var p moveParam
		if params(r.Params, &p) != nil || !listValid(p.IDs, 1, 50) || !idValid(p.Folder) {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return mutations(a.guard.Move(ctx, p.IDs, p.Folder)) }
	case "mail.flag":
		var p flagParam
		if params(r.Params, &p) != nil || !listValid(p.IDs, 1, 50) || (p.State != model.Flagged && p.State != model.NoFlag && p.State != model.Complete) {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return mutations(a.guard.Flag(ctx, p.IDs, p.State)) }
	case "mail.categorize":
		var p categoryParam
		if params(r.Params, &p) != nil || !listValid(p.IDs, 1, 50) || !listValid(p.Add, 0, 100) || !listValid(p.Remove, 0, 100) {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return mutations(a.guard.Categorize(ctx, p.IDs, p.Add, p.Remove)) }
	case "mail.organize":
		p := organizeParam{Since: "7d"}
		if params(r.Params, &p) != nil {
			return nil, invalid
		}
		now := s.options.Now().UTC()
		since, e := parseWhen(p.Since, now)
		if e != nil {
			return nil, invalid
		}
		var until *time.Time
		if p.Until != nil {
			u, e := parseWhen(*p.Until, now)
			if e != nil || !u.After(since) {
				return nil, invalid
			}
			until = &u
		}
		call = func(a *account) (any, error) {
			loaded, e := s.loadRules(ctx, a.config.Name)
			if e != nil {
				return nil, e
			}
			return a.guard.Organize(ctx, since, loaded, now, p.Apply, until)
		}
	case "calendar.list":
		var p calendarParam
		if params(r.Params, &p) != nil {
			return nil, invalid
		}
		first, e := time.Parse(time.RFC3339, p.Start)
		last, e2 := time.Parse(time.RFC3339, p.End)
		if e != nil || e2 != nil || !last.After(first) || last.Sub(first) > 90*24*time.Hour+time.Hour {
			return nil, invalid
		}
		call = func(a *account) (any, error) { return a.guard.CalendarList(ctx, first, last) }
	case "draft.new":
		var p newParam
		if params(r.Params, &p) != nil || !listValid(p.To, 1, 100) || p.Subject == nil || p.Body == nil || !validString(*p.Subject, 65536, true) || !validString(*p.Body, MaxRequestBytes, true) {
			return nil, invalid
		}
		call = func(a *account) (any, error) {
			id, e := a.guard.DraftNew(ctx, model.NewDraft{To: p.To, Subject: *p.Subject, Body: *p.Body})
			return draftDTO{id}, e
		}
	case "draft.reply", "draft.update":
		var p bodyParam
		if params(r.Params, &p) != nil || !idValid(p.ID) || p.Body == nil || !validString(*p.Body, MaxRequestBytes, true) {
			return nil, invalid
		}
		call = func(a *account) (any, error) {
			if r.Operation == "draft.reply" {
				id, e := a.guard.DraftReply(ctx, p.ID, *p.Body)
				return draftDTO{id}, e
			}
			id, e := a.guard.DraftUpdate(ctx, p.ID, *p.Body)
			return updateDTO{id, true}, e
		}
	case "draft.forward":
		var p forwardParam
		if params(r.Params, &p) != nil || !idValid(p.ID) || !listValid(p.To, 1, 100) {
			return nil, invalid
		}
		call = func(a *account) (any, error) { id, e := a.guard.DraftForward(ctx, p.ID, p.To); return draftDTO{id}, e }
	default:
		return nil, invalid
	}
	a, e := s.load(ctx, r.Account)
	if e != nil {
		return nil, e
	}
	if r.Operation == "rules.list" {
		return s.ruleList(ctx, a)
	}
	instance, e := s.instance(a)
	if e != nil {
		return nil, e
	}
	return call(instance)
}
func (s *Server) status(ctx context.Context, a *account) (any, error) {
	out := statusDTO{Account: a.config.Name}
	saved, e := s.options.Credentials.LoadOAuth(ctx, a.config.Provider, a.config.Name)
	if errors.Is(e, credentials.ErrNotFound) {
		return out, nil
	}
	if e != nil {
		return nil, errors.New("authentication unavailable")
	}
	out.LoggedIn = true
	if saved.Username != "" {
		out.Username = &saved.Username
	}
	user, scopes, e := a.provider.Status(ctx)
	if e != nil {
		var diagnostic interface{ MissingScopes() []string }
		if errors.As(e, &diagnostic) {
			missing := safeScopeDiagnostics(diagnostic.MissingScopes())
			out.MissingScopes = &missing
		}
		return out, nil
	}
	out.OK = true
	out.Username = &user
	extra := m365.ExtraScopes(scopes)
	if a.config.Provider == "gmail" {
		extra = gmail.ExtraScopes(scopes)
	}
	// scopes lists requested grants; extra grants appear only as bounded diagnostic labels.
	scopes = slices.DeleteFunc(scopes, func(s string) bool { return slices.Contains(extra, s) })
	out.Scopes = &scopes
	if extra = safeScopeDiagnostics(extra); len(extra) != 0 {
		out.ExtraScopes = &extra
	}
	return out, nil
}

// Scope labels come from credentials or token responses, not trusted error text.
var scopeLabel = regexp.MustCompile(`^(?:(?:role:)?[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)+|https://www\.googleapis\.com/auth/[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*)$`)

func safeScopeDiagnostics(scopes []string) []string {
	out := []string{}
	for _, scope := range scopes {
		if len(scope) <= 256 && scopeLabel.MatchString(scope) {
			out = append(out, scope)
			if len(out) == 100 {
				break
			}
		}
	}
	return out
}

func (s *Server) loadRules(ctx context.Context, name string) (rules.Rules, error) {
	names, e := config.AccountNames(ctx, filepath.Join(s.home, "config.toml"))
	if e != nil {
		return rules.Rules{}, configError
	}
	r, e := rules.Load(ctx, filepath.Join(s.home, "rules.toml"), name, names)
	if e != nil {
		return rules.Rules{}, rulesError
	}
	return r, nil
}
func (s *Server) ruleList(ctx context.Context, a config.Account) (any, error) {
	r, e := s.loadRules(ctx, a.Name)
	if e != nil {
		return nil, e
	}
	out := rulesDTO{Account: a.Name, Rules: []ruleDTO{}, Warnings: []guard.Warning{}}
	if len(r.Rules) == 0 {
		return out, nil
	}
	instance, e := s.instance(a)
	if e != nil {
		return nil, e
	}
	g := instance.guard
	folders, e := g.Folders(ctx)
	if e != nil {
		return nil, e
	}
	cats, e := g.Categories(ctx)
	if e != nil {
		return nil, e
	}
	names := map[string]string{}
	for _, f := range folders {
		names[f.ID] = content.CleanSubject(f.Name)
	}
	for _, v := range r.Rules {
		line := ruleDTO{Rule: v.Name}
		if v.Kind == rules.Folder {
			line.Folder = &v.Target
			line.MinAge = &v.MinAgeHours
			if v.Target == "archive" || v.Target == "trash" {
				line.Name = &v.Target
			} else if name, ok := names[v.Target]; ok {
				line.Name = &name
			}
		} else {
			line.Category = &v.Target
			if slices.Contains(cats, v.Target) {
				line.Name = &v.Target
			}
		}
		out.Rules = append(out.Rules, line)
		if line.Name == nil {
			out.Warnings = append(out.Warnings, guard.Warning{Rule: v.Name, Problem: string(v.Kind) + " not found"})
		}
	}
	return out, nil
}

var relativePattern = regexp.MustCompile(`^([0-9]+)([dwm])$`)

func parseWhen(value string, now time.Time) (time.Time, error) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if match := relativePattern.FindStringSubmatch(value); match != nil {
		n, e := strconv.Atoi(match[1])
		if e != nil || n < 1 || n > 3650000 {
			return time.Time{}, invalid
		}
		var t time.Time
		switch match[2] {
		case "d":
			t = today.AddDate(0, 0, -n)
		case "w":
			if n > 521428 {
				return t, invalid
			}
			t = today.AddDate(0, 0, -7*n)
		case "m":
			if n > 119987 {
				return t, invalid
			}
			first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -n, 0)
			last := first.AddDate(0, 1, -1).Day()
			t = time.Date(first.Year(), first.Month(), min(today.Day(), last), 0, 0, 0, 0, time.UTC)
		}
		if t.Year() < 1 || t.Year() > 9999 {
			return time.Time{}, invalid
		}
		return t, nil
	}
	t, e := time.Parse("2006-01-02", value)
	if e != nil || t.Year() < 1 || t.After(today) {
		return time.Time{}, invalid
	}
	return t, nil
}
