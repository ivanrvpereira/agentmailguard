package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	"filippo.io/age"
)

var ErrDecrypt = errors.New("wrong password or damaged file")
var ErrPasswordMissing = errors.New("password missing")
var ErrInvalidPassword = errors.New("invalid credential password")

const PasswordEnvironment = "AGENTMAILGUARD_CREDENTIALS_PASSWORD"

// TakePasswordEnv removes the process variable before a browser can inherit it.
func TakePasswordEnv(lookup func(string) string) (string, bool) {
	defer os.Unsetenv(PasswordEnvironment)
	if lookup == nil {
		return os.LookupEnv(PasswordEnvironment)
	}
	// An injected environment must not consult process values or their presence.
	password := lookup(PasswordEnvironment)
	return password, password != ""
}

// ValidatePassword rejects terminal control sequences without changing the password.
func ValidatePassword(password string) error {
	if password == "" {
		return ErrPasswordMissing
	}
	if len(password) > 4096 || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	for _, c := range password {
		if unicode.IsControl(c) {
			return ErrInvalidPassword
		}
	}
	return nil
}

type fileBackend struct {
	home, password string
	logN           int
}

func (fileBackend) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "FileCredentials{redacted}")
}

// FileWorkFactor is the scrypt work factor of credential files that NewFile writes.
// Only tests lower it, to keep scrypt cheap under the race detector. No production code changes it,
// and the native test in internal/server checks that the built binary still writes 18.
var FileWorkFactor = 18

// NewFile uses password encryption with the production scrypt work factor, FileWorkFactor.
// Missing or invalid passwords fail on the first store operation, including Preload.
func NewFile(home, password string) *RecordStore {
	return NewStore(home, &fileBackend{home: home, password: password, logN: FileWorkFactor})
}
func (b *fileBackend) Read(ctx context.Context) (Record, error) {
	if err := ValidatePassword(b.password); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	f, err := os.Open(filepath.Join(b.home, "credentials.age"))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, ErrUnavailable
	}
	defer f.Close()
	identity, _ := age.NewScryptIdentity(b.password)
	identity.SetMaxWorkFactor(18)
	plaintext, err := age.Decrypt(f, identity)
	if err != nil {
		return Record{}, ErrDecrypt
	}
	// Authenticate every chunk, including the final one, before decoding any record.
	data, err := io.ReadAll(plaintext)
	if err != nil {
		return Record{}, ErrDecrypt
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r, err := decodeRecord(data)
	if errors.Is(err, ErrInvalidRecord) {
		return Record{}, ErrDecrypt
	}
	return r, err
}
func (b *fileBackend) Write(ctx context.Context, r Record) error {
	if err := ValidatePassword(b.password); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRecord(r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return ErrInvalidRecord
	}
	f, err := os.CreateTemp(b.home, ".credentials-*.age")
	if err != nil {
		return ErrUnavailable
	}
	defer os.Remove(f.Name())
	defer f.Close()
	recipient, _ := age.NewScryptRecipient(b.password)
	recipient.SetWorkFactor(b.logN)
	encrypted, err := age.Encrypt(f, recipient)
	if err != nil {
		return ErrUnavailable
	}
	if _, err := encrypted.Write(data); err != nil {
		return ErrUnavailable
	}
	if encrypted.Close() != nil || f.Sync() != nil || f.Close() != nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Rename(f.Name(), filepath.Join(b.home, "credentials.age")) != nil {
		return ErrUnavailable
	}
	dir, err := os.Open(b.home)
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
