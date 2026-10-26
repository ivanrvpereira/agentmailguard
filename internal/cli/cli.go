// Package cli is the testable command entrypoint. Data commands only use HTTP.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard/internal/client"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

type Options struct {
	CredentialPlatform credentials.Platform // optional native Keychain dependency for local migration
	Client             client.Options
	Server             server.Options
	LookupEnv          func(string) string
	Now                func() time.Time
	Stdin              io.Reader // defaults to os.Stdin; read by auth set-screening-key and set-approval-password, by auth login for a pasted Gmail address, and by serve for a missing screening key
}

func (Options) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "CLIOptions{redacted}") }

const notes = `All data commands use the server. No automatic server start. No permanent delete.
mail send, reply, and forward send email at once, and calendar invite, update, cancel, and respond (unless --no-notify) send meeting notifications at once, when the write policy allows them (default: deny).
serve checks all credentials at start. An account that is not logged in, or whose sign-in fails, does not stop the start; its commands fail. Other credential failures stop the start.
Login runs locally on the server host. Restart the server after login or account changes.
Days in date options run from 00:00 to the end of the day on this machine's clock and time zone; the last day is included.
Client/server authentication and built-in TLS are absent. Anyone who reaches the server can run every action that the write policy allows. Protect remote access with a tunnel.`

// errUsage reports a rejected command line; errDone reports a finished command that already set its code.
var errUsage = errors.New("invalid command usage")
var errDone = errors.New("command finished")

// errorLine writes one message line; the prefix may carry color, the words never change. Run gives
// every caller a message writer that keeps, downgrades, or removes the color.
func errorLine(w io.Writer, message string) {
	fmt.Fprintln(w, errorStyle.Render("error:")+" "+message)
}
func usage(w io.Writer) int                   { errorLine(w, "invalid command usage; use --help"); return 2 }
func failure(w io.Writer, message string) int { errorLine(w, message); return 1 }

// Scope labels come from provider tokens, so each one is quoted.
func warnExtraScopes(w io.Writer, extra []string) {
	quoted := make([]string, len(extra))
	note := ""
	for i, s := range extra {
		quoted[i] = strconv.Quote(s)
		if strings.HasPrefix(s, "role:") {
			note = " role: entries are application permissions."
		}
	}
	fmt.Fprintf(w, "%s token grants extra scopes the server does not use: %s.%s Remove the extra consent to reduce exposure.\n", warningStyle.Render("warning:"), strings.Join(quoted, ", "), note)
}
func output(stdout, stderr io.Writer, text string, code int) int {
	if _, err := io.WriteString(stdout, text); err != nil {
		return failure(stderr, "output failed")
	}
	return code
}

// runner holds the per-invocation state of one command tree. Run builds a new one every call.
type runner struct {
	opts           Options
	passwordEnv    func(string) string
	stdout, stderr io.Writer
	account        string
	base           string
	home           string
	timeout        time.Duration
	code           int
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) (code int) {
	// A nil lookup preserves the distinction between missing and explicitly empty process passwords.
	passwordEnv := opts.LookupEnv
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.Getenv
	}
	// Messages carry color for one run at most, decided once from the injected writer and environment.
	stderr = colorized(stderr, opts.LookupEnv)
	defer func() {
		if recover() != nil {
			code = failure(stderr, "operation failed")
		}
	}()
	r := &runner{opts: opts, passwordEnv: passwordEnv, stdout: stdout, stderr: stderr}
	// Help goes to a buffer so a failed write is reported once, and so Cobra never writes a partial page.
	// Everything else Cobra prints, including messages that quote the command line, is discarded.
	var help bytes.Buffer
	root := newRoot(r, args)
	root.SetOut(&help)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{}, args...))
	root.SetContext(ctx)
	cmd, err := root.ExecuteC()
	// Cobra always registers a hidden completion request command; shell completion is not part of this interface.
	if cmd != nil && cmd.Name() == cobra.ShellCompRequestCmd {
		return usage(stderr)
	}
	if help.Len() > 0 {
		return output(stdout, stderr, help.String(), r.code)
	}
	if err != nil && !errors.Is(err, errDone) {
		return usage(stderr)
	}
	return r.code
}

