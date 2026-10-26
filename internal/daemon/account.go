package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
)

func accountCmd(r *runner) *cobra.Command {
	return group(r, "account", "Add, sign in, and remove accounts", accountAddCmd(r), accountLoginCmd(r), accountRemoveCmd(r))
}

// signInFlags are the headless sign-in options of add and login.
type signInFlags struct{ device, noBrowser bool }

func (f *signInFlags) register(c *cobra.Command) {
	c.Flags().BoolVar(&f.device, "device-code", false, "Microsoft 365: sign in with a code on another device")
	c.Flags().BoolVar(&f.noBrowser, "no-browser", false, "Gmail: print the sign-in URL and read the redirected address from stdin")
}

func accountAddCmd(r *runner) *cobra.Command {
	var provider, clientID, tenantID, clientSecret string
	var flags signInFlags
	c := r.leaf("add ALIAS", "Add an account: sign in, then write its table with the email of the sign-in", 1, 1, func(c *cobra.Command, args []string) int {
		alias := args[0]
		a := config.Account{Alias: alias, Provider: provider, ClientID: strings.TrimSpace(clientID), TenantID: strings.TrimSpace(tenantID), ClientSecret: strings.TrimSpace(clientSecret)}
		set := func(name string) bool { return c.Flags().Changed(name) }
		switch {
		case !config.ValidAlias(alias):
			return r.fail("an alias uses only letters, digits, _ and -, at most 64")
		case provider == "fake" && (set("client-id") || set("tenant-id") || set("client-secret") || flags.device || flags.noBrowser),
			provider == "m365" && (a.ClientID == "" || a.TenantID == "" || set("client-secret") || flags.noBrowser),
			provider == "gmail" && (a.ClientID == "" || a.ClientSecret == "" || set("tenant-id") || flags.device),
			provider != "fake" && provider != "m365" && provider != "gmail":
			return 2
		}
		return r.addAccount(c.Context(), a, flags)
	})
	c.Flags().StringVar(&provider, "provider", "", "m365, gmail, or fake")
	c.Flags().StringVar(&clientID, "client-id", "", "OAuth client ID (m365 and gmail)")
	c.Flags().StringVar(&tenantID, "tenant-id", "", "Microsoft Entra tenant ID (m365)")
	c.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client secret of the desktop app (gmail); it is not confidential and stays in config.toml")
	flags.register(c)
	return c
}

func (r *runner) addAccount(ctx context.Context, a config.Account, flags signInFlags) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	if _, exists := c.Account(a.Alias); exists {
		return r.fail(fmt.Sprintf("account %s already exists", a.Alias))
	}
	var login *credentials.OAuth
	var store *credentials.RecordStore
	if a.Provider == "fake" {
		a.Email = strings.ToLower(a.Alias) + "@fake.invalid"
	} else {
		if store, err = r.store(ctx, home, c.Backend); err != nil {
			return r.fail(err.Error())
		}
		token, code := r.signIn(ctx, a, flags)
		if token == nil {
			return code
		}
		login, a.Email = token, strings.ToLower(token.Username)
		if !config.ValidEmail(a.Email) {
			return r.fail("the sign-in returned no usable email; nothing was saved")
		}
	}
	if other, taken := c.ByEmail(a.Email); taken {
		return r.fail(fmt.Sprintf("%s is already account %s", quoted(a.Email), other.Alias))
	}
	_, err = config.Edit(ctx, path, func(d map[string]any) error {
		accounts, _ := d["accounts"].(map[string]any)
		if accounts == nil {
			accounts = map[string]any{}
			d["accounts"] = accounts
		}
		if _, exists := accounts[a.Alias]; exists {
			return fmt.Errorf("account %s already exists", a.Alias)
		}
		table := map[string]any{"provider": a.Provider, "email": a.Email}
		switch a.Provider {
		case "m365":
			table["client_id"], table["tenant_id"] = a.ClientID, a.TenantID
		case "gmail":
			table["client_id"], table["client_secret"] = a.ClientID, a.ClientSecret
		}
		accounts[a.Alias] = table
		if _, ok := d["default_account"]; !ok {
			d["default_account"] = a.Alias
		}
		return nil
	})
	if err != nil {
		return r.fail(err.Error())
	}
	// The login is saved only now, so a failed edit leaves no login for an email that no account has. From here
	// config.toml has the account, and the daemon must load it even when a later step fails.
	code := 0
	if login != nil && store.SaveOAuth(ctx, a.Provider, a.Email, *login) != nil {
		code = r.fail(fmt.Sprintf("account %s is added, but its login could not be saved; run amgd account login %s", a.Alias, a.Alias))
	}
	if err := r.audit(home).Account(ctx, "account_added", a.Email); err != nil {
		code = r.fail("audit record failed")
	}
	if code == 0 {
		fmt.Fprintf(r.stderr, "%s Added account %s: %s (%s)\n", term.Success.Render("\u2713"), a.Alias, quoted(a.Email), a.Provider)
		if loaded, _, err := rules.Load(ctx, filepath.Join(home, "rules.toml"), []string{a.Email}); err == nil && len(loaded[a.Email].Rules) > 0 {
			fmt.Fprintf(r.stderr, "rules.toml already has %d rules for %s; they apply to %s.\n", len(loaded[a.Email].Rules), quoted(a.Email), a.Alias)
		}
		code = r.result(a, login)
	}
	if reload := r.changed(home); code == 0 {
		code = reload
	}
	return code
}

