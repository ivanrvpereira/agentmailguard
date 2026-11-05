package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
)

func serveCmd(r *runner) *cobra.Command {
	listen := "127.0.0.1:8787"
	c := r.leaf("serve", "Run the daemon on this host", 0, 0, func(c *cobra.Command, _ []string) int {
		return r.serve(c.Context(), listen)
	})
	c.Flags().StringVar(&listen, "listen", listen, "API listen address")
	return c
}

// serve runs the daemon: lock, API listener, credential store, the first load with its check list, the console
// listener, then requests until ctx ends. Reload requests run the load again inside this process.
func (r *runner) serve(ctx context.Context, listen string) int {
	host, _, e := net.SplitHostPort(listen)
	if e != nil {
		return 2
	}
	home, path, e := r.paths()
	if e != nil {
		return r.fail("configuration unavailable")
	}
	// Ask for reloads before the lock shows the PID, so a signal that amgd sends during the start is not lost and
	// does not stop the process.
	reloads := r.opts.Reloads
	if reloads == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR1)
		defer signal.Stop(ch)
		reloads = ch
	}
	release, e := holdLock(home)
	if e != nil {
		return r.fail(e.Error())
	}
	defer release()
	open := r.opts.Listen
	if open == nil {
		open = net.Listen
	}
	listener, e := open("tcp", listen)
	if e != nil {
		return r.fail("listener unavailable")
	}
	defer listener.Close()
	c, e := config.Load(ctx, path)
	if e != nil {
		return r.fail(e.Error())
	}
	store, e := r.store(ctx, home, c.Backend)
	if e != nil {
		status := "access denied"
		if errors.Is(e, credentials.ErrPasswordMissing) {
			status = "password missing"
		}
		fmt.Fprintln(r.stderr, "credential check: credentials (file): "+status)
		return r.fail(e.Error())
	}
	opts := r.opts.Server
	opts.Store = store
	s, e := server.New(context.WithoutCancel(ctx), home, opts)
	if e != nil {
		return r.fail("daemon state unavailable")
	}
	defer s.Close()
	if problem := r.consolePasswordPrompt(ctx, store, home, c.Console.Username); problem != "" {
		return r.fail(problem)
	}
	fmt.Fprintln(r.stderr, "checking credentials")
	result, e := s.Load(ctx, r.screeningKeyPrompt())
	r.report(home, result, e)
	if e != nil {
		if ctx.Err() != nil {
			return r.fail("credential check canceled")
		}
		return r.fail("start refused; fix the items above and start the daemon again")
	}
	c, password := s.Current()
	if !password {
		// Without a password, the first visitor of the console could set one; an agent on this host could be first.
		return r.fail("start refused: run " + r.command("amgd config set console.password", home) + ", then start the daemon again")
	}
	address := s.ConsoleAddress()
	consoleListener, e := open("tcp", address)
	if e != nil {
		return r.fail("console listener unavailable")
	}
	defer consoleListener.Close()
	if host, _, e := net.SplitHostPort(address); e != nil || !loopback(host) {
		fmt.Fprintln(r.stderr, "WARNING: the approval console listens beyond loopback. Use an HTTPS tunnel such as tailscale serve.")
	}
	var servers []*http.Server
	done := make(chan error, 2)
	start := func(handler http.Handler, l net.Listener) {
		h := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
		servers = append(servers, h)
		go func() { done <- h.Serve(l) }()
	}
	start(s.Console(), consoleListener)
	fmt.Fprint(r.stderr, term.Safe(consoleLines(c.Console)))
	start(s, listener)
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	fmt.Fprint(r.stderr, term.Safe(apiLines(host, port, c.Accounts)))
	notifySystemd(r.opts.LookupEnv, "READY=1")
	reloaded := make(chan struct{})
	go func() {
		defer close(reloaded)
		// One reload runs at a time. The signal channel holds one request, so signals during a reload cause exactly
		// one more reload after it.
		for {
			select {
			case <-ctx.Done():
				return
			case <-reloads:
			}
			notifySystemd(r.opts.LookupEnv, reloading())
			fmt.Fprintln(r.stderr, "reload requested")
			result, err := s.Reload(ctx)
			r.report(home, result, err)
			switch {
			case err == nil:
				fmt.Fprintln(r.stderr, "reload ok")
			case errors.Is(err, server.ErrReloadAudit):
				fmt.Fprintln(r.stderr, "reload ok")
				term.ErrorLine(r.stderr, err.Error())
			default:
				fmt.Fprintf(r.stderr, "reload refused: %s; old settings kept\n", err.Error())
			}
			notifySystemd(r.opts.LookupEnv, "READY=1")
		}
	}()
	select {
	case <-ctx.Done():
		notifySystemd(r.opts.LookupEnv, "STOPPING=1")
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, h := range servers {
			_ = h.Shutdown(drain)
		}
		cancel()
		<-reloaded
		e = s.Close()
		for _, h := range servers {
			_ = h.Close()
		}
		if e != nil {
			return r.fail("daemon shutdown failed")
		}
		return 0
	case <-done:
		for _, h := range servers {
			_ = h.Close()
		}
		return r.fail("daemon stopped")
	}
}

