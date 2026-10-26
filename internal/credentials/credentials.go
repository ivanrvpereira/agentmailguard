// Package credentials stores server-host secrets in Keychain or a password-encrypted file.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var ErrNotFound = errors.New("credential not found")
var ErrUnavailable = errors.New("credential store unavailable")
var ErrInvalidRecord = errors.New("invalid credential record")
var ErrUnsupportedVersion = errors.New("unsupported credential version")

type OAuth struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
	Username     string    `json:"username"`
}

func (OAuth) String() string                   { return "OAuth{redacted}" }
func (o OAuth) GoString() string               { return o.String() }
func (o OAuth) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, o.String()) }

// Store is the credential dependency for provider and screening adapters. It does not obtain tokens, expand scopes, or expose provider secrets to remote clients.
type Store interface {
	LoadOAuth(ctx context.Context, provider, account string) (OAuth, error)
	SaveOAuth(ctx context.Context, provider, account string, credential OAuth) error
	LoadScreeningKey(ctx context.Context) (string, error)
	SaveScreeningKey(ctx context.Context, key string) error
}

// Record holds all secrets. Formatting must never reveal its fields.
type Record struct {
	Version int              `json:"version"`
	OAuth   map[string]OAuth `json:"oauth"`
	// ScreeningKey keeps its first JSON name so that existing records still load.
	ScreeningKey string `json:"openrouter_key"`
	// ConsoleHash is the bcrypt hash of the console password. Empty means not set. The JSON name stays from
	// when it was called the approval password.
	ConsoleHash string `json:"approval_hash,omitempty"`
}

func (Record) String() string                   { return "Record{redacted}" }
func (r Record) GoString() string               { return r.String() }
func (r Record) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, r.String()) }

// Backend reads and writes a whole record. A missing record returns ErrNotFound.
// RecordStore serializes access, including migration, with the home lock.
type Backend interface {
	Read(context.Context) (Record, error)
	Write(context.Context, Record) error
}

type RecordStore struct {
	home    string
	backend Backend
}

func NewStore(home string, backend Backend) *RecordStore {
	return &RecordStore{home: home, backend: backend}
}
func (*RecordStore) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "CredentialStore{redacted}")
}