func accountLoginCmd(r *runner) *cobra.Command {
	var flags signInFlags
	c := r.leaf("login ALIAS", "Sign in again to the email of an account", 1, 1, func(c *cobra.Command, args []string) int {
		ctx := c.Context()
		home, path, err := r.paths()
		if err != nil {
			return r.fail("configuration unavailable")
		}
		cfg, err := config.Load(ctx, path)
		if err != nil {
			return r.fail(err.Error())
		}
		a, ok := cfg.Account(args[0])
		switch {
		case !ok:
			return r.fail("unknown account " + quoted(args[0]) + "; run amgd account add")
		case a.Provider == "fake":
			return r.fail("fake accounts need no login")
		case flags.noBrowser && a.Provider != "gmail":
			return r.fail("--no-browser is for Gmail; use --device-code for Microsoft 365")
		case flags.device && a.Provider != "m365":
			return r.fail("--device-code is for Microsoft 365; use --no-browser for Gmail")
		}
		store, err := r.store(ctx, home, cfg.Backend)
		if err != nil {
			return r.fail(err.Error())
		}
		token, code := r.signIn(ctx, a, flags)
		if token == nil {
			return code
		}
		if got := strings.ToLower(token.Username); got != a.Email {
			return r.fail(fmt.Sprintf("You signed in as %s, but %s is %s. Nothing was saved. To add %s, run amgd account add.", quoted(got), a.Alias, a.Email, quoted(got)))
		}
		if err := store.SaveOAuth(ctx, a.Provider, a.Email, *token); err != nil {
			return r.fail("credential save failed")
		}
		// The store holds the new login, so the daemon must load it even when a later step fails.
		if err := r.audit(home).Account(ctx, "account_login", a.Email); err != nil {
			code = r.fail("audit record failed")
		} else {
			fmt.Fprintf(r.stderr, "%s Signed in as %s\n", term.Success.Render("\u2713"), quoted(a.Email))
			code = r.result(a, token)
		}
		if reload := r.changed(home); code == 0 {
			code = reload
		}
		return code
	})
	flags.register(c)
	return c
}

// result prints the account, email, and scopes as JSON on stdout, and warns about extra grants.
func (r *runner) result(a config.Account, token *credentials.OAuth) int {
	out := struct {
		Account  string   `json:"account"`
		Provider string   `json:"provider"`
		Email    string   `json:"email"`
		Scopes   []string `json:"scopes"`
	}{a.Alias, a.Provider, a.Email, []string{}}
	if token != nil {
		out.Scopes = append(out.Scopes, token.Scopes...)
		extra := m365.ExtraScopes(token.Scopes)
		if a.Provider == "gmail" {
			extra = gmail.ExtraScopes(token.Scopes)
		}
		if len(extra) != 0 {
			term.WarnExtraScopes(r.stderr, extra)
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return r.fail("output failed")
	}
	return term.Output(r.stdout, r.stderr, string(b)+"\n", 0)
}

// staging is the store that a sign-in writes to. It keeps the login in memory, so amgd saves it only after the
// email check.
type staging struct {
	login atomic.Pointer[credentials.OAuth]
}

func (*staging) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	return credentials.OAuth{}, credentials.ErrNotFound
}
func (s *staging) SaveOAuth(_ context.Context, _, _ string, v credentials.OAuth) error {
	s.login.Store(&v)
	return nil
}
func (*staging) LoadScreeningKey(context.Context) (string, error) { return "", credentials.ErrNotFound }
func (*staging) SaveScreeningKey(context.Context, string) error   { return credentials.ErrUnavailable }

