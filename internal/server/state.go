package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/fake"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
)

// CredentialCheck is one line of the check list. Item and Status never contain secrets or backend text.
type CredentialCheck struct{ Item, Status string }

// An account without a login does not block a load. Its commands fail with the provider's auth error.
const notLoggedIn = "not logged in; commands for this account fail until amgd account login"

// noLogin is the check of a fake account, which reads no credentials.
const noLogin = "no login needed"

// noScreeningKey is the key check under fake screening, which reads no key.
const noScreeningKey = "not needed (fake screening)"

// consoleNotSet is the console check without a console password. amgd serve then refuses to start and names the
// command for its home; the console refuses every request.
const consoleNotSet = "not set"

// noAccounts is the account check of a configuration without accounts.
const noAccounts = "none; run amgd account add"

// Providers return fixed codes that cannot tell a revoked login from a network failure, so one message covers both.
const signInFailed = "sign-in failed; check the network, or run amgd account login"

// fakeScreeningWarning is the warning when fake screening judges the mail of a real account.
const fakeScreeningWarning = "fake screening judges the mail of real accounts by keywords, not by a decision model."

func accountItem(a config.Account) string { return fmt.Sprintf("account %q (%s)", a.Alias, a.Provider) }

// LoadError is a refused load. Code is fixed: config_invalid, rules_invalid, credentials_failed, or canceled.
// Err is the detail for the daemon log: a config or rules error that names a key, or nil.
type LoadError struct {
	Code string
	Err  error
}

func (e LoadError) Error() string { return e.Code }

// Result is the outcome of a load: the check list, warnings, and the restart keys whose new value waits for a restart.
type Result struct {
	Checks   []CredentialCheck
	Warnings []string
	Restart  []string
}

type provider interface {
	Mailbox() model.Mailbox
	Calendar() model.Calendar
	Status(context.Context) (string, []string, error)
}
type account struct {
	config   config.Account
	provider provider
	guard    *guard.Guard
	rules    rules.Rules
	// logins is the credential snapshot of the state that built the account.
	logins *logins
}

// state is everything that the daemon read from config.toml, rules.toml, and the credential store. A load builds
// it whole and replaces the old one; it never changes afterwards. A request uses the state that was current when
// it began, until it ends.
type state struct {
	config      config.Config
	accounts    map[string]*account // by account key
	logins      *logins
	consoleHash string
}

// errNoAccounts and errUnknownAccount refuse an account call; both are public.
var (
	errNoAccounts     = errors.New("no accounts; run amgd account add")
	errUnknownAccount = errors.New("unknown account; run amg account list")
)

// account resolves the account of a request: an approved item by its key, an API call by its alias, and
// without an alias the default account.
func (st *state) account(alias *string, key string) (*account, error) {
	if key == "" {
		if len(st.config.Accounts) == 0 {
			return nil, errNoAccounts
		}
		name := st.config.DefaultAccount
		if alias != nil {
			name = *alias
		}
		a, ok := st.config.Account(name)
		if !ok {
			return nil, errUnknownAccount
		}
		key = a.Key()
	}
	if a := st.accounts[key]; a != nil {
		return a, nil
	}
	return nil, errUnknownAccount
}

// logins is the credential snapshot of one state. Providers read logins only from it. A token refresh saves through
// SwapOAuth: it never overwrites a login that amgd saved since the snapshot, and takes that login instead.
type logins struct {
	store        *credentials.RecordStore
	mu           sync.Mutex
	oauth        map[string]credentials.OAuth // by provider/account
	screeningKey string
}

func (l *logins) LoadOAuth(_ context.Context, p, a string) (credentials.OAuth, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.oauth[p+"/"+a]
	if !ok {
		return credentials.OAuth{}, credentials.ErrNotFound
	}
	v.Scopes = slices.Clone(v.Scopes)
	return v, nil
}

// SaveOAuth holds the lock across the store call, so two refreshes of one state cannot both expect the same entry.
func (l *logins) SaveOAuth(ctx context.Context, p, a string, v credentials.OAuth) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var expected *credentials.OAuth
	if old, ok := l.oauth[p+"/"+a]; ok {
		expected = &old
	}
	stored, err := l.store.SwapOAuth(ctx, p, a, expected, v)
	if errors.Is(err, credentials.ErrNotFound) {
		delete(l.oauth, p+"/"+a)
	}
	if err != nil {
		return err
	}
	stored.Scopes = slices.Clone(stored.Scopes)
	l.oauth[p+"/"+a] = stored
	return nil
}
func (l *logins) LoadScreeningKey(context.Context) (string, error) {
	if l.screeningKey == "" {
		return "", credentials.ErrNotFound
	}
	return l.screeningKey, nil
}

