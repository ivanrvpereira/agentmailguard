// Package server owns all mailbox state and exposes only closed, guarded operations.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

const MaxRequestBytes = 1 << 20
const ShutdownTimeout = 10 * time.Second

// Options are the external seams of the daemon. Store is required: the daemon reads logins, the screening key,
// and the console password hash only from it.
type Options struct {
	Store    *credentials.RecordStore
	Screener model.Screener // nil: the engine that [screening] names
	M365     m365.Options
	Gmail    gmail.Options
	Now      func() time.Time
	// ScreeningHTTPClient is the HTTP client of the jev engine. Nil uses the default client. For tests.
	ScreeningHTTPClient *http.Client
}

func (Options) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "ServerOptions{redacted}") }

type Server struct {
	home      string
	options   Options
	cache     *cache.Cache
	audit     *audit.Log
	ctx       context.Context
	cancel    context.CancelFunc
	mu        *sync.Mutex
	closed    bool
	work      *sync.WaitGroup
	slots     chan struct{}
	closeOnce *sync.Once
	closeErr  error
	// current is the state that new requests use; nil until the first Load succeeds.
	current atomic.Pointer[state]
	// loading serializes loads. It is never held while mu is wanted by a load's caller.
	loading *sync.Mutex
	// started, listen, and backend are fixed by the first Load, under mu: the console address and the credential
	// backend change only at a restart.
	started         bool
	listen, backend string
	// accountGates are the guard gates of each account key, kept across loads.
	accountGates map[string]*guard.Gates
	// signins limits wrong console passwords across loads.
	signins *signinLimit
}

func (*Server) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "Server{redacted}") }

// New opens cache.db and the audit log. It reads no configuration: Load does.
func New(ctx context.Context, home string, opts Options) (*Server, error) {
	home, err := config.Home(home)
	if err != nil || opts.Store == nil {
		return nil, errors.New("configuration unavailable")
	}
	c, err := cache.Open(ctx, filepath.Join(home, "cache.db"))
	if err != nil {
		return nil, errors.New("cache unavailable")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Server{home: home, options: opts, cache: c, audit: audit.New(filepath.Join(home, "audit.jsonl")), ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64), mu: new(sync.Mutex), work: new(sync.WaitGroup), closeOnce: new(sync.Once), loading: new(sync.Mutex), accountGates: map[string]*guard.Gates{}, signins: &signinLimit{}}, nil
}

// Close stops admission, cancels active calls, and waits at most ShutdownTimeout.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		done := make(chan struct{})
		go func() { s.work.Wait(); close(done) }()
		select {
		case <-done:
			if s.cache.Close() != nil {
				s.closeErr = errors.New("cache close failed")
			}
		case <-time.After(ShutdownTimeout):
			s.closeErr = errors.New("shutdown timed out")
			go func() { <-done; _ = s.cache.Close() }()
		}
	})
	return s.closeErr
}

type request struct {
	Operation string          `json:"operation"`
	Account   *string         `json:"account,omitempty"`
	Params    json.RawMessage `json:"params"`
	// key selects the account of an approved action by its key instead of an alias. The API cannot set it.
	key string
}

var errInvalid = errors.New("invalid request")

func fail(w http.ResponseWriter, status int, message string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}

