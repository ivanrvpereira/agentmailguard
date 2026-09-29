package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
)

func credentialCommand(ctx context.Context, op, home, target string, stdout, stderr io.Writer, opts Options, lookup func(string) string) int {
	// Capture once: a Keychain source must not discard the file target's password.
	password, present := credentials.TakePasswordEnv(lookup)
	home, err := config.Home(home)
	if err != nil {
		return failure(stderr, "configuration unavailable")
	}
	path := filepath.Join(home, "config.toml")
	accounts, err := config.LoadAccounts(ctx, path)
	if err != nil {
		return failure(stderr, "configuration unavailable")
	}
	backend, err := config.CredentialsBackend(ctx, path)
	if err != nil {
		return failure(stderr, "configuration unavailable")
	}
	changing := op == "auth.change-password"
	if changing && backend != "file" {
		return failure(stderr, "password changes require the file backend")
	}
	if !changing && target == backend {
		return failure(stderr, "migration requires the other credential backend")
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	if changing {
		if _, ok := terminal(stdin); !ok {
			return failure(stderr, "new credential password requires a terminal")
		}
	}
	if !present {
		password, err = readCredentialPassword(ctx, stdin, stderr, !changing && target == "file")
		if err != nil {
			return failure(stderr, err.Error())
		}
	}
	if err := credentials.ValidatePassword(password); err != nil {
		return failure(stderr, err.Error())
	}
	file := credentials.NewFile(home, password)
	if changing {
		fmt.Fprintln(stderr, "Enter the new credential password twice.")
		replacement, err := readCredentialPassword(ctx, stdin, stderr, true)
		if err != nil {
			return failure(stderr, err.Error())
		}
		if err := file.ChangePassword(ctx, replacement); err != nil {
			return failure(stderr, err.Error())
		}
		return output(stdout, stderr, "Credential password changed. Restart the server with the new password.\n", 0)
	}
	var keychain *credentials.RecordStore
	if opts.CredentialPlatform != nil {
		keychain = credentials.New(opts.CredentialPlatform, home, accounts)
	} else {
		native, err := credentials.NewMacOS(home, accounts)
		if err != nil {
			return failure(stderr, "credential store unavailable")
		}
		keychain = native.(*credentials.RecordStore)
	}
	source, destination := keychain, file
	if backend == "file" {
		source, destination = file, keychain
	}
	if err := source.Migrate(ctx, destination); err != nil {
		return failure(stderr, err.Error())
	}
	return output(stdout, stderr, fmt.Sprintf("Credentials copied. Source credentials and config.toml are unchanged.\nSet this table in config.toml:\n[credentials]\nbackend = %q\nRestart the server after changing config.toml.\n", target), 0)
}
