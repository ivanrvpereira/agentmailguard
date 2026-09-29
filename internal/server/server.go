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
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard-go/internal/audit"
	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard-go/internal/screening"
)

const MaxRequestBytes = 1 << 20
const ShutdownTimeout = 10 * time.Second

type Options struct {
	Credentials credentials.Store
	Screener    model.Screener
	M365        m365.Options
	Gmail       gmail.Options
	Now         func() time.Time
	LookupEnv   func(string) string // defaults to os.Getenv; used for OPENROUTER_API_KEY
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
	accounts  map[string]*account
	creds     *memoryStore
	preloaded bool
}

func (Server) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "Server{redacted}") }

func New(ctx context.Context, home string, opts Options) (*Server, error) {
	home, err := config.Home(home)
	if err != nil {
		return nil, errors.New("configuration unavailable")
	}
	c, err := cache.Open(ctx, filepath.Join(home, "cache.db"))
	if err != nil {
		return nil, errors.New("cache unavailable")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.Getenv
	}
	if opts.Credentials == nil {
		opts.Credentials, _ = credentials.NewMacOS()
	}
	creds := &memoryStore{backend: safeStore{opts.Credentials}}
	opts.Credentials = creds
	if opts.Screener == nil {
		opts.Screener = screening.New(screening.Options{Credentials: opts.Credentials, HTTPClient: safeHTTP(nil), LookupEnv: opts.LookupEnv})
	}
	opts.Screener = safeScreener{opts.Screener}
	opts.M365.HTTPClient = safeHTTP(opts.M365.HTTPClient)
	opts.Gmail.HTTPClient = safeHTTP(opts.Gmail.HTTPClient)
	ctx, cancel := context.WithCancel(ctx)
	return &Server{home: home, options: opts, cache: c, audit: audit.New(filepath.Join(home, "audit.jsonl")), ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64), accounts: make(map[string]*account), creds: creds, mu: new(sync.Mutex), work: new(sync.WaitGroup), closeOnce: new(sync.Once)}, nil
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
}

var invalid = errors.New("invalid request")

func fail(w http.ResponseWriter, status int, message string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
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
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		fail(w, 503, "server unavailable")
		return
	}
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, 503, "server busy")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
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
		if errors.Is(err, invalid) {
			status = 400
			message = "invalid request"
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
		return invalid
	}
	t, err := d.Token()
	if err != nil {
		return invalid
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return invalid
				}
				seen[key] = true
				if jsonValue(d, depth+1) != nil {
					return invalid
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return invalid
			}
		case '[':
			for d.More() {
				if jsonValue(d, depth+1) != nil {
					return invalid
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}
func strict(b []byte, out any, keys ...string) error {
	if !utf8.Valid(b) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if jsonValue(d, 0) != nil {
		return invalid
	}
	if _, e := d.Token(); e != io.EOF {
		return invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return invalid
	}
	for k, v := range fields {
		allowed := false
		for _, a := range keys {
			allowed = allowed || k == a
		}
		if !allowed || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return invalid
		}
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return invalid
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

func (s *Server) call(ctx context.Context, r request) (any, error) {
	switch r.Operation {
	case "stats":
		p := struct {
			Days int `json:"days"`
		}{30}
		if strict(r.Params, &p, "days") != nil || p.Days < 1 || p.Days > 365000 {
			return nil, invalid
		}
		stats, e := s.cache.Stats(ctx, p.Days, s.options.Now())
		out := map[string]statsDTO{}
		for a, v := range stats {
			out[a] = statsDTO{v.Total, v.Counts, v.Shares, v.GreenShareNonSealed, v.DMARCFail, v.CompAuthFail}
		}
		return out, e
	case "cache.clear":
		if strict(r.Params, &struct{}{}) != nil {
			return nil, invalid
		}
		n, e := s.cache.Clear(ctx)
		return struct {
			Deleted int64 `json:"deleted"`
		}{n}, e
	default:
		return s.accountCall(ctx, r)
	}
}