// report writes the check list, the detail of a refusal, warnings, and restart reminders of a load.
func (r *runner) report(home string, result server.Result, err error) {
	for _, c := range result.Checks {
		status := c.Status
		// A check that ends with a command to copy gets the home of this daemon, as the refusal does.
		for _, cmd := range []string{"amgd config set console.password", "amgd config set screening.api_key"} {
			if before, ok := strings.CutSuffix(status, cmd); ok {
				status = before + r.command(cmd, home)
			}
		}
		fmt.Fprintf(r.stderr, "credential check: %s: %s\n", c.Item, status)
	}
	if e, ok := errors.AsType[server.LoadError](err); ok && e.Err != nil {
		fmt.Fprintln(r.stderr, "error: "+quoted(e.Err.Error()))
	}
	for _, w := range result.Warnings {
		fmt.Fprintln(r.stderr, "WARNING: "+w)
	}
	for _, key := range result.Restart {
		fmt.Fprintf(r.stderr, "restart needed: %s changed\n", key)
	}
}

// screeningKeyPrompt asks for a missing screening key at the start, only on a terminal.
func (r *runner) screeningKeyPrompt() func(context.Context) (string, error) {
	if _, ok := terminal(r.stdin); !ok {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		key, problem := readScreeningKey(ctx, r.stdin, r.stderr, "Screening key not found. Enter the screening API key to save it: ")
		if problem != "" {
			r.fail(problem)
			return "", errors.New(problem)
		}
		return key, nil
	}
}

// consolePasswordPrompt asks for a console password at the start when the credential store has none, only on a
// terminal. Without a terminal the start is refused after the check list. Other store errors are left to the check
// list too.
func (r *runner) consolePasswordPrompt(ctx context.Context, store *credentials.RecordStore, home, user string) string {
	if _, ok := terminal(r.stdin); !ok {
		return ""
	}
	if _, err := store.LoadConsoleHash(ctx); !errors.Is(err, credentials.ErrNotFound) {
		return ""
	}
	fmt.Fprintln(r.stderr, term.Safe("Console password not set. Choose one now; you sign in to the console as "+user+" with it."))
	if problem := r.setConsolePassword(ctx, store); problem != "" {
		return problem
	}
	if r.audit(home).Credential(context.WithoutCancel(ctx), "console_password_set") != nil {
		return "audit record failed"
	}
	return ""
}

// consoleLines tell where the approval console is: its URL from [console].listen, as in the pending url, the extra
// host names that it accepts, and the username.
func consoleLines(c config.Console) string {
	out := fmt.Sprintf("approval console: http://%s/ (user %s)\n", c.Listen, c.Username)
	if len(c.Hosts) > 0 {
		out += "  also accepted as: " + strings.Join(c.Hosts, ", ") + " (from console.hosts)\n"
	}
	return out
}

// apiLines tell where the API listens and how agents connect. Beyond loopback they warn that anyone who reaches
// the address can use the API, with the mail_send decisions of the accounts, and name the fixes.
func apiLines(host, port string, accounts []config.Account) string {
	address := net.JoinHostPort(host, port)
	// url.URL escapes the zone of a scoped IPv6 address (%25), which amg's URL parser needs.
	out := "daemon listening: " + (&url.URL{Scheme: "http", Host: address}).String() + "\n"
	reach := address
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		// A wildcard address is not one that agents can connect to; the daemon does not guess its host name.
		address = net.JoinHostPort("DAEMON-HOST", port)
		reach = "port " + port + " on any address of this host"
	}
	out += "  agents connect with: AMG_HOST=" + (&url.URL{Scheme: "http", Host: address}).String() + " amg account list\n"
	if loopback(host) {
		return out
	}
	var sends []string
	for _, d := range []config.Decision{config.Allow, config.Approve, config.Deny} {
		if slices.ContainsFunc(accounts, func(a config.Account) bool { return a.Policy.Base.MailSend == d }) {
			sends = append(sends, string(d))
		}
	}
	policy := ""
	if len(sends) > 0 {
		policy = " (sends: " + strings.Join(sends, " or ") + ")"
	}
	return out + "WARNING: this API has no login and no TLS. Anyone who can reach " + reach + " can read screened mail and run every action that the write policy allows" + policy + ".\n" +
		"  Restrict it: allow only the agent machine (Tailscale grant or firewall), or keep --listen on 127.0.0.1 and use an SSH tunnel. See docs/daemon.md#local-and-remote-clients\n"
}

// loopback reports whether host is localhost or a loopback IP address.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// notifySystemd sends a state to systemd when NOTIFY_SOCKET is set, for Type=notify and Type=notify-reload. It
// ignores every failure: the daemon runs the same without systemd.
func notifySystemd(lookup func(string) string, state string) {
	path := lookup("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	if strings.HasPrefix(path, "@") {
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}
