// Package daemon is amgd, the command of the daemon host: it runs the daemon (serve) and holds every management
// command (reload, account, config, store, cache). The API never manages the daemon; these commands do, on the host.
package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
	"github.com/ivanrvpereira/agentmailguard/internal/version"
)

// Options are the external seams of amgd. Zero values use the real host.
type Options struct {
	// Platform is the Keychain seam; nil uses the macOS Keychain.
	Platform credentials.Platform
	// Server holds the provider and screening seams. serve builds Server.Store itself.
	Server    server.Options
	LookupEnv func(string) string
	// Stdin is read for passwords, the screening key, a pasted Gmail address, and confirmations; nil is os.Stdin.
	Stdin io.Reader
	// Notify sends the reload signal to the daemon with the PID; nil sends SIGUSR1.
	Notify func(pid int) error
	// Reloads delivers reload requests to serve; nil listens for SIGUSR1.
	Reloads <-chan os.Signal
	// Listen opens the API and console listeners of serve; nil is net.Listen.
	Listen func(network, address string) (net.Listener, error)
	Now    func() time.Time
}

func (Options) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "DaemonOptions{redacted}") }

const notes = `amgd runs the agentmailguard daemon (amgd serve) and manages it on this host: accounts, settings, secrets, and the credential store.
amg, the agent's client, has only the API commands. The API never manages the daemon.
The daemon reads config.toml, rules.toml, and the credential store at start and at a reload. Commands that change them reload the daemon with SIGUSR1; after a hand edit of config.toml, run amgd reload.
The home is --home, then AMG_HOME, then ~/.agentmailguard. Use one home per machine: homes share one Keychain item.
The API listener has no client authentication and no built-in TLS. Anyone who reaches it can run every action that the write policy allows. Protect remote access with a tunnel.`

var errUsage = errors.New("invalid command usage")
var errDone = errors.New("command finished")

type runner struct {
	opts           Options
	passwordEnv    func(string) string
	stdin          io.Reader
	stdout, stderr io.Writer
	home           string
	code           int
}

// Run runs one amgd command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) (code int) {
	// A nil lookup keeps the difference between a missing and an empty process password.
	passwordEnv := opts.LookupEnv
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.Getenv
	}
	stderr = term.Colorized(stderr, opts.LookupEnv)
	defer func() {
		if recover() != nil {
			code = term.Failure(stderr, "operation failed")
		}
	}()
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	r := &runner{opts: opts, passwordEnv: passwordEnv, stdin: stdin, stdout: stdout, stderr: stderr}
	var help bytes.Buffer
	root := &cobra.Command{Use: "amgd", Short: "Run and manage the agentmailguard daemon on this host", Long: notes, Version: version.String(), Args: cobra.ArbitraryArgs, SilenceErrors: true, SilenceUsage: true}
	root.RunE = func(c *cobra.Command, rest []string) error {
		if len(rest) > 1 || len(rest) == 0 && len(args) > 0 {
			return errUsage
		}
		if len(rest) == 1 {
			r.code = 2
		}
		return c.Help()
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(&cobra.Command{Use: "no-help-command", Hidden: true, RunE: func(*cobra.Command, []string) error { return errUsage }})
	root.PersistentFlags().StringVar(&r.home, "home", "", "daemon home (default from AMG_HOME, else ~/.agentmailguard)")
	root.AddCommand(serveCmd(r), reloadCmd(r), accountCmd(r), configCmd(r), storeCmd(r), cacheCmd(r))
	root.SetOut(&help)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{}, args...))
	root.SetContext(ctx)
	cmd, err := root.ExecuteC()
	if cmd != nil && cmd.Name() == cobra.ShellCompRequestCmd {
		return term.Usage(stderr)
	}
	if help.Len() > 0 {
		return term.Output(stdout, stderr, help.String(), r.code)
	}
	if err != nil && !errors.Is(err, errDone) {
		return term.Usage(stderr)
	}
	return r.code
}

