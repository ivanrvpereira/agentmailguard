package credentials

import (
	"context"
	"errors"
	"path/filepath"
)

// ChangePassword rereads under the home lock and uses the same atomic file writer.
// The old store keeps its old password, so it cannot overwrite the changed file.
func (s *RecordStore) ChangePassword(ctx context.Context, password string) error {
	b, ok := s.backend.(*fileBackend)
	if !ok {
		return errors.New("password changes require the file backend")
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	_, err := s.withLock(ctx, func() (Record, error) {
		r, err := s.readExistingLocked(ctx)
		if err != nil {
			return Record{}, err
		}
		replacement := *b
		replacement.password = password
		if err := replacement.Write(ctx, r); err != nil {
			return Record{}, safeError(ctx, err)
		}
		return r, nil
	})
	return err
}

var ErrTargetExists = errors.New("target credentials already exist")
var ErrInvalidTarget = errors.New("credential migration requires the other backend in the same home")

// Migrate copies a complete existing record. The caller changes [credentials] backend in config.toml.
// Both stores share one lock; calling their Store methods here would deadlock.
func (s *RecordStore) Migrate(ctx context.Context, target *RecordStore) error {
	if target == nil || filepath.Clean(s.home) != filepath.Clean(target.home) {
		return ErrInvalidTarget
	}
	_, sourceFile := s.backend.(*fileBackend)
	_, sourceKeychain := s.backend.(*keychainBackend)
	_, targetFile := target.backend.(*fileBackend)
	_, targetKeychain := target.backend.(*keychainBackend)
	if !(sourceFile && targetKeychain || sourceKeychain && targetFile) {
		return ErrInvalidTarget
	}
	_, err := s.withLock(ctx, func() (Record, error) {
		r, err := s.readExistingLocked(ctx)
		if err != nil {
			return Record{}, err
		}
		if _, err = target.readExistingLocked(ctx); err == nil {
			return Record{}, ErrTargetExists
		}
		if !errors.Is(err, ErrNotFound) {
			return Record{}, err
		}
		if err := target.backend.Write(ctx, r); err != nil {
			return Record{}, safeError(ctx, err)
		}
		return r, nil
	})
	return err
}