func newRoot(r *runner, raw []string) *cobra.Command {
	root := &cobra.Command{
		Use:           "agentmailguard",
		Short:         "Screened mailbox access through the agentmailguard server",
		Long:          notes,
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	// No word at all prints help and succeeds; one unknown word prints help and fails; more is a usage error.
	root.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 1 || (len(args) == 0 && len(raw) > 0) {
			return errUsage
		}
		if len(args) == 1 {
			r.code = 2
		}
		return c.Help()
	}
	root.CompletionOptions.DisableDefaultCmd = true
	// Cobra always registers a help command and its help template lists any command named "help" even when
	// hidden. This interface has none, so the registered one gets another name and "help" stays an unknown word.
	root.SetHelpCommand(&cobra.Command{Use: "no-help-command", Hidden: true, RunE: func(*cobra.Command, []string) error { return errUsage }})
	f := root.PersistentFlags()
	f.StringVar(&r.account, "account", "", "account name from the server configuration")
	f.StringVar(&r.base, "server", "", "server base URL")
	f.DurationVar(&r.timeout, "timeout", client.DefaultTimeout, "request timeout")
	f.StringVar(&r.home, "home", "", "configuration directory; serve, auth login, set-screening-key, set-approval-password, migrate, and change-password only")
	root.AddCommand(serveCmd(r), authCmd(r), statsCmd(r), cacheCmd(r), rulesCmd(r), approvalsCmd(r), mailCmd(r), calendarCmd(r), draftCmd(r))
	return root
}

// group is a command that only holds subcommands; without one it prints its own help and exits 2.
func group(r *runner, use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return errUsage
		}
		r.code = 2
		return c.Help()
	}}
}

// leaf is a runnable command. It refuses an explicit blank account before any state, network, or local
// access, then checks the positional count; max below zero accepts any number. Both run before the
// option checks inside run, so a rejected command line keeps the order of the hand-written parser.
func (r *runner) leaf(use, short string, min, max int, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("account") && strings.TrimSpace(r.account) == "" {
			r.code = failure(r.stderr, "account must not be blank")
			return errDone
		}
		if len(args) < min || (max >= 0 && len(args) > max) {
			return errUsage
		}
		return run(c, args)
	}}
}

// none runs a data command that takes no positional argument and no parameter.
func (r *runner) none(op string) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, _ []string) error { return r.call(c, op, map[string]any{}, false) }
}

// ids runs a data command over one or more message identifiers.
func (r *runner) ids(op string) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		return r.call(c, op, map[string]any{"ids": args}, false)
	}
}

// optional copies a string option into the parameters only when the command line set it.
func optional(c *cobra.Command, p map[string]any, name string, value *string) {
	if c.Flags().Changed(name) {
		p[name] = *value
	}
}

// negated adds --NAME and --no-NAME; the last option on the command line wins. *target is the default.
func negated(c *cobra.Command, name, short string, target *bool) {
	c.Flags().BoolVar(target, name, *target, short)
	c.Flags().Var(negatedValue{target}, "no-"+name, "turn off --"+name)
	c.Flags().Lookup("no-" + name).NoOptDefVal = "true"
}

// negatedValue stores the opposite of the parsed value. It reports a zero default so help omits one.
type negatedValue struct{ target *bool }

func (negatedValue) String() string { return "false" }
func (negatedValue) Type() string   { return "bool" }
func (n negatedValue) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err == nil {
		*n.target = !b
	}
	return err
}

