// Package cli is the testable command entrypoint. Data commands only use HTTP.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
	"github.com/ivanrvpereira/agentmailguard-go/internal/views"
)

type Options struct {
	Client    client.Options
	Server    server.Options
	LookupEnv func(string) string
	Now       func() time.Time
	Stdin     io.Reader // defaults to os.Stdin; read by auth set-jev-key and by serve for a missing Jev key
}

func (Options) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "CLIOptions{redacted}") }

const help = `agentmailguard-go [--account NAME] [--server URL] [--timeout 30m] COMMAND

serve [--home PATH] [--listen 127.0.0.1:8787]
auth login [--home PATH] [--device-code | --no-device-code] | auth status | auth set-jev-key
stats [--days 30] | cache clear | rules list
mail list [--folder inbox] [--count 25] [--skip 0]
mail get ID [--pretty | --no-pretty]
mail search [--text TEXT] [--from ADDRESS] [--subject TEXT] [--after YYYY-MM-DD] [--before YYYY-MM-DD] [--folder ID] [--count 25]
mail link ID (L1 L2 ... | --all) | mail folders | mail categories
mail archive IDS... | mail trash IDS... | mail move FOLDER_ID IDS...
mail read IDS... | mail unread IDS... | mail flag IDS... --state flagged|complete|none
mail categorize IDS... [--add NAME ...] [--remove NAME ...]
mail organize [--since 7d] [--until DATE] [--apply]
calendar list [--from today] [--to +7d] | calendar get ID
draft new --to ADDRESS ... --subject TEXT --body TEXT
draft reply ID --body TEXT | draft forward ID --to ADDRESS ... | draft update ID --body TEXT

All data commands use the server. No automatic server start. No send or permanent delete.
serve loads all credentials at start. Accounts without login start disabled; other failures refuse the start.
Login runs locally on the server host. Restart the server after login or account changes.
Client/server authentication and built-in TLS are absent. Protect remote access with a tunnel.
`