type loginProvider interface {
	Login(context.Context, bool, func(string, string)) (string, []string, error)
}

// signIn runs the provider sign-in into a staging store and returns the login, or nil and the exit code. The
// login carries the email of the sign-in in Username.
func (r *runner) signIn(ctx context.Context, a config.Account, flags signInFlags) (*credentials.OAuth, int) {
	title := "Microsoft 365"
	if a.Provider == "gmail" {
		title = "Gmail"
	}
	fmt.Fprintf(r.stderr, "\n%s for account %s\n\n", term.Label.Render(title+" sign-in"), a.Alias)
	stage := &staging{}
	// waiting is set while the cursor sits after the paste prompt, so later output starts on a new line.
	var waiting atomic.Bool
	var p loginProvider
	if a.Provider == "gmail" {
		opts := r.opts.Server.Gmail
		if flags.noBrowser {
			opts.OpenBrowser = func(context.Context, string) error { return nil }
		}
		_, interactive := terminal(r.stdin)
		opts.ReadRedirect = func(ctx context.Context, retry bool) (string, error) {
			switch {
			case retry:
				fmt.Fprintf(r.stderr, "%s That address is not from this sign-in. Paste the full address again.\n", term.Warning.Render("!"))
			case flags.noBrowser:
				fmt.Fprint(r.stderr, "2. The browser then fails to load a 127.0.0.1 page. That is expected.\n3. Copy the full address from the address bar and paste it here.\n\n")
			default:
				fmt.Fprint(r.stderr, "2. If the browser then fails to load a 127.0.0.1 page, copy the full address from the address bar and paste it here.\n\n")
			}
			fmt.Fprint(r.stderr, term.Label.Render("Address:")+" ")
			waiting.Store(true)
			// passwordLine reads without buffering; its errors only stop pasted input, and the program never prints the line.
			line, err := passwordLine(ctx, r.stdin)
			if err == nil && interactive {
				waiting.Store(false) // Enter moved the cursor to a new line.
			}
			if err == nil && line == "" && !interactive {
				return "", io.EOF // passwordLine reports EOF as an empty line; closed input would otherwise repeat forever.
			}
			return line, err
		}
		p = gmail.New(a, stage, opts)
	} else {
		if !flags.device {
			fmt.Fprintln(r.stderr, "Complete the sign-in in the browser window that opens.")
		}
		p = m365.New(a, stage, r.opts.Server.M365)
	}
	_, _, err := p.Login(ctx, flags.device, func(uri, code string) {
		step := "1. Open this URL in a browser on any computer and approve access:"
		switch {
		case code != "":
			step = "1. Open this URL in a browser on any computer:"
		case !flags.noBrowser:
			step = "1. Approve access in the browser window that opens. If none opens, open this URL:"
		}
		// Bare and on its own line, so terminals can link it and a copy takes nothing extra.
		fmt.Fprintf(r.stderr, "%s\n\n%s\n\n", step, term.Safe(uri))
		if code != "" {
			fmt.Fprintf(r.stderr, "2. Enter this code: %s\n\nWaiting for sign-in...\n", term.Label.Render(term.Safe(code)))
		}
	})
	if waiting.Load() {
		fmt.Fprintln(r.stderr)
	}
	if err != nil {
		if diagnostic, ok := errors.AsType[m365.LoginError](err); ok {
			return nil, r.fail(diagnostic.Message())
		}
		return nil, r.fail("provider login failed")
	}
	token := stage.login.Load()
	if token == nil || token.Username == "" {
		return nil, r.fail("provider login failed")
	}
	return token, 0
}

func accountRemoveCmd(r *runner) *cobra.Command {
	yes := false
	c := r.leaf("remove ALIAS", "Remove an account, its login, and its cached verdicts, and deny its pending actions", 1, 1, func(c *cobra.Command, args []string) int {
		return r.removeAccount(c.Context(), args[0], yes)
	})
	c.Flags().BoolVar(&yes, "yes", false, "remove without the confirmation question; needed when stdin is not a terminal")
	return c
}

