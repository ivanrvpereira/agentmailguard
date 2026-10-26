package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

type concurrentPlatform struct {
	mu    sync.Mutex
	value []byte
	reads int
}

func (p *concurrentPlatform) Read(_ context.Context, service, account string) ([]byte, error) {
	p.mu.Lock()
	p.reads++
	var v []byte
	if service == "agentmailguard" && account == "credentials" {
		v = append([]byte(nil), p.value...)
	}
	p.mu.Unlock()
	// Make overlapping read-modify-writes lose updates unless serialized.
	time.Sleep(time.Millisecond)
	return v, nil
}
func (p *concurrentPlatform) Write(_ context.Context, _, _ string, v []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = append([]byte(nil), v...)
	return nil
}
func TestConcurrentStoresPreserveEveryEntry(t *testing.T) {
	home := t.TempDir()
	p := &concurrentPlatform{}
	stores := []*credentials.RecordStore{credentials.New(p, home), credentials.New(p, home)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for i, s := range stores {
		go func() {
			for j := range 10 {
				if err := s.SaveOAuth(ctx, "gmail", fmt.Sprintf("%d-%d", i, j), credentials.OAuth{RefreshToken: "synthetic"}); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for range stores {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		for j := range 10 {
			if _, err := stores[0].LoadOAuth(ctx, "gmail", fmt.Sprintf("%d-%d", i, j)); err != nil {
				t.Fatal("concurrent save lost an entry")
			}
		}
	}
}
func TestStoreLockAcrossProcesses(t *testing.T) {
	if home := os.Getenv("AMG_LOCK_TEST_HOME"); home != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		p := &concurrentPlatform{}
		if err := credentials.New(p, home).SaveScreeningKey(ctx, "synthetic"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("child ignored cross-process lock")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.reads != 0 {
			t.Fatal("child accessed backend while another process held the lock")
		}
		return
	}
	home := t.TempDir()
	f, err := os.OpenFile(filepath.Join(home, "credentials.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreLockAcrossProcesses$", "-test.timeout=3s")
	cmd.Env = []string{"AMG_LOCK_TEST_HOME=" + home}
	if err := cmd.Run(); err != nil {
		t.Fatal("child did not honor the held lock")
	}
}

func TestStoreHonorsHomeFlockAndCancellation(t *testing.T) {
	home := t.TempDir()
	f, err := os.OpenFile(filepath.Join(home, "credentials.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	p := &concurrentPlatform{}
	s := credentials.New(p, home)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.SaveScreeningKey(ctx, "synthetic"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked save=%v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reads != 0 {
		t.Fatal("backend accessed outside lock")
	}
}