func usage(w io.Writer) int                   { fmt.Fprintln(w, "error: invalid command usage; use --help"); return 2 }
func failure(w io.Writer, message string) int { fmt.Fprintln(w, "error: "+message); return 1 }

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
	fmt.Fprintf(w, "warning: token grants extra scopes the server does not use: %s.%s Remove the extra consent to reduce exposure.\n", strings.Join(quoted, ", "), note)
}
func output(stdout, stderr io.Writer, text string, code int) int {
	if _, err := io.WriteString(stdout, text); err != nil {
		return failure(stderr, "output failed")
	}
	return code
}
func flags() *flag.FlagSet {
	f := flag.NewFlagSet("agentmailguard-go", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

type stringsFlag []string

func (s *stringsFlag) String() string     { return "" }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// Reorder complete option/value pairs, never option-looking text inside a value.
func interspersed(f *flag.FlagSet, args []string) error {
	var opts, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		name, _, equal := strings.Cut(name, "=")
		v := f.Lookup(name)
		if v == nil {
			return errors.New("usage")
		}
		opts = append(opts, a)
		b, isBool := v.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(isBool && b.IsBoolFlag()) {
			i++
			if i >= len(args) {
				return errors.New("usage")
			}
			opts = append(opts, args[i])
		}
	}
	return f.Parse(append(append(opts, "--"), pos...))
}
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) (code int) {
	defer func() {
		if recover() != nil {
			code = failure(stderr, "operation failed")
		}
	}()
	if len(args) == 0 {
		return output(stdout, stderr, help, 0)
	}
	// Help is recognized only as an option, not as a draft body or other option value.
	global := flags()
	account, base, home, listen := "", "", "", "127.0.0.1:8787"
	timeout := client.DefaultTimeout
	global.StringVar(&account, "account", "", "")
	global.StringVar(&base, "server", "", "")
	global.DurationVar(&timeout, "timeout", client.DefaultTimeout, "")
	global.StringVar(&home, "home", "", "")
	wantHelp := global.Bool("help", false, "")
	global.BoolVar(wantHelp, "h", false, "")
	if global.Parse(args) != nil {
		return usage(stderr)
	}
	if *wantHelp {
		return output(stdout, stderr, help, 0)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return usage(stderr)
	}
	op := rest[0]
	rest = rest[1:]
	if op != "stats" && op != "serve" {
		if len(rest) == 0 {
			return output(stdout, stderr, help, 2)
		}
		if rest[0] == "--help" || rest[0] == "-h" {
			return output(stdout, stderr, help, 0)
		}
		op += "." + rest[0]
		rest = rest[1:]
	}
	f := flags()
	f.StringVar(&account, "account", account, "")
	f.StringVar(&base, "server", base, "")
	f.DurationVar(&timeout, "timeout", timeout, "")
	f.StringVar(&home, "home", home, "")
	f.BoolVar(wantHelp, "help", false, "")
	f.BoolVar(wantHelp, "h", false, "")
	p := map[string]any{}
	str := map[string]*string{}
	ints := map[string]*int{}
	bools := map[string]*bool{}
	repeated := map[string]*stringsFlag{}
	addString := func(name, value string) { str[name] = f.String(name, value, "") }
	addInt := func(name string, value int) { ints[name] = f.Int(name, value, "") }
	addBool := func(name string) {
		bools[name] = f.Bool(name, false, "")
		if name == "pretty" || name == "device-code" {
			f.BoolFunc("no-"+name, "", func(value string) error {
				v, err := strconv.ParseBool(value)
				if err == nil {
					*bools[name] = !v
				}
				return err
			})
		}
	}
	addRepeated := func(name string) { v := new(stringsFlag); repeated[name] = v; f.Var(v, name, "") }
	switch op {
	case "stats":
		addInt("days", 30)
	case "cache.clear", "rules.list", "auth.status", "auth.set-jev-key", "mail.folders", "mail.categories", "mail.archive", "mail.trash", "mail.read", "mail.unread", "mail.move", "calendar.get":
	case "serve":
		f.StringVar(&listen, "listen", listen, "")
	case "auth.login":
		addBool("device-code")
	case "mail.list":
		addString("folder", "inbox")
		addInt("count", 25)
		addInt("skip", 0)
	case "mail.get":
		addBool("pretty")
	case "mail.search":
		for _, n := range []string{"text", "from", "subject", "after", "before", "folder"} {
			addString(n, "")
		}
		addInt("count", 25)
	case "mail.link":
		addBool("all")
	case "mail.flag":
		addString("state", "")
	case "mail.categorize":
		addRepeated("add")
		addRepeated("remove")
	case "mail.organize":
		addString("since", "7d")
		addString("until", "")
		addBool("apply")
	case "calendar.list":
		addString("from", "today")
		addString("to", "+7d")
	case "draft.new":
		addRepeated("to")
		addString("subject", "")
		addString("body", "")
	case "draft.reply", "draft.update":
		addString("body", "")
	case "draft.forward":
		addRepeated("to")
	default:
		return usage(stderr)
	}
	if interspersed(f, rest) != nil {
		return usage(stderr)
	}
	if *wantHelp {
		return output(stdout, stderr, help, 0)
	}
	seen := map[string]bool{}
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	global.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	if seen["account"] && strings.TrimSpace(account) == "" {
		return failure(stderr, "account must not be blank")
	}
	pos := f.Args()
	for k, v := range str {
		if seen[k] || k == "since" || k == "folder" && op == "mail.list" {
			p[k] = *v
		}
	}
	for k, v := range ints {
		p[k] = *v
	}
	for k, v := range bools {
		if *v {
			p[k] = true
		}
	}
	for k, v := range repeated {
		if len(*v) > 0 {
			p[k] = []string(*v)
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	switch op {
	case "mail.get", "calendar.get":
		if len(pos) != 1 {
			return usage(stderr)
		}
		p["id"] = pos[0]
		delete(p, "pretty")
	case "mail.archive", "mail.trash", "mail.read", "mail.unread", "mail.flag", "mail.categorize":
		if len(pos) < 1 {
			return usage(stderr)
		}
		p["ids"] = pos
		if op == "mail.flag" && (!seen["state"] || (*str["state"] != "flagged" && *str["state"] != "complete" && *str["state"] != "none")) {
			return usage(stderr)
		}
	case "mail.move":
		if len(pos) < 2 {
			return usage(stderr)
		}
		p["folder"] = pos[0]
		p["ids"] = pos[1:]
	case "mail.link":
		if len(pos) < 1 || *bools["all"] == (len(pos) > 1) {
			return usage(stderr)
		}
		p["id"] = pos[0]
		if len(pos) > 1 {
			p["links"] = pos[1:]
		}
	case "draft.reply", "draft.update":
		if len(pos) != 1 || !seen["body"] {
			return usage(stderr)
		}
		p["id"] = pos[0]
	case "draft.forward":
		if len(pos) != 1 || len(*repeated["to"]) == 0 {
			return usage(stderr)
		}
		p["id"] = pos[0]
	default:
		if len(pos) != 0 {
			return usage(stderr)
		}
	}
	if op == "draft.new" && (!seen["body"] || !seen["subject"] || len(*repeated["to"]) == 0) {
		return usage(stderr)
	}
	if op == "stats" && *ints["days"] < 1 {
		return usage(stderr)
	}
	if op == "mail.search" {
		for _, n := range []string{"after", "before"} {
			if seen[n] {
				if _, e := time.Parse("2006-01-02", *str[n]); e != nil {
					return usage(stderr)
				}
			}
		}
	}
	if op == "mail.organize" {
		if !whenSyntax(*str["since"]) || (seen["until"] && !whenSyntax(*str["until"])) {
			return usage(stderr)
		}
	}
	if op == "calendar.list" {
		now := opts.Now()
		start, e := parseDay(*str["from"], now, now)
		if e != nil {
			return usage(stderr)
		}
		end, e := parseDay(*str["to"], start, now)
		if e != nil {
			return usage(stderr)
		}
		p = map[string]any{"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339)}
	}
	if seen["home"] && op != "serve" && op != "auth.login" {
		return usage(stderr)
	}
	if timeout <= 0 {
		return usage(stderr)
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.Getenv
	}
	envServer := opts.LookupEnv("AGENTMAILGUARD_GO_SERVER")
	supplied := seen["server"] || envServer != ""
	if !seen["server"] {
		base = envServer
	}
	if base == "" {
		base = client.DefaultServer
	}
	if op == "serve" {
		if supplied || seen["account"] {
			return usage(stderr)
		}
		if opts.Server.LookupEnv == nil {
			opts.Server.LookupEnv = opts.LookupEnv
		}
		return serve(ctx, home, listen, opts.Stdin, stderr, opts.Server)
	}
	if op == "auth.login" {
		if supplied {
			return failure(stderr, "login is local only; remove --server and AGENTMAILGUARD_GO_SERVER")
		}
		return login(ctx, home, account, *bools["device-code"], stdout, stderr, opts.Server)
	}
	if op == "auth.set-jev-key" {
		if seen["account"] {
			return usage(stderr)
		}
		if supplied {
			return failure(stderr, "Jev key setup is local only; remove --server and AGENTMAILGUARD_GO_SERVER")
		}
		return setJevKey(ctx, opts.Stdin, stderr, opts.Server)
	}
	clientOpts := opts.Client
	clientOpts.Timeout = timeout
	c, e := client.New(base, clientOpts)
	if e != nil {
		return failure(stderr, "invalid server URL")
	}
	b, e := c.Call(ctx, op, account, p)
	if e != nil {
		var remote *client.Error
		if op == "mail.organize" && errors.As(e, &remote) && remote.Status == 400 {
			return usage(stderr)
		}
		return failure(stderr, e.Error())
	}
	if op == "mail.get" && *bools["pretty"] {
		var v views.MailView
		if json.Unmarshal(b, &v) != nil {
			return failure(stderr, "invalid server response")
		}
		if output(stdout, stderr, views.ToText(v)+"\n", 0) != 0 {
			return 1
		}
	} else {
		var pretty bytes.Buffer
		if json.Indent(&pretty, bytes.TrimSpace(b), "", "  ") != nil {
			return failure(stderr, "invalid server response")
		}
		if output(stdout, stderr, pretty.String()+"\n", 0) != 0 {
			return 1
		}
	}
	if op == "auth.status" {
		var v struct {
			OK            bool      `json:"ok"`
			LoggedIn      bool      `json:"logged_in"`
			ExtraScopes   *[]string `json:"extra_scopes"`
			MissingScopes *[]string `json:"missing_scopes"`
		}
		if json.Unmarshal(b, &v) != nil {
			return failure(stderr, "invalid server response")
		}
		if v.OK && v.ExtraScopes != nil {
			warnExtraScopes(stderr, *v.ExtraScopes)
		}
		if !v.OK {
			if v.MissingScopes != nil {
				return failure(stderr, "authentication refused: required scopes missing")
			}
			if v.LoggedIn {
				return failure(stderr, "authentication failed")
			}
			return 1
		}
	}
	return 0
}

var relative = regexp.MustCompile(`^([0-9]+)([dwm])$`)
var plusDays = regexp.MustCompile(`^\+([0-9]{1,3})d$`)

func whenSyntax(v string) bool {
	if m := relative.FindStringSubmatch(v); m != nil {
		n, e := strconv.Atoi(m[1])
		return e == nil && n >= 1 && n <= 3650000
	}
	d, e := time.Parse("2006-01-02", v)
	return e == nil && d.Year() >= 1
}
func parseDay(v string, base, now time.Time) (time.Time, error) {
	loc := now.Location()
	mid := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc) }
	if v == "today" {
		return mid(now), nil
	}
	if m := plusDays.FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		return mid(base).AddDate(0, 0, n), nil
	}
	return time.ParseInLocation("2006-01-02", v, loc)
}
