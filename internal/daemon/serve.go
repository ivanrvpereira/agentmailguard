package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(r.stderr, "WARNING: unauthenticated mailbox access; no built-in TLS. A protected network or tunnel is required.")
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
	listener, e := net.Listen("tcp", listen)
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
	fmt.Fprintln(r.stderr, "checking credentials")
	result, e := s.Load(ctx, r.screeningKeyPrompt())
	r.report(result, e)
	if e != nil {
		if ctx.Err() != nil {
			return r.fail("credential check canceled")
		}
		return r.fail("start refused; fix the items above and start the daemon again")
	}
	address := s.ConsoleAddress()
	consoleListener, e := net.Listen("tcp", address)
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
	fmt.Fprintln(r.stderr, "approval console listening")
	start(s, listener)
	fmt.Fprintln(r.stderr, "daemon listening")
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
			r.report(result, err)
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
func (r *runner) report(result server.Result, err error) {
	for _, c := range result.Checks {
		fmt.Fprintf(r.stderr, "credential check: %s: %s\n", c.Item, c.Status)
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
