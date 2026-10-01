//go:build darwin && cgo

package credentials

import (
	"context"
	"errors"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/keybase/go-keychain"
)

// NewMacOS creates a native adapter without accessing the Keychain until an operation.
func NewMacOS(home string, accounts []config.Account) (Store, error) {
	return New(nativeKeychain{}, home, accounts), nil
}

type nativeKeychain struct{}

// Native Keychain calls are synchronous and cannot be interrupted once started.
// Context cancellation is checked before entering the OS; no background write is spawned.
func (nativeKeychain) Read(ctx context.Context, service, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return keychain.GetGenericPassword(service, account, "", "")
}
func (nativeKeychain) Write(ctx context.Context, service, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	item := keychain.NewGenericPassword(service, account, service, value, "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	err := keychain.AddItem(item)
	if !errors.Is(err, keychain.ErrorDuplicateItem) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Update in place: never delete a working credential before a replacement succeeds.
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(service)
	query.SetAccount(account)
	update := keychain.NewItem()
	update.SetData(value)
	return keychain.UpdateItem(query, update)
}