// admit refuses work after Close and registers the work so that Close waits for it. The returned context
// ends when ctx or the server ends; done must be called when the work ends.
func (s *Server) admit(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	s.work.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel(); s.work.Done() }, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			fail(w, 500, "operation failed")
		}
	}()
	if r.URL.Path != "/v1/call" || r.URL.RawQuery != "" {
		fail(w, 404, "not found")
		return
	}
	if _, ok := r.Header["Origin"]; ok {
		fail(w, 403, "browser requests refused")
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "method refused")
		return
	}
	typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" || len(params) > 1 || (len(params) == 1 && params["charset"] != "utf-8") {
		fail(w, 415, "content type refused")
		return
	}
	ctx, done, err := s.admit(r.Context())
	if err != nil {
		fail(w, 503, err.Error())
		return
	}
	defer done()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, 503, "daemon busy")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Second))
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err == nil {
		_ = rc.SetReadDeadline(time.Time{})
	}
	var req request
	if err != nil || len(b) > MaxRequestBytes || strict(b, &req, "operation", "account", "params") != nil || req.Operation == "" || len(req.Params) == 0 || (req.Account != nil && !validString(*req.Account, 256, false)) {
		fail(w, 400, "invalid request")
		return
	}
	result, err := s.call(ctx, req)
	if err != nil {
		status := 500
		message := "operation failed"
		if errors.Is(err, errInvalid) {
			status = 400
			message = "invalid request"
		} else if errors.Is(err, ErrApprovalNotFound) {
			status = 404
			message = err.Error()
		} else if text, ok := publicError(err); ok {
			message = text
		}
		fail(w, status, message)
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		fail(w, 500, "operation failed")
		return
	}
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(data, '\n'))
}

// Token validation rejects duplicate keys at every depth, including escaped spellings.
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errInvalid
	}
	t, err := d.Token()
	if err != nil {
		return errInvalid
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return errInvalid
				}
				seen[key] = true
				if jsonValue(d, depth+1) != nil {
					return errInvalid
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return errInvalid
			}
		case '[':
			for d.More() {
				if jsonValue(d, depth+1) != nil {
					return errInvalid
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return errInvalid
			}
		default:
			return errInvalid
		}
	}
	return nil
}
func strict(b []byte, out any, keys ...string) error {
	if !utf8.Valid(b) {
		return errInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if jsonValue(d, 0) != nil {
		return errInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return errInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return errInvalid
	}
	for k, v := range fields {
		allowed := false
		for _, a := range keys {
			allowed = allowed || k == a
		}
		if !allowed || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errInvalid
		}
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errInvalid
	}
	return nil
}
func validString(s string, max int, empty bool) bool {
	return (empty || s != "") && len(s) <= max && utf8.ValidString(s)
}

type statsDTO struct {
	Total               int                       `json:"total"`
	Counts              map[model.Verdict]int     `json:"counts"`
	Shares              map[model.Verdict]float64 `json:"shares"`
	GreenShareNonSealed *float64                  `json:"green_share_non_sealed"`
	DMARCFail           int                       `json:"dmarc_fail"`
	CompAuthFail        int                       `json:"compauth_fail"`
}

// call runs one API operation on the current state. A name that is not in the operation table is refused.
func (s *Server) call(ctx context.Context, r request) (any, error) {
	st := s.current.Load()
	if st == nil {
		return nil, ErrUnavailable
	}
	op, ok := operations[r.Operation]
	if !ok {
		return nil, errInvalid
	}
	if op.daemon != nil {
		return op.daemon(ctx, s, st, r.Params)
	}
	return s.accountCall(ctx, st, op, r)
}

func stats(ctx context.Context, s *Server, _ *state, raw json.RawMessage) (any, error) {
	p := struct {
		Days int `json:"days"`
	}{30}
	if strict(raw, &p, "days") != nil || p.Days < 1 || p.Days > 365000 {
		return nil, errInvalid
	}
	v, e := s.cache.Stats(ctx, p.Days, s.options.Now())
	out := map[string]statsDTO{}
	for a, v := range v {
		out[a] = statsDTO{v.Total, v.Counts, v.Shares, v.GreenShareNonSealed, v.DMARCFail, v.CompAuthFail}
	}
	return out, e
}

type accountDTO struct {
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Email    string `json:"email"`
	Default  bool   `json:"default"`
}

// accountList lists the configured accounts. It carries no secret and no sign-in state.
func accountList(_ context.Context, _ *Server, st *state, raw json.RawMessage) (any, error) {
	if strict(raw, &struct{}{}) != nil {
		return nil, errInvalid
	}
	out := []accountDTO{}
	for _, a := range st.config.Accounts {
		out = append(out, accountDTO{a.Alias, a.Provider, a.Email, a.Alias == st.config.DefaultAccount})
	}
	return out, nil
}
