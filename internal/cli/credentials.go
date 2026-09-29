package cli

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
)

// All local commands supply the full configured account list before the first store operation.
func localCredentials(ctx context.Context, home string, injected credentials.Store) (credentials.Store, error) {
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
	store, err := credentials.NewMacOS(home, accounts)
	if err != nil {
		return nil, errors.New("credential store unavailable")
	}
	return store, nil
}
