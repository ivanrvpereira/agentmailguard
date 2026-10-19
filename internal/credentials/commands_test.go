package credentials_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestChangePasswordPreservesLatestRecordAndBlocksOldWriters(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	old := credentials.NewFile(home, "old-password")
	if old.SaveScreeningKey(ctx, "original-key") != nil {
		t.Fatal("seed")
	}
	changer := credentials.NewFile(home, "old-password")
	if old.SaveOAuth(ctx, "gmail", "work", credentials.OAuth{RefreshToken: "latest-token"}) != nil || old.SaveScreeningKey(ctx, "latest-key") != nil {
		t.Fatal("update")
	}
	if err := changer.ChangePassword(ctx, "new-password"); err != nil {
		t.Fatal(err)
	}
	if err := old.SaveScreeningKey(ctx, "stale"); err != credentials.ErrDecrypt {
		t.Fatal("old process overwrote changed password")
	}
	fresh := credentials.NewFile(home, "new-password")
	if key, err := fresh.LoadScreeningKey(ctx); err != nil || key != "latest-key" {
		t.Fatal("latest key lost")
	}
	if token, err := fresh.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "latest-token" {
		t.Fatal("latest token lost")
	}
}

func TestChangePasswordFailuresPreserveFile(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "unknown-version", "wrong-current", "empty", "control", "oversize", "canceled", "write-error", "keychain"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			path := filepath.Join(home, "credentials.age")
			store := credentials.NewFile(home, "synthetic-password")
			if mode != "missing" && store.SaveScreeningKey(ctx, "old-key") != nil {
				t.Fatal("seed")
			}
			switch mode {
			case "corrupt":
				if os.WriteFile(path, []byte("damaged"), 0600) != nil {
					t.Fatal("fixture")
				}
			case "unknown-version":
				if os.WriteFile(path, encryptedFixture(t, `{"version":2,"oauth":{},"openrouter_key":""}`), 0600) != nil {
					t.Fatal("fixture")
				}
			case "wrong-current":
				store = credentials.NewFile(home, "wrong-password")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "write-error":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses permissions")
				}
				if os.Chmod(home, 0500) != nil {
					t.Fatal("chmod")
				}
				defer os.Chmod(home, 0700)
			case "keychain":
				store = credentials.New(nil, home, nil)
			}
			password := "new-password"
			switch mode {
			case "empty":
				password = ""
			case "control":
				password = "private-\x1b"
			case "oversize":
				password = strings.Repeat("x", 4097)
			}
			before, readErr := os.ReadFile(path)
			if err := store.ChangePassword(ctx, password); err == nil {
				t.Fatal("unsafe password change succeeded")
			}
			after, err := os.ReadFile(path)
			if mode == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("created missing source")
				}
			} else if readErr != nil || err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed password change replaced file")
			}
		})
	}
}

