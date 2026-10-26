package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// lockName is the daemon lock in the home. The running daemon holds an exclusive flock on it and keeps its PID in
// it, so a second daemon for the same home fails and amgd signals only a daemon that runs.
const lockName = "server.lock"

var errNoDaemon = errors.New("no daemon runs for this home")

// holdLock takes the daemon lock and writes the PID into it. release gives it up; the file stays.
func holdLock(home string) (release func(), err error) {
	if os.MkdirAll(home, 0700) != nil {
		return nil, errors.New("cannot create the home directory")
	}
	f, err := os.OpenFile(filepath.Join(home, lockName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open the daemon lock")
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, errors.New("cannot secure the daemon lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			if pid, e := daemonPID(home); e == nil && pid > 0 {
				return nil, fmt.Errorf("another daemon runs for this home (PID %d)", pid)
			}
			return nil, errors.New("another daemon runs for this home")
		}
		return nil, errors.New("cannot lock the daemon lock")
	}
	if f.Truncate(0) != nil || func() error { _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); return err }() != nil || f.Sync() != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, errors.New("cannot write the daemon lock")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// daemonPID returns the PID of the daemon that holds the lock of home. errNoDaemon means that none runs. A
// probe uses a shared lock, so it never takes the lock from a daemon.
func daemonPID(home string) (int, error) {
	f, err := os.Open(filepath.Join(home, lockName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, errNoDaemon
	}
	if err != nil {
		return 0, errors.New("cannot open the daemon lock")
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0, errNoDaemon
	}
	if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
		return 0, errors.New("cannot read the daemon lock")
	}
	b, err := io.ReadAll(io.LimitReader(f, 32))
	pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || e != nil || pid <= 0 {
		return 0, errors.New("the daemon is starting; try again")
	}
	return pid, nil
}