func emptyRecord() Record { return Record{Version: 1, OAuth: map[string]OAuth{}} }
func (s *RecordStore) readLocked(ctx context.Context) (Record, error) {
	r, err := s.readExistingLocked(ctx)
	if errors.Is(err, ErrNotFound) {
		return emptyRecord(), nil
	}
	return r, err
}
func (s *RecordStore) readExistingLocked(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if s.backend == nil {
		return Record{}, ErrUnavailable
	}
	r, err := s.backend.Read(ctx)
	if err != nil {
		return Record{}, safeError(ctx, err)
	}
	if err := validateRecord(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// transact reads under the cross-process lock. Each update reads again under the same lock; no stale
// snapshot is saved.
func (s *RecordStore) transact(ctx context.Context, change func(*Record)) (Record, error) {
	return s.withLock(ctx, func() (Record, error) {
		r, err := s.readLocked(ctx)
		if err != nil {
			return Record{}, err
		}
		if change != nil {
			change(&r)
			if err := s.backend.Write(ctx, r); err != nil {
				return Record{}, safeError(ctx, err)
			}
		}
		return r, nil
	})
}

func (s *RecordStore) withLock(ctx context.Context, operation func() (Record, error)) (record Record, err error) {
	defer func() {
		if recover() != nil {
			record, err = Record{}, ErrUnavailable
		}
	}()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if s.home == "" || os.MkdirAll(s.home, 0700) != nil {
		return Record{}, ErrUnavailable
	}
	lock, err := os.OpenFile(filepath.Join(s.home, "credentials.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Record{}, ErrUnavailable
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			return Record{}, ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return Record{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

// Preload reads and rewrites the whole record under one lock to obtain read and modify permission.
func (s *RecordStore) Preload(ctx context.Context) (Record, error) {
	return s.transact(ctx, func(*Record) {})
}

// Read returns the whole record without writing it. A missing record is an empty one.
func (s *RecordStore) Read(ctx context.Context) (Record, error) {
	return s.transact(ctx, nil)
}

func entry(provider, account string) (string, error) {
	if !utf8.ValidString(account) {
		return "", ErrInvalidRecord
	}
	if strings.TrimSpace(account) == "" {
		return "", errors.New("credential account is empty")
	}
	if _, err := service(provider); err != nil {
		return "", err
	}
	return provider + "/" + account, nil
}
func (s *RecordStore) LoadOAuth(ctx context.Context, provider, account string) (OAuth, error) {
	key, err := entry(provider, account)
	if err != nil {
		return OAuth{}, err
	}
	r, err := s.transact(ctx, nil)
	if err != nil {
		return OAuth{}, err
	}
	v, ok := r.OAuth[key]
	if !ok {
		return OAuth{}, ErrNotFound
	}
	return v, nil
}
func (s *RecordStore) SaveOAuth(ctx context.Context, provider, account string, v OAuth) error {
	key, err := entry(provider, account)
	if err != nil {
		return err
	}
	if v.AccessToken == "" && v.RefreshToken == "" {
		return errors.New("OAuth credential is empty")
	}
	_, err = s.transact(ctx, func(r *Record) { r.OAuth[key] = v })
	return err
}

// SwapOAuth saves next only if the stored entry still equals expected (nil: no entry). Otherwise it keeps
// the stored entry and returns it, so a token refresh never overwrites a newer login. It returns the entry
// that is stored afterwards; a missing entry returns ErrNotFound.
func (s *RecordStore) SwapOAuth(ctx context.Context, provider, account string, expected *OAuth, next OAuth) (OAuth, error) {
	key, err := entry(provider, account)
	if err != nil {
		return OAuth{}, err
	}
	if next.AccessToken == "" && next.RefreshToken == "" {
		return OAuth{}, errors.New("OAuth credential is empty")
	}
	var stored OAuth
	var found bool
	_, err = s.withLock(ctx, func() (Record, error) {
		r, err := s.readLocked(ctx)
		if err != nil {
			return Record{}, err
		}
		stored, found = r.OAuth[key]
		if found != (expected != nil) || found && !SameOAuth(stored, *expected) {
			return r, nil
		}
		r.OAuth[key], stored, found = next, next, true
		if err := s.backend.Write(ctx, r); err != nil {
			return Record{}, safeError(ctx, err)
		}
		return r, nil
	})
	if err != nil {
		return OAuth{}, err
	}
	if !found {
		return OAuth{}, ErrNotFound
	}
	return stored, nil
}

// SameOAuth compares two entries; times compare by instant, so a stored copy equals its original.
func SameOAuth(a, b OAuth) bool {
	return a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken && a.ExpiresAt.Equal(b.ExpiresAt) && a.Username == b.Username && slices.Equal(a.Scopes, b.Scopes)
}

// DeleteOAuth removes the login of an account. A missing entry is not an error.
func (s *RecordStore) DeleteOAuth(ctx context.Context, provider, account string) error {
	key, err := entry(provider, account)
	if err != nil {
		return err
	}
	_, err = s.withLock(ctx, func() (Record, error) {
		r, err := s.readLocked(ctx)
		if err != nil {
			return Record{}, err
		}
		if _, found := r.OAuth[key]; !found {
			return r, nil
		}
		delete(r.OAuth, key)
		if err := s.backend.Write(ctx, r); err != nil {
			return Record{}, safeError(ctx, err)
		}
		return r, nil
	})
	return err
}
func (s *RecordStore) LoadScreeningKey(ctx context.Context) (string, error) {
	r, err := s.transact(ctx, nil)
	if err != nil {
		return "", err
	}
	if r.ScreeningKey == "" {
		return "", ErrNotFound
	}
	return r.ScreeningKey, nil
}
func (s *RecordStore) SaveScreeningKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("screening key is empty")
	}
	_, err := s.transact(ctx, func(r *Record) { r.ScreeningKey = key })
	return err
}
func (s *RecordStore) LoadConsoleHash(ctx context.Context) (string, error) {
	r, err := s.transact(ctx, nil)
	if err != nil {
		return "", err
	}
	if r.ConsoleHash == "" {
		return "", ErrNotFound
	}
	return r.ConsoleHash, nil
}

// SaveConsoleHash stores a bcrypt hash of the console password; it refuses any other value so a plain
// password is never stored.
func (s *RecordStore) SaveConsoleHash(ctx context.Context, hash string) error {
	if !validConsoleHash(hash) {
		return ErrInvalidRecord
	}
	_, err := s.transact(ctx, func(r *Record) { r.ConsoleHash = hash })
	return err
}
func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{ErrNotFound, ErrUnsupportedVersion, ErrInvalidRecord, ErrDecrypt, ErrPasswordMissing, ErrInvalidPassword} {
		if errors.Is(err, known) {
			return known
		}
	}
	// Do not wrap backend errors: their text and unwrap chains can contain secrets.
	return ErrUnavailable
}