// resolve applies the option checks shared by every command and returns the server base
// plus whether a server was supplied by option or environment.
func (r *runner) resolve(c *cobra.Command, allowHome bool) (bool, string, error) {
	if c.Flags().Changed("home") && !allowHome {
		return false, "", errUsage
	}
	if r.timeout <= 0 {
		return false, "", errUsage
	}
	envServer := r.opts.LookupEnv("AGENTMAILGUARD_SERVER")
	base := r.base
	if !c.Flags().Changed("server") {
		base = envServer
	}
	if base == "" {
		base = client.DefaultServer
	}
	return c.Flags().Changed("server") || envServer != "", base, nil
}

// call runs a data operation over HTTP and writes the response once.
func (r *runner) call(c *cobra.Command, op string, p map[string]any, pretty bool) error {
	_, base, err := r.resolve(c, false)
	if err != nil {
		return err
	}
	clientOpts := r.opts.Client
	clientOpts.Timeout = r.timeout
	cl, e := client.New(base, clientOpts)
	if e != nil {
		r.code = failure(r.stderr, "invalid server URL")
		return errDone
	}
	b, e := cl.Call(c.Context(), op, r.account, p)
	if e != nil {
		var remote *client.Error
		if op == "mail.organize" && errors.As(e, &remote) && remote.Status == 400 {
			return errUsage
		}
		r.code = failure(r.stderr, e.Error())
		return errDone
	}
	text := ""
	switch {
	case pretty && op == "mail.get":
		var v views.MailView
		if json.Unmarshal(b, &v) != nil {
			r.code = failure(r.stderr, "invalid server response")
			return errDone
		}
		text = styleText(views.ToText(v), v.Verdict) + "\n"
	case pretty && (op == "mail.list" || op == "mail.search"):
		var page struct {
			Messages *[]views.MailSummary `json:"messages"`
			NextPage string               `json:"next_page"`
		}
		if json.Unmarshal(b, &page) != nil || page.Messages == nil {
			r.code = failure(r.stderr, "invalid server response")
			return errDone
		}
		text = summaryTable(*page.Messages) + "\n"
		if page.NextPage != "" {
			text += "next_page: " + terminalSafe(page.NextPage) + "\n"
		}
	default:
		var indented bytes.Buffer
		if json.Indent(&indented, bytes.TrimSpace(b), "", "  ") != nil {
			r.code = failure(r.stderr, "invalid server response")
			return errDone
		}
		text = indented.String() + "\n"
	}
	// JSON output never carries color; styled text passes the writer's profile.
	stdout := r.stdout
	if pretty {
		stdout = colorized(r.stdout, r.opts.LookupEnv)
	}
	if output(stdout, r.stderr, text, 0) != 0 {
		r.code = 1
		return errDone
	}
	if op == "auth.status" {
		var v struct {
			OK            bool      `json:"ok"`
			LoggedIn      bool      `json:"logged_in"`
			ExtraScopes   *[]string `json:"extra_scopes"`
			MissingScopes *[]string `json:"missing_scopes"`
		}
		if json.Unmarshal(b, &v) != nil {
			r.code = failure(r.stderr, "invalid server response")
			return errDone
		}
		if v.OK && v.ExtraScopes != nil {
			warnExtraScopes(r.stderr, *v.ExtraScopes)
		}
		if !v.OK {
			switch {
			case v.MissingScopes != nil:
				r.code = failure(r.stderr, "authentication refused: required scopes missing")
			case v.LoggedIn:
				r.code = failure(r.stderr, "authentication failed")
			default:
				r.code = 1
			}
		}
	}
	return errDone
}

func serveCmd(r *runner) *cobra.Command {
	listen := "127.0.0.1:8787"
	c := r.leaf("serve", "Run the mailbox server on this host", 0, 0, func(c *cobra.Command, _ []string) error {
		supplied, _, err := r.resolve(c, true)
		if err != nil {
			return err
		}
		if supplied || c.Flags().Changed("account") {
			return errUsage
		}
		if r.opts.Server.LookupEnv == nil {
			r.opts.Server.LookupEnv = r.opts.LookupEnv
		}
		r.code = serve(c.Context(), r.home, listen, r.opts.Stdin, r.stderr, r.opts.Server, r.passwordEnv)
		return errDone
	})
	c.Flags().StringVar(&listen, "listen", listen, "listen address")
	return c
}

