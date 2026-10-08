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
	"sync/atomic"
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
		if e.Error() == "configuration unavailable" || errors.As(e, new(config.ApprovalsError)) {
			return failure(stderr, e.Error())
		}
		return failure(stderr, "server state unavailable")
	}
	if code := preload(ctx, s, stdin, stderr); code != 0 {
		_ = s.Close()
		return code
	}
	var servers []*http.Server
	done := make(chan error, 2)
	start := func(handler http.Handler, l net.Listener) {
		h := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
		servers = append(servers, h)
		go func() { done <- h.Serve(l) }()
	}
	// The approval console runs only with an approval password; the preload lines already say when it is off.
	if console, ok := s.Console(); ok {
		address := s.ConsoleAddress()
		consoleListener, e := net.Listen("tcp", address)
		if e != nil {
			_ = s.Close()
			return failure(stderr, "approval console listener unavailable")
		}
		defer consoleListener.Close()
		if host, _, e := net.SplitHostPort(address); e != nil || !loopback(host) {
			fmt.Fprintln(stderr, "WARNING: the approval console listens beyond loopback. Use an HTTPS tunnel such as tailscale serve.")
		}
		start(console, consoleListener)
		fmt.Fprintln(stderr, "approval console listening")
	}
	start(s, listener)
	fmt.Fprintln(stderr, "server listening")
	select {
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, h := range servers {
			_ = h.Shutdown(drain)
		}
		cancel()
		e = s.Close()
		for _, h := range servers {
			_ = h.Close()
		}
		if e != nil {
			return failure(stderr, "server shutdown failed")
		}
		return 0
	case <-done:
		for _, h := range servers {
			_ = h.Close()
		}
		_ = s.Close()
		return failure(stderr, "server stopped")
	}
}

// loopback reports whether host is localhost or a loopback IP address.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

type loginProvider interface {
	Login(context.Context, bool, func(string, string)) (string, []string, error)
}

func login(ctx context.Context, home, account string, device, noBrowser bool, stdin io.Reader, stdout, stderr io.Writer, opts server.Options, lookup func(string) string) int {
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
		return failure(stderr, configUnavailable(e).Error())
	}
	if noBrowser && a.Provider != "gmail" {
		return failure(stderr, "--no-browser is for Gmail; use --device-code for Microsoft 365")
	}
	opts.Credentials, e = localCredentials(ctx, home, opts.Credentials, stdin, stderr, lookup)
	if e != nil {
		return failure(stderr, e.Error())
	}
	title := "Microsoft 365"
	if a.Provider == "gmail" {
		title = "Gmail"
	}
	fmt.Fprintf(stderr, "\n%s for account %s\n\n", labelStyle.Render(title+" sign-in"), terminalSafe(a.Name))
	// waiting is set while the cursor sits after the paste prompt, so later output starts on a new line.
	var waiting atomic.Bool
	var p loginProvider
	if a.Provider == "gmail" {
		if noBrowser {
			opts.Gmail.OpenBrowser = func(context.Context, string) error { return nil }
		}
		if stdin == nil {
			stdin = os.Stdin
		}
		_, interactive := terminal(stdin)
		opts.Gmail.ReadRedirect = func(ctx context.Context, retry bool) (string, error) {
			switch {
			case retry:
				fmt.Fprintf(stderr, "%s That address is not from this sign-in. Paste the full address again.\n", warningStyle.Render("!"))
			case noBrowser:
				fmt.Fprint(stderr, "2. The browser then fails to load a 127.0.0.1 page. That is expected.\n3. Copy the full address from the address bar and paste it here.\n\n")
			default:
				fmt.Fprint(stderr, "2. If the browser then fails to load a 127.0.0.1 page, copy the full address from the address bar and paste it here.\n\n")
			}
			fmt.Fprint(stderr, labelStyle.Render("Address:")+" ")
			waiting.Store(true)
			// passwordLine reads without buffering; its errors only stop pasted input, and the program never prints the line.
			line, err := passwordLine(ctx, stdin)
			if err == nil && interactive {
				waiting.Store(false) // Enter moved the cursor to a new line.
			}
			if err == nil && line == "" && !interactive {
				return "", io.EOF // passwordLine reports EOF as an empty line; closed input would otherwise repeat forever.
			}
			return line, err
		}
		p = gmail.New(a, opts.Credentials, opts.Gmail)
	} else {
		if !device {
			fmt.Fprintln(stderr, "Complete the sign-in in the browser window that opens.")
		}
		p = m365.New(a, opts.Credentials, opts.M365)
	}
	user, scopes, e := p.Login(ctx, device, func(uri, code string) {
		step := "1. Open this URL in a browser on any computer and approve access:"
		switch {
		case code != "":
			step = "1. Open this URL in a browser on any computer:"
		case !noBrowser:
			step = "1. Approve access in the browser window that opens. If none opens, open this URL:"
		}
		// Bare and on its own line, so terminals can link it and a copy takes nothing extra.
		fmt.Fprintf(stderr, "%s\n\n%s\n\n", step, terminalSafe(uri))
		if code != "" {
			fmt.Fprintf(stderr, "2. Enter this code: %s\n\nWaiting for sign-in...\n", labelStyle.Render(terminalSafe(code)))
		}
	})
	if waiting.Load() {
		fmt.Fprintln(stderr)
	}
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
	fmt.Fprintf(stderr, "\n%s Signed in as %s\n  Restart the server to use the new login.\n", successStyle.Render("\u2713"), terminalSafe(user))
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
