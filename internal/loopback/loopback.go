// Package loopback is the browser half of a desktop OAuth sign-in on the daemon host: a callback on IPv4
// loopback, and the same checks for a redirected address that the user pastes on a host the callback cannot reach.
// It never redirects, logs, or prints an authorization code.
package loopback

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrDenied is a redirect of this sign-in that carries a provider error or not exactly one code.
var ErrDenied = errors.New("authorization denied")

// Random returns 32 random bytes in unpadded base64url, for a state or a PKCE verifier.
func Random() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Open opens uri in the default browser: open on macOS, xdg-open on Linux. It can return only when the browser
// exits, so callers run it in the background.
func Open(ctx context.Context, uri string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.CommandContext(ctx, opener, uri).Run()
}

// Callback is the loopback redirect of one sign-in. Host is the authority of its redirect URI: the host name
// that the app registration expects and the port of the listener, which binds only 127.0.0.1.
type Callback struct {
	Host     string
	state    string
	listener net.Listener
	server   *http.Server
	result   chan result
}

type result struct {
	code string
	err  error
}

// Listen starts the callback of the sign-in with state on a random port. The caller must Close it.
func Listen(name, state string) (*Callback, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	c := &Callback{Host: net.JoinHostPort(name, port), state: state, listener: listener, result: make(chan result, 1)}
	c.server = &http.Server{Handler: http.HandlerFunc(c.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = c.server.Serve(listener) }()
	return c, nil
}

// Close stops the callback; a redirect that arrives later gets no answer.
func (c *Callback) Close() {
	_ = c.server.Close()
	_ = c.listener.Close()
}

func (c *Callback) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodGet || !c.accept(r.Host, r.URL.Path, r.URL.RawQuery) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "Invalid sign-in callback.")
		return
	}
	_, _ = io.WriteString(w, "Sign-in response received. Return to the terminal for the login result.")
}

// accept reports whether a redirect belongs to this sign-in and passes the first one on. A matching redirect that
// carries a provider error still ends the sign-in.
func (c *Callback) accept(host, path, rawQuery string) bool {
	q, err := url.ParseQuery(rawQuery)
	if err != nil || path != "/" || host != c.Host || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 {
		return false
	}
	got := result{code: q.Get("code")}
	if len(q["code"]) != 1 || got.code == "" || q.Has("error") {
		got.err = ErrDenied
	}
	select {
	case c.result <- got:
	default:
	}
	return true
}

// Wait opens uri with open and returns the code of the first redirect of this sign-in: from the callback, or an
// address that read returns, with the same checks. The caller has already shown uri, so neither a failed open nor
// a failed or nil read ends the wait. Wait calls read again with retry set after an address that does not match.
// open and read must return when their ctx ends; Wait stops both and waits for them before it returns.
func (c *Callback) Wait(ctx context.Context, uri string, open func(context.Context, string) error, read func(ctx context.Context, retry bool) (string, error)) (string, error) {
	ctx, stop := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer func() { stop(); running.Wait() }()
	// Wait does not wait for open: xdg-open can run a browser in the foreground until it exits.
	running.Go(func() { _ = open(ctx, uri) })
	if read != nil {
		running.Go(func() {
			for retry := false; ctx.Err() == nil; retry = true {
				line, err := read(ctx, retry)
				if err != nil {
					return
				}
				u, err := url.Parse(strings.TrimSpace(line))
				if err == nil && u.Scheme == "http" && u.User == nil && c.accept(u.Host, u.Path, u.RawQuery) {
					return
				}
			}
		})
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case got := <-c.result:
		return got.code, got.err
	}
}
