package credentials

import (
	"context"
	"encoding/json"
	"errors"
)

// Platform is the native secret-store seam. A missing item returns nil, nil.
// Implementations must not use command arguments, environment variables, or files for secrets.
type Platform interface {
	Read(ctx context.Context, service, account string) ([]byte, error)
	Write(ctx context.Context, service, account string, value []byte) error
}
type keychainBackend struct{ platform Platform }

// New injects the platform seam. Production uses NewMacOS. nil fails closed.
func New(platform Platform, home string) *RecordStore {
	return NewStore(home, &keychainBackend{platform: platform})
}

// service checks the provider name of a store entry.
func service(provider string) (string, error) {
	switch provider {
	case "m365", "gmail":
		return provider, nil
	default:
		return "", errors.New("unsupported credential provider")
	}
}
func (k *keychainBackend) Read(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if k.platform == nil {
		return Record{}, ErrUnavailable
	}
	value, err := k.platform.Read(ctx, "agentmailguard", "credentials")
	if err != nil {
		// Platform absence is nil, nil; an error must never count as a missing record.
		return Record{}, safeError(ctx, ErrUnavailable)
	}
	if value == nil {
		return Record{}, ErrNotFound
	}
	return decodeRecord(value)
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