// group is a command that only holds subcommands; without one it prints its own help and exits 2.
func group(r *runner, use, short string, children ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return errUsage
		}
		r.code = 2
		return c.Help()
	}}
	c.AddCommand(children...)
	return c
}

// leaf is a runnable command with between min and max positional arguments. run returns the exit code.
func (r *runner) leaf(use, short string, min, max int, run func(c *cobra.Command, args []string) int) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return errUsage
		}
		code := run(c, args)
		if code == 2 {
			return errUsage
		}
		r.code = code
		return errDone
	}}
}

func (r *runner) fail(message string) int { return term.Failure(r.stderr, message) }

// paths returns the home and the path of config.toml.
func (r *runner) paths() (string, string, error) {
	home, err := config.Home(r.home)
	if err != nil {
		return "", "", err
	}
	return home, filepath.Join(home, "config.toml"), nil
}

func (r *runner) audit(home string) *audit.Log { return audit.New(filepath.Join(home, "audit.jsonl")) }

// store opens the credential store of the backend. The file backend takes its password from
// AMG_CREDENTIALS_PASSWORD, else from stdin; creating means that a new file is made and asks twice on a terminal.
func (r *runner) store(ctx context.Context, home, backend string) (*credentials.RecordStore, error) {
	password, present := credentials.TakePasswordEnv(r.passwordEnv)
	if backend != "file" {
		if r.opts.Platform != nil {
			return credentials.New(r.opts.Platform, home), nil
		}
		s, err := credentials.NewMacOS(home)
		if err != nil {
			return nil, errors.New("credential store unavailable")
		}
		return s, nil
	}
	if !present {
		_, err := os.Stat(filepath.Join(home, "credentials.age"))
		password, err = readCredentialPassword(ctx, r.stdin, r.stderr, errors.Is(err, os.ErrNotExist))
		if err != nil {
			return nil, err
		}
	}
	if err := credentials.ValidatePassword(password); err != nil {
		return nil, err
	}
	return credentials.NewFile(home, password), nil
}

// reload asks the daemon of home to load its files again. It sends the signal only while the daemon holds its
// lock, and does not wait for the result: the daemon log shows it.
func (r *runner) reload(home string) error {
	pid, err := daemonPID(home)
	if err != nil {
		return err
	}
	notify := r.opts.Notify
	if notify == nil {
		notify = func(pid int) error { return syscall.Kill(pid, syscall.SIGUSR1) }
	}
	if notify(pid) != nil {
		return errors.New("cannot signal the daemon")
	}
	fmt.Fprintf(r.stderr, "reload sent to daemon (PID %d); see the daemon log\n", pid)
	return nil
}

// changed reloads the daemon after a change. Without a daemon the change waits for the next start.
func (r *runner) changed(home string) int {
	err := r.reload(home)
	switch {
	case errors.Is(err, errNoDaemon):
		fmt.Fprintln(r.stderr, term.Safe("no daemon runs for this home. Now run: "+r.command("amgd serve", home)))
	case err != nil:
		return r.fail(err.Error() + "; the change is saved, run amgd reload")
	}
	return 0
}

func reloadCmd(r *runner) *cobra.Command {
	return r.leaf("reload", "Make the daemon read config.toml, rules.toml, and the credential store again", 0, 0, func(*cobra.Command, []string) int {
		home, _, err := r.paths()
		if err != nil {
			return r.fail("configuration unavailable")
		}
		if err := r.reload(home); err != nil {
			return r.fail(err.Error())
		}
		return 0
	})
}

// quoted renders operator text for a message line.
func quoted(s string) string { return term.Safe(strings.TrimSpace(s)) }

// command is an amgd command line to copy for home. The same sources as config.Home: a home from --home or AMG_HOME
// goes into it, so the command reaches the same home in another shell.
func (r *runner) command(line, home string) string {
	if r.home != "" || os.Getenv("AMG_HOME") != "" {
		line += " --home " + shellWord(home)
	}
	return line
}

// shellWord quotes s for a POSIX shell when it holds more than letters, digits, and / . _ - + : , = @.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/._-+:,=@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