// revokeAt tells where a user removes the grant of the app at the provider. amgd never calls the provider for it.
var revokeAt = map[string]string{
	"m365":  "To revoke the access at Microsoft, remove the app at https://myapps.microsoft.com or ask your administrator.",
	"gmail": "To revoke the access at Google, remove the app at https://myaccount.google.com/permissions.",
}

func (r *runner) removeAccount(ctx context.Context, alias string, yes bool) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	a, ok := c.Account(alias)
	if !ok {
		return r.fail("unknown account " + quoted(alias))
	}
	if a.Alias == c.DefaultAccount && len(c.Accounts) > 1 {
		return r.fail(fmt.Sprintf("%s is the default account; first run amgd config set default_account with another alias", a.Alias))
	}
	if !yes {
		if _, interactive := terminal(r.stdin); !interactive {
			return r.fail("confirmation needs a terminal; run again with --yes")
		}
		fmt.Fprintf(r.stderr, "This removes account %s (%s): its table and policy in config.toml, its login, and its cached verdicts. Its pending actions are denied. Audit records and its rules in rules.toml stay.\nType %s to remove it: ", a.Alias, a.Email, a.Alias)
		answer, err := passwordLine(ctx, r.stdin)
		if err != nil || strings.TrimSpace(answer) != a.Alias {
			return r.fail("not confirmed; nothing was removed")
		}
	}
	var store *credentials.RecordStore
	if a.Provider != "fake" {
		if store, err = r.store(ctx, home, c.Backend); err != nil {
			return r.fail(err.Error())
		}
	}
	// Every step that can fail runs before config.toml changes, so a failed removal keeps the account and can run
	// again.
	const again = "; the account stays in config.toml, run amgd account remove again"
	db, err := cache.Open(ctx, filepath.Join(home, "cache.db"))
	if err != nil {
		return r.fail("cache unavailable" + again)
	}
	defer db.Close()
	log := r.audit(home)
	denied, err := r.denyPending(ctx, db, log, a.Email)
	if err != nil {
		return r.fail(err.Error() + again)
	}
	if _, err := db.DeleteAccount(ctx, a.Email); err != nil {
		return r.fail("cached verdicts could not be deleted" + again)
	}
	if store != nil && store.DeleteOAuth(ctx, a.Provider, a.Email) != nil {
		return r.fail("its login could not be deleted" + again)
	}
	_, err = config.Edit(ctx, path, func(d map[string]any) error {
		accounts, _ := d["accounts"].(map[string]any)
		delete(accounts, a.Alias)
		if len(accounts) == 0 {
			delete(d, "accounts")
			delete(d, "default_account")
		}
		return nil
	})
	if err != nil {
		return r.fail(err.Error() + again)
	}
	// config.toml no longer has the account, so the daemon must load it even when a later step fails. The second
	// denial catches actions that the daemon created for the account since the first.
	late, err := r.denyPending(ctx, db, log, a.Email)
	code := 0
	switch {
	case err != nil:
		code = r.fail(err.Error())
	case log.Account(ctx, "account_removed", a.Email) != nil:
		code = r.fail("audit record failed")
	default:
		fmt.Fprintf(r.stderr, "%s Removed account %s (%s). %d pending actions denied. Its rules in rules.toml stay.\n", term.Success.Render("\u2713"), a.Alias, a.Email, denied+late)
		if hint := revokeAt[a.Provider]; hint != "" {
			fmt.Fprintln(r.stderr, hint)
		}
	}
	if reload := r.changed(home); code == 0 {
		code = reload
	}
	return code
}

// denyPending denies the pending actions of an account, with an audit record each, and returns how many it denied.
func (r *runner) denyPending(ctx context.Context, db *cache.Cache, log *audit.Log, email string) (int, error) {
	denied, err := db.DenyAccount(ctx, email, r.now())
	if err != nil {
		return 0, errors.New("pending actions could not be denied")
	}
	for _, p := range denied {
		if log.Approval(ctx, p.Account, p.Op, "denied", p.ID) != nil {
			return len(denied), errors.New("audit record failed")
		}
	}
	return len(denied), nil
}
