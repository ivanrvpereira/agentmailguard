package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
)

func configCmd(r *runner) *cobra.Command {
	path := r.leaf("path", "Print the path of config.toml", 0, 0, func(*cobra.Command, []string) int {
		_, path, err := r.paths()
		if err != nil {
			return r.fail("configuration unavailable")
		}
		return term.Output(r.stdout, r.stderr, path+"\n", 0)
	})
	list := r.leaf("list", "Show every setting with its value; defaults are marked, secrets show only set or not set", 0, 0, func(c *cobra.Command, _ []string) int {
		return r.configList(c.Context(), "")
	})
	get := r.leaf("get KEY", "Show one setting", 1, 1, func(c *cobra.Command, args []string) int {
		return r.configList(c.Context(), args[0])
	})
	set := r.leaf("set KEY [VALUE]", "Change a setting and reload the daemon; secrets read the value from stdin", 1, 2, func(c *cobra.Command, args []string) int {
		return r.configSet(c.Context(), args)
	})
	unset := r.leaf("unset KEY", "Remove a setting so that its default applies, and reload the daemon", 1, 1, func(c *cobra.Command, args []string) int {
		return r.configUnset(c.Context(), args[0])
	})
	check := r.leaf("check", "Validate config.toml and rules.toml with the code of the daemon", 0, 0, func(c *cobra.Command, _ []string) int {
		return r.configCheck(c.Context())
	})
	return group(r, "config", "Read and change settings and secrets", path, list, get, set, unset, check)
}

// secrets reads whether the screening key and the console password are set. Locked stores give "unknown".
func (r *runner) secrets(ctx context.Context, home, backend string) map[string]string {
	out := map[string]string{config.ScreeningAPIKey: "unknown", config.ConsolePassword: "unknown"}
	store, err := r.store(ctx, home, backend)
	if err != nil {
		return out
	}
	record, err := store.Read(ctx)
	if err != nil {
		return out
	}
	state := func(v string) string {
		if v == "" {
			return "not set"
		}
		return "set"
	}
	out[config.ScreeningAPIKey], out[config.ConsolePassword] = state(record.ScreeningKey), state(record.ConsoleHash)
	return out
}

// configList prints every setting, or only key.
func (r *runner) configList(ctx context.Context, key string) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	if key != "" && key != config.ScreeningAPIKey && key != config.ConsolePassword {
		if _, err := config.ParseKey(c, key); err != nil && !errors.Is(err, config.ErrAccountCommandsOnly) {
			return r.fail(err.Error())
		}
	}
	settings, err := config.List(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	var b strings.Builder
	line := func(k, v string, isDefault bool) {
		if key != "" && k != key {
			return
		}
		if v == "" {
			v = `""`
		}
		if isDefault {
			v += " (default)"
		}
		if key != "" {
			fmt.Fprintln(&b, term.Safe(v))
			return
		}
		fmt.Fprintf(&b, "%s = %s\n", k, term.Safe(v))
	}
	var secrets map[string]string
	if key == "" || key == config.ScreeningAPIKey || key == config.ConsolePassword {
		secrets = r.secrets(ctx, home, c.Backend)
	}
	// An empty endpoint or model means the engine's built-in one, which the list shows.
	builtIn := map[string]string{"screening.endpoint": screening.Endpoint, "screening.model": screening.Model}
	for _, s := range settings {
		if s.Value == "" && builtIn[s.Key] != "" {
			s.Value = builtIn[s.Key]
		}
		line(s.Key, s.Value, s.Default)
		if s.Key == "screening.model" {
			line(config.ScreeningAPIKey, secrets[config.ScreeningAPIKey], false)
		}
		if s.Key == "console.username" {
			line(config.ConsolePassword, secrets[config.ConsolePassword], false)
		}
	}
	if b.Len() == 0 {
		return r.fail("unknown key " + quoted(key))
	}
	return term.Output(r.stdout, r.stderr, b.String(), 0)
}

// configSet changes one key. A secret goes to the credential store from stdin; any other key goes to
// config.toml, validated with the code of the daemon. Restart keys are written without a reload.
func (r *runner) configSet(ctx context.Context, args []string) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	key := args[0]
	if key == config.ScreeningAPIKey || key == config.ConsolePassword {
		if len(args) != 1 {
			return r.fail(key + " is a secret; give the value on stdin, never as an argument")
		}
		return r.setSecret(ctx, home, c.Backend, c.Console.Username, key)
	}
	k, err := config.ParseKey(c, key)
	if err != nil {
		return r.fail(err.Error())
	}
	if len(args) != 2 {
		return 2
	}
	after, err := config.Edit(ctx, path, k.Set(args[1]))
	if err != nil {
		return r.fail(err.Error())
	}
	var value *string
	if k.Choice {
		value = &args[1]
	}
	return r.applied(home, key, r.audit(home).Config(ctx, "config_changed", key, accountKey(after, k), value))
}