func TestChangePasswordUsesSharedLockAndLatestReread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	store := credentials.NewFile(home, "synthetic-password")
	if store.SaveScreeningKey(ctx, "old-key") != nil {
		t.Fatal("seed")
	}
	lock, err := os.OpenFile(filepath.Join(home, "credentials.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		t.Fatal("lock")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- store.ChangePassword(ctx, "new-password") }()
	<-started
	select {
	case <-done:
		t.Fatal("password change bypassed home lock")
	case <-time.After(30 * time.Millisecond):
	}
	// Model a completed writer while this command waits for the shared lock.
	data := encryptedFixture(t, `{"version":1,"oauth":{"gmail/work":{"refresh_token":"latest-token"}},"openrouter_key":"latest-key"}`)
	if os.WriteFile(filepath.Join(home, "credentials.age"), data, 0600) != nil {
		t.Fatal("fixture update")
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) != nil {
		t.Fatal("unlock")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fresh := credentials.NewFile(home, "new-password")
	if key, err := fresh.LoadScreeningKey(ctx); err != nil || key != "latest-key" {
		t.Fatal("missed latest key")
	}
	if token, err := fresh.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "latest-token" {
		t.Fatal("missed latest token")
	}
}

func TestMigrationDoesNotWriteAnExistingLegacyTarget(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	source := credentials.NewFile(home, "synthetic-password")
	if source.SaveScreeningKey(ctx, "source-key") != nil {
		t.Fatal("seed")
	}
	p := &fakePlatform{values: map[[2]string][]byte{{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte("legacy-key")}}
	if source.Migrate(ctx, credentials.New(p, home, nil)) != credentials.ErrTargetExists {
		t.Fatal("legacy target not refused")
	}
	if _, ok := p.values[[2]string{"agentmailguard", "credentials"}]; ok {
		t.Fatal("refused migration wrote target")
	}
}

func TestMigrationNeverCreatesTargetFromFailedSource(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "version", "denied"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			p := &fakePlatform{values: map[[2]string][]byte{}}
			switch mode {
			case "malformed":
				p.values[[2]string{"agentmailguard", "credentials"}] = []byte(`{`)
			case "version":
				p.values[[2]string{"agentmailguard", "credentials"}] = []byte(`{"version":2,"oauth":{},"openrouter_key":""}`)
			case "denied":
				p.readFailure = map[[2]string]error{{"agentmailguard", "credentials"}: errors.New("private-canary")}
			}
			source := credentials.New(p, home, nil)
			if err := source.Migrate(context.Background(), credentials.NewFile(home, "synthetic-password")); err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("missing/failed source accepted or exposed")
			}
			if _, err := os.Stat(filepath.Join(home, "credentials.age")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed source created target")
			}
		})
	}
}

func TestMigrationRefusesUnsafeCopies(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "version", "denied", "target-exists", "same"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			p := &fakePlatform{values: map[[2]string][]byte{}}
			source := credentials.New(p, home, nil)
			target := credentials.NewFile(home, "synthetic-password")
			if mode != "missing" {
				p.values[[2]string{"agentmailguard", "credentials"}] = []byte(`{"version":1,"oauth":{},"openrouter_key":"source-key"}`)
			}
			switch mode {
			case "malformed":
				p.values[[2]string{"agentmailguard", "credentials"}] = []byte(`{`)
			case "version":
				p.values[[2]string{"agentmailguard", "credentials"}] = []byte(`{"version":2,"oauth":{},"openrouter_key":""}`)
			case "denied":
				p.readFailure = map[[2]string]error{{"agentmailguard", "credentials"}: errors.New("private-canary")}
			}
			if target.SaveScreeningKey(ctx, "existing-key") != nil {
				t.Fatal("seed")
			}
			before, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
			if mode == "same" {
				target = credentials.New(p, home, nil)
			}
			if source.Migrate(ctx, target) == nil {
				t.Fatal("unsafe migration succeeded")
			}
			after, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
			if !bytes.Equal(before, after) {
				t.Fatal("target changed on refusal")
			}
		})
	}
}

func TestMigrationSerializesTargetCheckAndWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	source := credentials.NewFile(home, "synthetic-password")
	if source.SaveScreeningKey(ctx, "original") != nil {
		t.Fatal("seed")
	}
	p := &concurrentPlatform{}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- source.Migrate(ctx, credentials.New(p, home, nil)) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if err != credentials.ErrTargetExists {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("target overwritten by concurrent migration")
	}
	lock, err := os.OpenFile(filepath.Join(home, "credentials.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		t.Fatal("lock")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	bounded, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if err := source.Migrate(bounded, credentials.New(p, home, nil)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("migration bypassed shared lock")
	}
}

func TestCopyCredentialsPreservesCompleteSource(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	p := &fakePlatform{values: map[[2]string][]byte{}}
	source := credentials.New(p, home, nil)
	if source.SaveOAuth(ctx, "gmail", "work", credentials.OAuth{RefreshToken: "private-token"}) != nil || source.SaveScreeningKey(ctx, "private-key") != nil {
		t.Fatal("seed failed")
	}
	target := credentials.NewFile(home, "synthetic-password")
	if err := source.Migrate(ctx, target); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*credentials.RecordStore{source, target} {
		if key, err := store.LoadScreeningKey(ctx); err != nil || key != "private-key" {
			t.Fatal("key lost")
		}
		if token, err := store.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "private-token" {
			t.Fatal("token lost")
		}
	}
	if err := source.Migrate(ctx, target); err == nil {
		t.Fatal("overwrote target")
	}
}
