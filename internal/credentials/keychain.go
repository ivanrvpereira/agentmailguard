package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

// Platform is the native secret-store seam. A missing item returns nil, nil.
// Implementations must not use command arguments, environment variables, or files for secrets.
type Platform interface {
	Read(ctx context.Context, service, account string) ([]byte, error)
	Write(ctx context.Context, service, account string, value []byte) error
}
type keychainBackend struct {
	platform Platform
	accounts []config.Account
}

// New injects the platform seam. Production uses NewMacOS. nil fails closed.
func New(platform Platform, home string, accounts []config.Account) *RecordStore {
	return NewStore(home, &keychainBackend{platform: platform, accounts: append([]config.Account(nil), accounts...)})
}

// service names the per-provider legacy items written by builds named agentmailguard-go.
// Keep the -go prefix: the unprefixed names belong to the retired Python version.
func service(provider string) (string, error) {
	switch provider {
	case "m365":
		return "agentmailguard-go-m365", nil
	case "gmail":
		return "agentmailguard-go-gmail", nil
	default:
		return "", errors.New("unsupported credential provider")
	}
}
func (k *keychainBackend) read(ctx context.Context, service, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k.platform == nil {
		return nil, ErrUnavailable
	}
	value, err := k.platform.Read(ctx, service, account)
	if err != nil {
		// Platform absence is nil, nil; an error must never trigger migration or skip an item.
		return nil, safeError(ctx, ErrUnavailable)
	}
	if value == nil {
		return nil, ErrNotFound
	}
	return value, nil
}
func (k *keychainBackend) Read(ctx context.Context) (Record, error) {
	return k.readRecord(ctx, true)
}

func (k *keychainBackend) readRecord(ctx context.Context, migrate bool) (Record, error) {
	value, err := k.read(ctx, "agentmailguard", "credentials")
	if errors.Is(err, ErrNotFound) {
		r, err := k.legacyRecord(ctx)
		if err == nil && migrate {
			err = k.Write(ctx, r)
		}
		return r, err
	}
	if err != nil {
		return Record{}, err
	}
	return decodeRecord(value)
}

// legacyRecord runs under the store lock and never changes legacy items.
func (k *keychainBackend) legacyRecord(ctx context.Context) (Record, error) {
	r := emptyRecord()
	for _, a := range k.accounts {
		// Fake accounts have no credentials.
		if a.Provider == "fake" {
			continue
		}
		namespace, err := service(a.Provider)
		if err != nil {
			return Record{}, ErrInvalidRecord
		}
		if _, err := entry(a.Provider, a.Name); err != nil {
			return Record{}, ErrInvalidRecord
		}
		value, err := k.read(ctx, namespace, a.Name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Record{}, err
		}
		token, err := decodeOAuth(value)
		if err != nil {
			return Record{}, err
		}
		r.OAuth[a.Provider+"/"+a.Name] = token
	}
	value, err := k.read(ctx, "agentmailguard-go-openrouter", "agentmailguard-go")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	r.ScreeningKey = strings.TrimSpace(string(value))
	if err == nil && r.ScreeningKey == "" {
		return Record{}, ErrInvalidRecord
	}
	if len(r.OAuth) == 0 && r.ScreeningKey == "" {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (k *keychainBackend) Write(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k.platform == nil {
		return ErrUnavailable
	}
	if err := validateRecord(r); err != nil {
		return err
	}
	value, err := json.Marshal(r)
	if err != nil {
		return ErrInvalidRecord
	}
	if err := k.platform.Write(ctx, "agentmailguard", "credentials", value); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