func authCmd(r *runner) *cobra.Command {
	device, noBrowser := false, false
	signIn := r.leaf("login", "Sign in to a provider on this host", 0, 0, func(c *cobra.Command, _ []string) error {
		supplied, _, err := r.resolve(c, true)
		if err != nil {
			return err
		}
		if supplied {
			r.code = failure(r.stderr, "login is local only; remove --server and AGENTMAILGUARD_SERVER")
			return errDone
		}
		r.code = login(c.Context(), r.home, r.account, device, noBrowser, r.opts.Stdin, r.stdout, r.stderr, r.opts.Server, r.passwordEnv)
		return errDone
	})
	negated(signIn, "device-code", "use the provider device-code flow", &device)
	signIn.Flags().BoolVar(&noBrowser, "no-browser", false, "print the Gmail sign-in URL without opening a browser")
	screeningKey := r.leaf("set-screening-key", "Save the screening key on this host from stdin", 0, 0, func(c *cobra.Command, _ []string) error {
		supplied, _, err := r.resolve(c, true)
		if err != nil {
			return err
		}
		if c.Flags().Changed("account") {
			return errUsage
		}
		if supplied {
			r.code = failure(r.stderr, "Screening key setup is local only; remove --server and AGENTMAILGUARD_SERVER")
			return errDone
		}
		r.code = setScreeningKey(c.Context(), r.home, r.opts.Stdin, r.stderr, r.opts.Server, r.passwordEnv)
		return errDone
	})
	approvalPassword := r.leaf("set-approval-password", "Set the approval console password on this host from stdin", 0, 0, func(c *cobra.Command, _ []string) error {
		supplied, _, err := r.resolve(c, true)
		if err != nil {
			return err
		}
		if c.Flags().Changed("account") {
			return errUsage
		}
		if supplied {
			r.code = failure(r.stderr, "approval password setup is local only; remove --server and AGENTMAILGUARD_SERVER")
			return errDone
		}
		r.code = setApprovalPassword(c.Context(), r.home, r.opts.Stdin, r.stderr, r.opts.Server, r.passwordEnv)
		return errDone
	})
	c := group(r, "auth", "Provider sign-in and credential setup")
	c.AddCommand(signIn, r.leaf("status", "Report account sign-in and scope state", 0, 0, r.none("auth.status")), screeningKey, approvalPassword,
		credentialCmd(r, "migrate", "Copy credentials to the other local backend"),
		credentialCmd(r, "change-password", "Change the local credential file password"))
	return c
}

func credentialCmd(r *runner, name, short string) *cobra.Command {
	target := ""
	c := r.leaf(name, short, 0, 0, func(c *cobra.Command, _ []string) error {
		supplied, _, err := r.resolve(c, true)
		if err != nil {
			return err
		}
		if c.Flags().Changed("account") {
			return errUsage
		}
		if supplied {
			r.code = failure(r.stderr, "credential commands are local only; remove --server and AGENTMAILGUARD_SERVER")
			return errDone
		}
		if name == "migrate" && target != "file" && target != "keychain" {
			return errUsage
		}
		r.code = credentialCommand(c.Context(), "auth."+name, r.home, target, r.stdout, r.stderr, r.opts, r.passwordEnv)
		return errDone
	})
	if name == "migrate" {
		c.Flags().StringVar(&target, "to", "", "target credential backend: file or keychain")
	}
	return c
}

func statsCmd(r *runner) *cobra.Command {
	days := 30
	c := r.leaf("stats", "Report screening counts for a recent period", 0, 0, func(c *cobra.Command, _ []string) error {
		if days < 1 {
			return errUsage
		}
		return r.call(c, "stats", map[string]any{"days": days}, false)
	})
	c.Flags().IntVar(&days, "days", days, "number of days to report")
	return c
}

