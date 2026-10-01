package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func serve(ctx context.Context, home, listen string, stdin io.Reader, stderr io.Writer, opts server.Options, lookup func(string) string) int {
	host, _, e := net.SplitHostPort(listen)
	if e != nil {
		return usage(stderr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(stderr, "WARNING: unauthenticated mailbox access; no built-in TLS. A protected network or tunnel is required.")
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		return failure(stderr, "listener unavailable")
	}
	defer listener.Close()
	opts.Credentials, e = localCredentials(ctx, home, opts.Credentials, stdin, stderr, lookup)
	if e != nil {
		var passwordError credentialPasswordError
		if errors.As(e, &passwordError) {
			status := "access denied"
			if errors.Is(passwordError.error, credentials.ErrPasswordMissing) {
				status = "password missing"
			}
			fmt.Fprintln(stderr, "credential check: credentials (file): "+status)
		}
		return failure(stderr, e.Error())
	}
	s, e := server.New(context.WithoutCancel(ctx), home, opts)
	if e != nil {
		if e.Error() == "configuration unavailable" {
			return failure(stderr, "configuration unavailable")
		}
		return failure(stderr, "server state unavailable")
	}
	if code := preload(ctx, s, stdin, stderr); code != 0 {
		_ = s.Close()
		return code
	}
	h := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- h.Serve(listener) }()
	fmt.Fprintln(stderr, "server listening")
	select {
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.Shutdown(drain)
		cancel()
		e = s.Close()
		_ = h.Close()
		if e != nil {
			return failure(stderr, "server shutdown failed")
		}
		return 0
	case <-done:
		_ = s.Close()
		return failure(stderr, "server stopped")
	}
}

// preload asks the credential backend for everything before the server accepts requests,
// because an operator is present at start. A missing Jev key is requested only on a terminal.
func preload(ctx context.Context, s *server.Server, stdin io.Reader, stderr io.Writer) int {
	if stdin == nil {
		stdin = os.Stdin
	}
	var prompt func(context.Context) (string, error)
	if _, ok := terminal(stdin); ok {
		prompt = func(ctx context.Context) (string, error) {
			key, problem := readJevKey(ctx, stdin, stderr, "Jev key not found. Enter the Jev (OpenRouter) API key to save it: ")
			if problem != "" {
				errorLine(stderr, problem)
				return "", errors.New(problem)
			}
			return key, nil
		}
	}
	fmt.Fprintln(stderr, "checking credentials")
	checks, e := s.Preload(ctx, prompt)
	for _, c := range checks {
		fmt.Fprintf(stderr, "credential check: %s: %s\n", c.Item, c.Status)
	}
	switch {
	case e == nil:
		return 0
	case ctx.Err() != nil:
		return failure(stderr, "credential check canceled")
	case e.Error() == "configuration unavailable":
		return failure(stderr, "configuration unavailable")
	default:
		return failure(stderr, "credential check failed; fix the items above and restart the server")
	}
}

// readCallback consumes one bounded line without buffering a later stdin line.
func readCallback(ctx context.Context, stdin io.Reader, stderr io.Writer) (string, error) {
	if stdin == nil {
		stdin = os.Stdin
	}
	if f, ok := terminal(stdin); ok {
		restore, err := hideTerminalInput(ctx, f)
		if err != nil {
			return "", err
		}
		defer func() { restore(); fmt.Fprintln(stderr) }()
	}
	fmt.Fprint(stderr, "Paste the full callback URL (input hidden on terminals), then press Enter: ")
	type result struct {
		raw string
		err error
	}
	done := make(chan result, 1)
	// ponytail: an arbitrary Reader can block until input closes; cancellation must still finish the CLI.
	go func() {
		var data []byte
		var one [1]byte
		for {
			if ctx.Err() != nil {
				done <- result{err: ctx.Err()}
				return
			}
			n, err := stdin.Read(one[:])
			if n > 0 {
				if one[0] == '\n' {
					done <- result{raw: strings.TrimSuffix(string(data), "\r")}
					return
				}
				data = append(data, one[0])
				if len(data) > 4096 {
					done <- result{err: errors.New("callback input too long")}
					return
				}
			}
			if err != nil {
				done <- result{err: errors.New("callback input unavailable")}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		return r.raw, r.err
	}
}

type loginProvider interface {
	Login(context.Context, bool, func(string, string)) (string, []string, error)
}

func login(ctx context.Context, home, account string, device, manual bool, stdin io.Reader, stdout, stderr io.Writer, opts server.Options, lookup func(string) string) int {
	home, e := config.Home(home)
	if e != nil {
		return failure(stderr, "configuration unavailable")
	}
	var name *string
	if account != "" {
		name = &account
	}
	a, e := config.LoadAccount(ctx, filepath.Join(home, "config.toml"), name)
	if e != nil {
		return failure(stderr, "configuration unavailable")
	}
	if manual && a.Provider != "gmail" {
		return failure(stderr, "manual login is Gmail only")
	}
	opts.Credentials, e = localCredentials(ctx, home, opts.Credentials, stdin, stderr, lookup)
	if e != nil {
		return failure(stderr, e.Error())
	}
	var p loginProvider
	if a.Provider == "gmail" {
		if manual {
			opts.Gmail.ReadCallback = func(ctx context.Context) (string, error) { return readCallback(ctx, stdin, stderr) }
		}
		p = gmail.New(a, opts.Credentials, opts.Gmail)
	} else {
		p = m365.New(a, opts.Credentials, opts.M365)
	}
	fmt.Fprintln(stderr, "Sign in with the provider. Restart the server after successful login.")
	user, scopes, e := p.Login(ctx, device, func(uri, code string) {
		fmt.Fprintf(stderr, "Provider sign-in URL: %q\n", uri)
		if code != "" {
			fmt.Fprintf(stderr, "Device code: %q\n", code)
		}
	})
	if e != nil {
		var diagnostic m365.LoginError
		if errors.As(e, &diagnostic) {
			return failure(stderr, diagnostic.Message())
		}
		return failure(stderr, "provider login failed")
	}
	extra := m365.ExtraScopes(scopes)
	if a.Provider == "gmail" {
		extra = gmail.ExtraScopes(scopes)
	}
	if len(extra) != 0 {
		warnExtraScopes(stderr, extra)
	}
	out := struct {
		Account  string   `json:"account"`
		Username string   `json:"username"`
		Scopes   []string `json:"scopes"`
	}{a.Name, user, scopes}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if enc.Encode(out) != nil {
		return failure(stderr, "output failed")
	}
	return 0
}