// SaveScreeningKey is refused: the daemon never writes the key after a load. amgd config set saves it.
func (l *logins) SaveScreeningKey(context.Context, string) error { return credentials.ErrUnavailable }

// Load reads config.toml, rules.toml, and the credential store, checks them, and installs the result as the state
// of the daemon. The start and every reload run it. On a refusal the old state stays. Loads run one at a time.
// The first load fixes the console address and the credential backend; a later load keeps them and names the
// key in Result.Restart. prompt asks for a missing screening key at the start; nil refuses.
func (s *Server) Load(ctx context.Context, prompt func(context.Context) (string, error)) (Result, error) {
	s.loading.Lock()
	defer s.loading.Unlock()
	var result Result
	c, err := config.Load(ctx, filepath.Join(s.home, "config.toml"))
	if err != nil {
		return result, s.refusal(ctx, "config_invalid", err)
	}
	s.mu.Lock()
	started, listen, backend := s.started, s.listen, s.backend
	s.mu.Unlock()
	if started && c.Console.Listen != listen {
		result.Restart = append(result.Restart, "console.listen")
		c.Console.Listen = listen
	}
	if started && c.Backend != backend {
		result.Restart = append(result.Restart, "credentials.backend")
		c.Backend = backend
	}
	loaded, _, err := rules.Load(ctx, filepath.Join(s.home, "rules.toml"), c.Emails())
	if err != nil {
		return result, s.refusal(ctx, "rules_invalid", err)
	}
	st, checks, err := s.build(ctx, c, loaded, started, prompt)
	result.Checks = checks
	if err != nil {
		return result, err
	}
	if c.Screening.Provider == "fake" && slices.ContainsFunc(c.Accounts, func(a config.Account) bool { return a.Provider != "fake" }) {
		result.Warnings = append(result.Warnings, fakeScreeningWarning)
	}
	s.checkSignIns(ctx, st, result.Checks)
	if ctx.Err() != nil {
		return result, LoadError{Code: "canceled"}
	}
	s.mu.Lock()
	if !s.started {
		s.started, s.listen, s.backend = true, c.Console.Listen, c.Backend
	}
	s.mu.Unlock()
	s.current.Store(st)
	return result, nil
}

// ErrReloadAudit is the error of a reload that installed the new state but could not write its audit record.
// The reload is not refused: the new state stays.
var ErrReloadAudit = errors.New("reload audit record failed")

// Reload is Load without a screening key prompt. It writes a reload audit record with ok or the refusal code; a
// failed record after a successful load returns ErrReloadAudit.
func (s *Server) Reload(ctx context.Context) (Result, error) {
	result, err := s.Load(ctx, nil)
	code := "ok"
	if e, ok := errors.AsType[LoadError](err); ok {
		code = e.Code
	} else if err != nil {
		code = "canceled"
	}
	if e := s.audit.Reload(context.WithoutCancel(ctx), code); e != nil && err == nil {
		err = ErrReloadAudit
	}
	return result, err
}

func (s *Server) refusal(ctx context.Context, code string, err error) error {
	if ctx.Err() != nil {
		return LoadError{Code: "canceled"}
	}
	return LoadError{Code: code, Err: err}
}