func cacheCmd(r *runner) *cobra.Command {
	c := group(r, "cache", "Screening cache maintenance")
	c.AddCommand(r.leaf("clear", "Discard cached screening results", 0, 0, r.none("cache.clear")))
	return c
}

func rulesCmd(r *runner) *cobra.Command {
	c := group(r, "rules", "Organize rules stored on the server")
	c.AddCommand(r.leaf("list", "List the configured organize rules", 0, 0, r.none("rules.list")))
	return c
}

func approvalsCmd(r *runner) *cobra.Command {
	c := group(r, "approvals", "Write actions that wait for approval")
	c.AddCommand(r.leaf("status ID", "Show the state of a pending write action", 1, 1, func(c *cobra.Command, args []string) error {
		return r.call(c, "approvals.status", map[string]any{"id": args[0]}, false)
	}))
	return c
}

func mailCmd(r *runner) *cobra.Command {
	c := group(r, "mail", "Read, organize, and send screened mail")
	c.AddCommand(mailListCmd(r), mailGetCmd(r), mailThreadCmd(r), mailSearchCmd(r), mailLinkCmd(r),
		r.leaf("folders", "List mail folders", 0, 0, r.none("mail.folders")),
		r.leaf("categories", "List mail categories", 0, 0, r.none("mail.categories")),
		r.leaf("archive IDS...", "Move messages to the archive", 1, -1, r.ids("mail.archive")),
		r.leaf("trash IDS...", "Move messages to the trash", 1, -1, r.ids("mail.trash")),
		r.leaf("read IDS...", "Mark messages as read", 1, -1, r.ids("mail.read")),
		r.leaf("unread IDS...", "Mark messages as unread", 1, -1, r.ids("mail.unread")),
		mailMoveCmd(r), mailFlagCmd(r), mailCategorizeCmd(r), mailOrganizeCmd(r),
		newMessageCmd(r, "send", "mail.send", "Send a new message at once"),
		bodyCmd(r, "reply ID", "mail.reply", "Send a reply to a message at once"),
		forwardCmd(r, "mail.forward", "Send a forward of a message at once"))
	return c
}

// paging adds --count and --page to a list or search. The parameters carry each only when the command line sets it,
// so the provider's default page size applies without --count.
func paging(c *cobra.Command, count *int, page *string) {
	c.Flags().IntVar(count, "count", 0, "number of messages per page; default: the provider's (Gmail 100, Microsoft 365 10)")
	c.Flags().StringVar(page, "page", "", "next_page of an earlier result; repeat the same options")
}

// paged copies --count and --page into the parameters when the command line set them.
func paged(c *cobra.Command, p map[string]any, count int, page *string) {
	if c.Flags().Changed("count") {
		p["count"] = count
	}
	optional(c, p, "page", page)
}

func mailListCmd(r *runner) *cobra.Command {
	folder, count, page := "inbox", 0, ""
	pretty := false
	c := r.leaf("list", "List one page of screened messages in a folder", 0, 0, func(c *cobra.Command, _ []string) error {
		p := map[string]any{"folder": folder}
		paged(c, p, count, &page)
		return r.call(c, "mail.list", p, pretty)
	})
	negated(c, "pretty", "render the messages as a table instead of JSON", &pretty)
	c.Flags().StringVar(&folder, "folder", folder, "folder name or identifier; the Inbox by default, our choice because the providers differ without a folder")
	paging(c, &count, &page)
	return c
}

func mailGetCmd(r *runner) *cobra.Command {
	pretty := false
	c := r.leaf("get ID", "Show one screened message", 1, 1, func(c *cobra.Command, args []string) error {
		return r.call(c, "mail.get", map[string]any{"id": args[0]}, pretty)
	})
	negated(c, "pretty", "render the message as text instead of JSON", &pretty)
	return c
}

