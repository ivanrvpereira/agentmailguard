package credentials_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"os"
	"path/filepath"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func encryptedFixture(t *testing.T, plaintext string) []byte {
	t.Helper()
	var out bytes.Buffer
	r, _ := age.NewScryptRecipient("synthetic-password")
	r.SetWorkFactor(1)
	w, err := age.Encrypt(&out, r)
	if err != nil {
		t.Fatal("fixture encryption failed")
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		t.Fatal("fixture write failed")
	}
	if w.Close() != nil {
		t.Fatal("fixture close failed")
	}
	return out.Bytes()
}

func TestFileStoreHostileInputs(t *testing.T) {
	valid := encryptedFixture(t, `{"version":1,"oauth":{},"openrouter_key":"synthetic-key"}`)
	tampered := append([]byte(nil), valid...)
	tampered[len(tampered)-1] ^= 1
	high := bytes.Replace(valid, []byte(" 1\n"), []byte(" 19\n"), 1)
	for _, tc := range []struct {
		name     string
		data     []byte
		password string
		want     error
	}{
		{"wrong-password", valid, "other-password", credentials.ErrDecrypt},
		{"truncated", valid[:len(valid)-1], "synthetic-password", credentials.ErrDecrypt},
		{"tampered", tampered, "synthetic-password", credentials.ErrDecrypt},
		{"higher-work", high, "synthetic-password", credentials.ErrDecrypt},
		{"absurd-work", bytes.Replace(valid, []byte(" 1\n"), []byte(" 999999999999999999999999\n"), 1), "synthetic-password", credentials.ErrDecrypt},
		{"unknown-version", encryptedFixture(t, `{"version":2,"oauth":{},"openrouter_key":""}`), "synthetic-password", credentials.ErrUnsupportedVersion},
		{"malformed-json", encryptedFixture(t, `{"version":1,`), "synthetic-password", credentials.ErrDecrypt},
		{"missing-password", valid, "", credentials.ErrPasswordMissing},
		{"control-password", valid, "private-\x1b[31m", credentials.ErrInvalidPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "credentials.age")
			if os.WriteFile(path, tc.data, 0600) != nil {
				t.Fatal("fixture write")
			}
			s := credentials.NewFile(home, tc.password)
			started := time.Now()
			if _, err := s.Preload(context.Background()); err != tc.want {
				t.Fatal("unsafe or missing fixed error")
			}
			if (tc.name == "higher-work" || tc.name == "absurd-work") && time.Since(started) > 500*time.Millisecond {
				t.Fatal("hostile work factor was not refused before expensive derivation")
			}
			if err := s.SaveScreeningKey(context.Background(), "replacement"); err != tc.want {
				t.Fatal("save did not fail closed")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, tc.data) {
				t.Fatal("failed write destroyed old file")
			}
			entries, _ := os.ReadDir(home)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".credentials-") {
					t.Fatal("temporary file survived failure")
				}
			}
		})
	}
	// A valid JSON first chunk must not be trusted without authenticating the last chunk.
	large := encryptedFixture(t, `{"version":2,"oauth":{},"openrouter_key":""}`+strings.Repeat(" ", 128<<10))
	large[len(large)-1] ^= 1
	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "credentials.age"), large, 0600)
	if _, err := credentials.NewFile(home, "synthetic-password").Preload(context.Background()); err != credentials.ErrDecrypt {
		t.Fatal("decoded before complete authentication")
	}
}

func TestFileStoreConcurrentAndCanceledWrites(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a, b := credentials.NewFile(home, "synthetic-password"), credentials.NewFile(home, "synthetic-password")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if a.SaveScreeningKey(ctx, "synthetic-key") != nil {
			t.Error("key save failed")
		}
	}()
	go func() {
		defer wg.Done()
		if b.SaveOAuth(ctx, "gmail", "work", credentials.OAuth{RefreshToken: "synthetic-token"}) != nil {
			t.Error("token save failed")
		}
	}()
	wg.Wait()
	if key, err := a.LoadScreeningKey(ctx); err != nil || key != "synthetic-key" {
		t.Fatal("concurrent key lost")
	}
	if token, err := a.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "synthetic-token" {
		t.Fatal("concurrent token lost")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.SaveScreeningKey(canceled, "replacement"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled write succeeded")
	}
	if key, _ := a.LoadScreeningKey(ctx); key != "synthetic-key" {
		t.Fatal("cancellation lost old key")
	}
}

func TestFileStoreFailedWritePreservesPreviousFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not constrain root")
	}
	home := t.TempDir()
	ctx := context.Background()
	s := credentials.NewFile(home, "synthetic-password")
	if s.SaveScreeningKey(ctx, "synthetic-key") != nil {
		t.Fatal("initial save failed")
	}
	before, err := os.ReadFile(filepath.Join(home, "credentials.age"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Chmod(home, 0500) != nil {
		t.Fatal("cannot constrain temporary home")
	}
	defer os.Chmod(home, 0700)
	if s.SaveScreeningKey(ctx, "replacement") == nil {
		t.Fatal("write to read-only home succeeded")
	}
	after, err := os.ReadFile(filepath.Join(home, "credentials.age"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed write lost old encrypted file")
	}
	if key, err := s.LoadScreeningKey(ctx); err != nil || key != "synthetic-key" {
		t.Fatal("old record no longer readable")
	}
}

func TestFileStoreEncryptedRoundTrip(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	s := credentials.NewFile(home, "synthetic-password")
	if err := s.SaveScreeningKey(ctx, "private-key-canary"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuth(ctx, "gmail", "work", credentials.OAuth{RefreshToken: "private-token-canary"}); err != nil {
		t.Fatal(err)
	}
	reopened := credentials.NewFile(home, "synthetic-password")
	if key, err := reopened.LoadScreeningKey(ctx); err != nil || key != "private-key-canary" {
		t.Fatal("key did not survive reopen")
	}
	if token, err := reopened.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "private-token-canary" {
		t.Fatal("token did not survive reopen")
	}
	entries, _ := os.ReadDir(home)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(home, entry.Name()))
		if err != nil || bytes.Contains(data, []byte("private-")) || bytes.Contains(data, []byte("synthetic-password")) {
			t.Fatal("secret persisted without encryption")
		}
	}
	info, err := os.Stat(filepath.Join(home, "credentials.age"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("encrypted file permissions")
	}
	if got := fmt.Sprintf("%+v %#v", s, s); bytes.Contains([]byte(got), []byte("synthetic-password")) {
		t.Fatal("store formatting exposed password")
	}
}
