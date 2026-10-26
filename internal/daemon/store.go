package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
)

func storeCmd(r *runner) *cobra.Command {
	target := ""
	migrate := r.leaf("migrate", "Copy the credential store to the other backend and switch [credentials] backend", 0, 0, func(c *cobra.Command, _ []string) int {
		if target != "file" && target != "keychain" {
			return 2
		}
		return r.migrate(c.Context(), target)
	})
	migrate.Flags().StringVar(&target, "to", "", "target backend: file or keychain")
	change := r.leaf("change-password", "Change the password of the credential file", 0, 0, func(c *cobra.Command, _ []string) int {
		return r.changePassword(c.Context())
	})
	return group(r, "store", "Credential store maintenance; the daemon must be stopped", migrate, change)
}

func (r *runner) migrate(ctx context.Context, target string) int {
	// Capture once: a Keychain source must not discard the file target's password.
	password, present := credentials.TakePasswordEnv(r.passwordEnv)
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	if err := idle(home); err != nil {
		return r.fail(err.Error())
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	if target == c.Backend {
		return r.fail("migration requires the other credential backend")
	}
	if !present {
		password, err = readCredentialPassword(ctx, r.stdin, r.stderr, target == "file")
		if err != nil {
			return r.fail(err.Error())
		}
	}
	if err := credentials.ValidatePassword(password); err != nil {
		return r.fail(err.Error())
	}
	file := credentials.NewFile(home, password)
	keychain := credentials.New(r.opts.Platform, home)
	if r.opts.Platform == nil {
		if keychain, err = credentials.NewMacOS(home); err != nil {
			return r.fail("credential store unavailable")
		}
	}
	source, destination := keychain, file
	if c.Backend == "file" {
		source, destination = file, keychain
	}
	if err := source.Migrate(ctx, destination); err != nil {
		return r.fail(err.Error())
	}
	k, err := config.ParseKey(c, "credentials.backend")
	if err != nil {
		return r.fail(err.Error())
	}
	if _, err := config.Edit(ctx, path, k.Set(target)); err != nil {
		return r.fail("credentials copied, but config.toml could not be changed: " + err.Error())
	}
	log := r.audit(home)
	if log.Credential(ctx, "store_migrated") != nil || log.Config(ctx, "config_changed", "credentials.backend", "", &target) != nil {
		return r.fail("audit record failed")
	}
	return term.Output(r.stdout, r.stderr, fmt.Sprintf("Credentials copied to the %s backend; config.toml now uses it. The source credentials are unchanged.\nrestart needed: credentials.backend changed\n", target), 0)
}

func (r *runner) changePassword(ctx context.Context) int {
	password, present := credentials.TakePasswordEnv(r.passwordEnv)
	home, path, err := r.paths()
	if err != nil {
		return r.fail("configuration unavailable")
	}
	if err := idle(home); err != nil {
		return r.fail(err.Error())
	}
	c, err := config.Load(ctx, path)
	if err != nil {
		return r.fail(err.Error())
	}
	if c.Backend != "file" {
		return r.fail("password changes require the file backend")
	}
	if _, ok := terminal(r.stdin); !ok {
		return r.fail("new credential password requires a terminal")
	}
	if !present {
		if password, err = readCredentialPassword(ctx, r.stdin, r.stderr, false); err != nil {
			return r.fail(err.Error())
		}
	}
	if err := credentials.ValidatePassword(password); err != nil {
		return r.fail(err.Error())
	}
	fmt.Fprintln(r.stderr, "Enter the new credential password twice.")
	replacement, err := readCredentialPassword(ctx, r.stdin, r.stderr, true)
	if err != nil {
		return r.fail(err.Error())
	}
	if err := credentials.NewFile(home, password).ChangePassword(ctx, replacement); err != nil {
		return r.fail(err.Error())
	}
	if r.audit(home).Credential(ctx, "store_password_changed") != nil {
		return r.fail("audit record failed")
	}
	return term.Output(r.stdout, r.stderr, "Credential password changed. Start the daemon with the new password.\n", 0)
}

func cacheCmd(r *runner) *cobra.Command {
	clearCmd := r.leaf("clear", "Discard cached screening results; pending actions stay", 0, 0, func(c *cobra.Command, _ []string) int {
		home, _, err := r.paths()
		if err != nil {
			return r.fail("configuration unavailable")
		}
		db, err := cache.Open(c.Context(), filepath.Join(home, "cache.db"))
		if err != nil {
			return r.fail("cache unavailable")
		}
		defer db.Close()
		n, err := db.Clear(c.Context())
		if err != nil {
			return r.fail("cache clear failed")
		}
		return term.Output(r.stdout, r.stderr, fmt.Sprintf("{\n  \"deleted\": %d\n}\n", n), 0)
	})
	return group(r, "cache", "Screening cache maintenance", clearCmd)
}

func (r *runner) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}