func mailThreadCmd(r *runner) *cobra.Command {
	return r.leaf("thread THREAD_ID", "Show the screened messages of a conversation from every folder, newest first", 1, 1, func(c *cobra.Command, args []string) error {
		return r.call(c, "mail.thread", map[string]any{"id": args[0]}, false)
	})
}

// midnight is the start of day d of month m in year y in loc; d may be out of range and is normalized, so callers add
// or subtract days on the date, never on a time. Where a DST gap skips 00:00, the day starts at the end of the gap,
// its first instant: time.Date may resolve the missing 00:00 to the evening before.
func midnight(y int, m time.Month, d int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC); t.Day() != day.Day() {
		_, t = t.ZoneBounds()
	}
	return t
}

// localDay parses a YYYY-MM-DD day as its start, local midnight in loc.
func localDay(v string, loc *time.Location) (time.Time, bool) {
	t, e := time.Parse("2006-01-02", v)
	return midnight(t.Year(), t.Month(), t.Day(), loc), e == nil
}

func mailSearchCmd(r *runner) *cobra.Command {
	var text, from, to, subject, after, before, folder, page string
	count := 0
	pretty := false
	c := r.leaf("search", "Search screened messages, one page at a time", 0, 0, func(c *cobra.Command, _ []string) error {
		p := map[string]any{}
		paged(c, p, count, &page)
		optional(c, p, "text", &text)
		optional(c, p, "from", &from)
		optional(c, p, "to", &to)
		optional(c, p, "subject", &subject)
		optional(c, p, "folder", &folder)
		// The server takes exact instants: after is the first day's local midnight, before the next local
		// midnight after the last day, so both days are included.
		loc := r.now().Location()
		first, firstOK := localDay(after, loc)
		last, lastOK := localDay(before, loc)
		hasAfter, hasBefore := c.Flags().Changed("after"), c.Flags().Changed("before")
		if hasAfter && !firstOK || hasBefore && !lastOK || hasAfter && hasBefore && last.Before(first) {
			return errUsage
		}
		if hasAfter {
			p["after"] = first.Format(time.RFC3339)
		}
		if hasBefore {
			p["before"] = midnight(last.Year(), last.Month(), last.Day()+1, loc).Format(time.RFC3339)
		}
		return r.call(c, "mail.search", p, pretty)
	})
	negated(c, "pretty", "render the messages as a table instead of JSON", &pretty)
	c.Flags().StringVar(&text, "text", "", "text to match")
	c.Flags().StringVar(&from, "from", "", "sender address to match")
	c.Flags().StringVar(&to, "to", "", "To or Cc address to match")
	c.Flags().StringVar(&subject, "subject", "", "subject text to match")
	c.Flags().StringVar(&after, "after", "", "first day YYYY-MM-DD, included, from local midnight")
	c.Flags().StringVar(&before, "before", "", "last day YYYY-MM-DD, included, to the next local midnight")
	c.Flags().StringVar(&folder, "folder", "", "folder identifier to search")
	paging(c, &count, &page)
	return c
}

func mailLinkCmd(r *runner) *cobra.Command {
	all := false
	c := r.leaf("link ID [L1 L2 ...]", "Reveal selected links of a message", 1, -1, func(c *cobra.Command, args []string) error {
		if all == (len(args) > 1) {
			return errUsage
		}
		p := map[string]any{"id": args[0]}
		if len(args) > 1 {
			p["links"] = args[1:]
		}
		if all {
			p["all"] = true
		}
		return r.call(c, "mail.link", p, false)
	})
	c.Flags().BoolVar(&all, "all", false, "reveal every link of the message")
	return c
}

func mailMoveCmd(r *runner) *cobra.Command {
	return r.leaf("move FOLDER_ID IDS...", "Move messages to a folder", 2, -1, func(c *cobra.Command, args []string) error {
		return r.call(c, "mail.move", map[string]any{"folder": args[0], "ids": args[1:]}, false)
	})
}