// build reads the credential store once and builds the accounts. The first load rewrites the whole record so that
// backend prompts (macOS Keychain access dialogs) happen at the start, while an operator is present.
func (s *Server) build(ctx context.Context, c config.Config, loaded map[string]rules.Rules, started bool, prompt func(context.Context) (string, error)) (*state, []CredentialCheck, error) {
	read := s.options.Store.Preload
	if started {
		read = s.options.Store.Read
	}
	record, err := read(ctx)
	status := "ok"
	switch {
	case err == nil:
	case errors.Is(err, credentials.ErrDecrypt):
		status = "wrong password or damaged file"
	case errors.Is(err, credentials.ErrPasswordMissing):
		status = "password missing"
	case errors.Is(err, credentials.ErrUnsupportedVersion):
		status = "unsupported version"
	default:
		status = "access denied"
	}
	checks := []CredentialCheck{{"credentials (" + c.Backend + ")", status}}
	if err != nil {
		return nil, checks, s.refusal(ctx, "credentials_failed", nil)
	}
	failed := false
	add := func(item, status string) {
		checks = append(checks, CredentialCheck{item, status})
		failed = failed || (status != "ok" && status != "saved" && status != noScreeningKey && status != notLoggedIn && status != noLogin && status != consoleNotSet && status != noAccounts)
	}
	l := &logins{store: s.options.Store, oauth: map[string]credentials.OAuth{}}
	if len(c.Accounts) == 0 {
		add("accounts", noAccounts)
	}
	for _, a := range c.Accounts {
		if a.Provider == "fake" {
			add(accountItem(a), noLogin)
			continue
		}
		v, ok := record.OAuth[a.Provider+"/"+a.Key()]
		if !ok {
			add(accountItem(a), notLoggedIn)
			continue
		}
		l.oauth[a.Provider+"/"+a.Key()] = v
		add(accountItem(a), "ok")
	}
	switch key := record.ScreeningKey; {
	case c.Screening.Provider == "fake":
		add("screening key", noScreeningKey)
	case key != "":
		l.screeningKey = key
		add("screening key", "ok")
	case prompt == nil:
		add("screening key", "missing; run amgd config set screening.api_key")
	default:
		entered, err := prompt(ctx)
		entered = strings.TrimSpace(entered)
		switch {
		case ctx.Err() != nil:
			return nil, checks, LoadError{Code: "canceled"}
		case err != nil || entered == "":
			add("screening key", "not entered; run amgd config set screening.api_key")
		case s.options.Store.SaveScreeningKey(ctx, entered) != nil:
			add("screening key", "write failed or denied")
		default:
			l.screeningKey = entered
			add("screening key", "saved")
			_ = s.audit.Credential(context.WithoutCancel(ctx), "screening_api_key_set")
		}
	}
	if record.ConsoleHash != "" {
		add("console password", "ok")
	} else {
		add("console password", consoleNotSet)
	}
	if failed {
		return nil, checks, s.refusal(ctx, "credentials_failed", nil)
	}
	screener := s.options.Screener
	if screener == nil && c.Screening.Provider == "fake" {
		screener = screening.NewFake()
	}
	if screener == nil {
		screener = screening.New(screening.Options{Credentials: l, HTTPClient: safeHTTP(s.options.ScreeningHTTPClient), Endpoint: c.Screening.Endpoint, Model: c.Screening.Model})
	}
	screener = safeScreener{screener}
	st := &state{config: c, accounts: map[string]*account{}, logins: l, consoleHash: record.ConsoleHash}
	for _, a := range c.Accounts {
		var p provider
		switch a.Provider {
		case "fake":
			p = fake.New(a.Key(), s.home, s.options.Now)
		case "gmail":
			o := s.options.Gmail
			if o.Now == nil {
				o.Now = s.options.Now
			}
			o.HTTPClient = safeHTTP(o.HTTPClient)
			p = gmail.New(a, l, o)
		default:
			o := s.options.M365
			if o.Now == nil {
				o.Now = s.options.Now
			}
			o.HTTPClient = safeHTTP(o.HTTPClient)
			p = m365.New(a, l, o)
		}
		g := guard.New(a.Key(), p.Mailbox(), p.Calendar(), screener, s.cache, s.audit, guard.Options{Now: s.options.Now, Policy: a.Policy, Gates: s.gates(a.Key())})
		st.accounts[a.Key()] = &account{a, p, g, loaded[a.Key()], l}
	}
	return st, checks, nil
}

// gates returns the guard gates of an account, the same for every state.
func (s *Server) gates(key string) *guard.Gates {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.accountGates[key]
	if g == nil {
		g = guard.NewGates()
		s.accountGates[key] = g
	}
	return g
}

// checkSignIns asks the provider of each account that has a login. A saved token can be expired or revoked, so each
// ok line is replaced with the provider's answer. A refusal does not block the load.
func (s *Server) checkSignIns(ctx context.Context, st *state, checks []CredentialCheck) {
	for _, a := range st.config.Accounts {
		i := slices.Index(checks, CredentialCheck{accountItem(a), "ok"})
		if i < 0 {
			continue
		}
		check, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _, err := st.accounts[a.Key()].provider.Status(check)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			checks[i].Status = signInFailed
		}
	}
}
