package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// Header starts every config.toml that amgd writes.
const Header = "# Written by amgd. Comments are not kept.\n"

// Edit changes config.toml in one atomic step. It decodes the file, or starts a new one with version = 1, lets
// change edit the document, encodes the whole document, and validates the result with Parse, the code that the
// daemon uses. The file changes only when the result is valid. Comments are not kept. A lock file next to
// config.toml serializes edits, so two amgd commands cannot lose each other's change.
func Edit(ctx context.Context, path string, change func(document map[string]any) error) (Config, error) {
	unlock, err := lock(ctx, path+".lock")
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	document := map[string]any{"version": int64(Version)}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Config{}, Error{"", "cannot read the file"}
	default:
		if document, err = decode(data); err != nil {
			return Config{}, err
		}
	}
	if err := change(document); err != nil {
		return Config{}, err
	}
	var b bytes.Buffer
	b.WriteString(Header)
	if toml.NewEncoder(&b).Encode(document) != nil {
		return Config{}, Error{"", "cannot encode the file"}
	}
	c, err := Parse(b.Bytes())
	if err != nil {
		return Config{}, err
	}
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	return c, replace(path, b.Bytes())
}

// replace writes data to a new private file and renames it over path, so readers see the old or the new file.
func replace(path string, data []byte) error {
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0700) != nil {
		return Error{"", "cannot create the home directory"}
	}
	f, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return Error{"", "cannot write the file"}
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if f.Chmod(0600) != nil {
		return Error{"", "cannot secure the file"}
	}
	if _, err := f.Write(data); err != nil || f.Sync() != nil || f.Close() != nil {
		return Error{"", "cannot write the file"}
	}
	if os.Rename(f.Name(), path) != nil {
		return Error{"", "cannot replace the file"}
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// lock takes an exclusive lock on a private lock file and waits while another process holds it.
func lock(ctx context.Context, path string) (func(), error) {
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return nil, Error{"", "cannot create the home directory"}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, Error{"", "cannot open the lock file"}
	}
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return nil, Error{"", "cannot secure the lock file"}
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			_ = f.Close()
			return nil, Error{"", "cannot lock the file"}
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