func mailFlagCmd(r *runner) *cobra.Command {
	state := ""
	c := r.leaf("flag IDS...", "Set the follow-up flag of messages", 1, -1, func(c *cobra.Command, args []string) error {
		if state != "flagged" && state != "complete" && state != "none" {
			return errUsage
		}
		return r.call(c, "mail.flag", map[string]any{"ids": args, "state": state}, false)
	})
	c.Flags().StringVar(&state, "state", "", "flagged, complete, or none")
	return c
}

func mailCategorizeCmd(r *runner) *cobra.Command {
	var add, remove []string
	c := r.leaf("categorize IDS...", "Add or remove categories of messages", 1, -1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"ids": args}
		if len(add) > 0 {
			p["add"] = add
		}
		if len(remove) > 0 {
			p["remove"] = remove
		}
		return r.call(c, "mail.categorize", p, false)
	})
	c.Flags().StringArrayVar(&add, "add", nil, "category to add; repeatable")
	c.Flags().StringArrayVar(&remove, "remove", nil, "category to remove; repeatable")
	return c
}

func mailOrganizeCmd(r *runner) *cobra.Command {
	since, until, apply := "7d", "", false
	c := r.leaf("organize", "Preview organize rules on recent messages; --apply performs them", 0, 0, func(c *cobra.Command, _ []string) error {
		now := r.now()
		first, ok := organizeDay(since, now)
		if !ok {
			return errUsage
		}
		p := map[string]any{"since": first.Format(time.RFC3339)}
		if c.Flags().Changed("until") {
			last, ok := organizeDay(until, now)
			if !ok {
				return errUsage
			}
			p["until"] = midnight(last.Year(), last.Month(), last.Day()+1, now.Location()).Format(time.RFC3339)
		}
		if apply {
			p["apply"] = true
		}
		return r.call(c, "mail.organize", p, false)
	})
	c.Flags().StringVar(&since, "since", since, "first day, from local midnight, as 7d, 2w, or 1m before today, or YYYY-MM-DD")
	c.Flags().StringVar(&until, "until", until, "last day, included, as 7d, 2w, or 1m before today, or YYYY-MM-DD")
	c.Flags().BoolVar(&apply, "apply", apply, "perform the planned changes")
	return c
}

func calendarCmd(r *runner) *cobra.Command {
	from, to := "today", "+7d"
	list := r.leaf("list", "List calendar events in a range", 0, 0, func(c *cobra.Command, _ []string) error {
		now := r.now()
		start, e := parseDay(from, now, now)
		if e != nil {
			return errUsage
		}
		last, e := parseDay(to, start, now)
		if e != nil || last.Before(start) {
			return errUsage
		}
		end := midnight(last.Year(), last.Month(), last.Day()+1, now.Location())
		return r.call(c, "calendar.list", map[string]any{"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339)}, false)
	})
	list.Flags().StringVar(&from, "from", from, "first day, from local midnight, as today, +Nd, or YYYY-MM-DD")
	list.Flags().StringVar(&to, "to", to, "last day, included, as today, +Nd from --from, or YYYY-MM-DD")
	get := r.leaf("get ID", "Show one calendar event", 1, 1, func(c *cobra.Command, args []string) error {
		return r.call(c, "calendar.get", map[string]any{"id": args[0]}, false)
	})
	page := ""
	search := r.leaf("search TEXT", "Search calendar events with the provider's own search, one page at a time", 1, 1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"text": args[0]}
		optional(c, p, "page", &page)
		return r.call(c, "calendar.search", p, false)
	})
	search.Flags().StringVar(&page, "page", "", "next_page of an earlier result for the same TEXT")
	c := group(r, "calendar", "Read calendar events and M365 or fake availability, and send meeting notifications")
	c.AddCommand(list, get, search, calendarAvailabilityCmd(r), calendarInviteCmd(r), calendarUpdateCmd(r), calendarCancelCmd(r), calendarRespondCmd(r))
	return c
}

