package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// Password acquisition fails before a store exists, but still needs a startup store check.
type credentialPasswordError struct{ error }

// All local commands supply the full configured account list before the first store operation.
func localCredentials(ctx context.Context, home string, injected credentials.Store, stdin io.Reader, stderr io.Writer, lookup func(string) string) (credentials.Store, error) {
	password, present := credentials.TakePasswordEnv(lookup)
	if injected != nil {
		return injected, nil
	}
	home, err := config.Home(home)
	if err != nil {
		return nil, errors.New("configuration unavailable")
	}
	accounts, err := config.LoadAccounts(ctx, filepath.Join(home, "config.toml"))
	if err != nil {
		return nil, errors.New("configuration unavailable")
	}
	backend, err := config.CredentialsBackend(ctx, filepath.Join(home, "config.toml"))
	if err != nil {
		return nil, errors.New("configuration unavailable")
	}
	if backend == "file" {
		if !present {
			if stdin == nil {
				stdin = os.Stdin
			}
			_, err := os.Stat(filepath.Join(home, "credentials.age"))
			password, err = readCredentialPassword(ctx, stdin, stderr, errors.Is(err, os.ErrNotExist))
			if err != nil {
				return nil, credentialPasswordError{err}
			}
		}
		if err := credentials.ValidatePassword(password); err != nil {
			return nil, credentialPasswordError{err}
		}
		return credentials.NewFile(home, password), nil
	}
	store, err := credentials.NewMacOS(home, accounts)
	if err != nil {
		return nil, errors.New("credential store unavailable")
	}
	return store, nil
}
