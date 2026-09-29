package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

func serve(ctx context.Context, home, listen string, stdout, stderr io.Writer, opts server.Options) int {
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
	s, e := server.New(context.WithoutCancel(ctx), home, opts)
	if e != nil {
		return failure(stderr, "server state unavailable")
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

type loginProvider interface {
	Login(context.Context, bool, func(string, string)) (string, []string, error)
}

func login(ctx context.Context, home, account string, device bool, stdout, stderr io.Writer, opts server.Options) int {
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
	if opts.Credentials == nil {
		opts.Credentials, e = credentials.NewMacOS()
		if e != nil {
			return failure(stderr, "credential store unavailable")
		}
	}
	var p loginProvider
	if a.Provider == "gmail" {
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
		return failure(stderr, "provider login failed")
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