func draftCmd(r *runner) *cobra.Command {
	c := group(r, "draft", "Create and update drafts without sending them")
	c.AddCommand(newMessageCmd(r, "new", "draft.new", "Draft a new message"), bodyCmd(r, "reply ID", "draft.reply", "Draft a reply to a message"),
		forwardCmd(r, "draft.forward", "Draft a forward of a message"), bodyCmd(r, "update ID", "draft.update", "Replace the body of a draft"))
	return c
}

// newMessageCmd runs draft.new or mail.send: both need --to, --subject, and --body.
func newMessageCmd(r *runner, use, op, short string) *cobra.Command {
	var to []string
	subject, body := "", ""
	c := r.leaf(use, short, 0, 0, func(c *cobra.Command, _ []string) error {
		if !c.Flags().Changed("body") || !c.Flags().Changed("subject") || len(to) == 0 {
			return errUsage
		}
		return r.call(c, op, map[string]any{"to": to, "subject": subject, "body": body}, false)
	})
	c.Flags().StringArrayVar(&to, "to", nil, "recipient address; repeatable")
	c.Flags().StringVar(&subject, "subject", "", "message subject")
	c.Flags().StringVar(&body, "body", "", "message body")
	return c
}

// bodyCmd runs an operation on one message ID that needs --body.
func bodyCmd(r *runner, use, op, short string) *cobra.Command {
	body := ""
	c := r.leaf(use, short, 1, 1, func(c *cobra.Command, args []string) error {
		if !c.Flags().Changed("body") {
			return errUsage
		}
		return r.call(c, op, map[string]any{"id": args[0], "body": body}, false)
	})
	c.Flags().StringVar(&body, "body", "", "message body")
	return c
}

// forwardCmd runs draft.forward or mail.forward: one message ID and at least one --to.
func forwardCmd(r *runner, op, short string) *cobra.Command {
	var to []string
	c := r.leaf("forward ID", short, 1, 1, func(c *cobra.Command, args []string) error {
		if len(to) == 0 {
			return errUsage
		}
		return r.call(c, op, map[string]any{"id": args[0], "to": to}, false)
	})
	c.Flags().StringArrayVar(&to, "to", nil, "recipient address; repeatable")
	return c
}

var relative = regexp.MustCompile(`^([0-9]+)([dwm])$`)
var plusDays = regexp.MustCompile(`^\+([0-9]{1,3})d$`)

// organizeDay resolves an organize day to its local midnight on the client clock: Nd, Nw, or Nm counts back from
// today, and YYYY-MM-DD is that day. A month back keeps the day of the month, or the month's last day if shorter.
func organizeDay(v string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	y, mo, d := now.Date()
	m := relative.FindStringSubmatch(v)
	if m == nil {
		t, ok := localDay(v, loc)
		return t, ok && t.Year() >= 1
	}
	n, e := strconv.Atoi(m[1])
	if e != nil || n < 1 || n > 3650000 {
		return time.Time{}, false
	}
	var t time.Time
	switch m[2] {
	case "d":
		t = midnight(y, mo, d-n, loc)
	case "w":
		if n > 521428 {
			return t, false
		}
		t = midnight(y, mo, d-7*n, loc)
	case "m":
		if n > 119987 {
			return t, false
		}
		// The calendar arithmetic runs in UTC, which has no gaps; day 0 of the next month is this month's last day.
		month := time.Date(y, mo-time.Month(n), 1, 0, 0, 0, 0, time.UTC)
		last := time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		t = midnight(month.Year(), month.Month(), min(d, last), loc)
	}
	return t, t.Year() >= 1 && t.Year() <= 9999
}
func parseDay(v string, base, now time.Time) (time.Time, error) {
	loc := now.Location()
	if v == "today" {
		return midnight(now.Year(), now.Month(), now.Day(), loc), nil
	}
	if m := plusDays.FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		return midnight(base.Year(), base.Month(), base.Day()+n, loc), nil
	}
	if t, ok := localDay(v, loc); ok {
		return t, nil
	}
	return time.Time{}, errUsage
}