func (r *runner) configUnset(ctx context.Context, key string) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	if key == config.ScreeningAPIKey || key == config.ConsolePassword {
		return r.fail(key + " is a secret; set a new value with amgd config set " + key)
	}
	k, err := config.ParseKey(c, key)
	if err != nil {
		return r.fail(err.Error())
	}
	if _, err := config.Edit(ctx, path, k.Unset()); err != nil {
		return r.fail(err.Error())
	}
	return r.applied(home, key, r.audit(home).Config(ctx, "config_removed", key, accountKey(c, k), nil))
}

// accountKey is the account email of an accounts.ALIAS key, else empty.
func accountKey(c config.Config, k config.Key) string {
	if a, ok := c.Account(k.Alias); ok && k.Alias != "" {
		return a.Email
	}
	return ""
}

// applied reloads the daemon after a change of key that config.toml already holds, or reminds that a restart key
// waits for a restart. A failed audit record fails the command, but the daemon still loads the change.
func (r *runner) applied(home, key string, audited error) int {
	code := 0
	if audited != nil {
		code = r.fail("audit record failed")
	}
	if config.RestartKey(key) {
		fmt.Fprintf(r.stderr, "restart needed: %s changed\n", key)
		return code
	}
	if reload := r.changed(home); code == 0 {
		code = reload
	}
	return code
}

// setSecret reads a secret from stdin, stores it, and reloads the daemon. The console password replaces the
// current one without asking for it, so that a forgotten password can be reset on the daemon host. user is the console
// username, which the message names so that the operator knows the whole sign-in.
func (r *runner) setSecret(ctx context.Context, home, backend, user, key string) int {
	store, err := r.store(ctx, home, backend)
	if err != nil {
		return r.fail(err.Error())
	}
	log := r.audit(home)
	if key == config.ScreeningAPIKey {
		value, problem := readScreeningKey(ctx, r.stdin, r.stderr, "Screening API key: ")
		if problem != "" {
			return r.fail(problem)
		}
		if store.SaveScreeningKey(ctx, value) != nil {
			return r.fail("credential save failed")
		}
		return r.stored(home, "Screening key saved.", log.Credential(ctx, "screening_api_key_set"))
	}
	password, problem := readConsolePassword(ctx, r.stdin, r.stderr)
	if problem != "" {
		return r.fail(problem)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return r.fail("console password hashing failed")
	}
	if store.SaveConsoleHash(ctx, string(hash)) != nil {
		return r.fail("credential save failed")
	}
	return r.stored(home, "Console password saved. Sign in to the console as "+term.Safe(user)+" (console.username).", log.Credential(ctx, "console_password_set"))
}

// stored finishes a change that the credential store already holds: it reports the change or the failed audit
// record, and reloads the daemon either way, so the daemon never keeps a secret that the store replaced.
func (r *runner) stored(home, saved string, audited error) int {
	code := 0
	if audited != nil {
		code = r.fail("audit record failed")
	} else {
		fmt.Fprintln(r.stderr, saved)
	}
	if reload := r.changed(home); code == 0 {
		code = reload
	}
	return code
}

// configCheck validates both files with the daemon's code. Rules for an email that no account has are a warning:
// the daemon ignores them.
func (r *runner) configCheck(ctx context.Context) int {
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	_, unknown, err := rules.Load(ctx, filepath.Join(home, "rules.toml"), c.Emails())
	if err != nil {
		return r.fail(err.Error())
	}
	for _, email := range unknown {
		fmt.Fprintf(r.stderr, "%s rules.toml has rules for %s, which is no account; the daemon ignores them\n", term.Warning.Render("warning:"), quoted(email))
	}
	return term.Output(r.stdout, r.stderr, "config.toml and rules.toml are valid\n", 0)
}

// errLocked refuses a store change while a daemon uses the store.
var errLocked = errors.New("a daemon runs for this home; stop it first")

// idle refuses when a daemon runs for home.
func idle(home string) error {
	_, err := daemonPID(home)
	if errors.Is(err, errNoDaemon) {
		return nil
	}
	if err != nil {
		return err
	}
	return errLocked
}
