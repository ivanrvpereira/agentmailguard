package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"golang.org/x/crypto/bcrypt"
)

var item = [2]string{"agentmailguard", "credentials"}

func testHash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func TestRecordWithoutApprovalPasswordKeepsTheOldShape(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{values: map[[2]string][]byte{item: []byte(`{"version":1,"oauth":{},"openrouter_key":"old-key"}`)}}
	store := credentials.New(p, t.TempDir(), nil)
	if _, err := store.LoadApprovalHash(ctx); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unset approval password: %v", err)
	}
	if err := store.SaveOpenRouterKey(ctx, "new-key"); err != nil {
		t.Fatal(err)
	}
	if got := string(p.values[item]); got != `{"version":1,"oauth":{},"openrouter_key":"new-key"}` {
		t.Fatalf("record shape changed: %s", got)
	}
}

func TestApprovalHashRoundTripsAndOnlyAcceptsBcrypt(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{values: map[[2]string][]byte{}}
	store := credentials.New(p, t.TempDir(), nil)
	if err := store.SaveOpenRouterKey(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "private-canary", "$2a$04$short", testHash(t, "other") + " private-canary"} {
		if err := store.SaveApprovalHash(ctx, bad); err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatalf("non-bcrypt value accepted or exposed: %v", err)
		}
	}
	if p.writes != 1 {
		t.Fatal("refused hash reached the platform")
	}
	hash := testHash(t, "private-approval")
	if err := store.SaveApprovalHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	got, err := credentials.New(p, t.TempDir(), nil).LoadApprovalHash(ctx)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(got), []byte("private-approval")) != nil {
		t.Fatalf("hash round trip failed: %v", err)
	}
	if key, err := store.LoadOpenRouterKey(ctx); err != nil || key != "key" {
		t.Fatal("saving the hash lost the Jev key")
	}
	record := credentials.Record{Version: 1, OAuth: map[string]credentials.OAuth{}, ApprovalHash: hash}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, record), "$2a$") {
			t.Fatal("record formatting exposes the hash")
		}
	}
}

func TestApprovalHashFieldIsStrictlyDecoded(t *testing.T) {
	for name, raw := range map[string]string{
		"null":       `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":null}`,
		"empty":      `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":""}`,
		"number":     `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":1}`,
		"not bcrypt": `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":"private-canary"}`,
		"alias":      `{"version":1,"oauth":{},"openrouter_key":"","Approval_Hash":"private-canary"}`,
		"duplicate":  `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":"a","approval_hash":"b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakePlatform{values: map[[2]string][]byte{item: []byte(raw)}}
			_, err := credentials.New(p, t.TempDir(), nil).LoadApprovalHash(context.Background())
			if !errors.Is(err, credentials.ErrInvalidRecord) || p.writes != 0 {
				t.Fatalf("invalid approval hash accepted: %v", err)
			}
		})
	}
}

func TestMigrationKeepsTheApprovalHash(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	source := credentials.New(&fakePlatform{values: map[[2]string][]byte{}}, home, nil)
	if err := source.SaveApprovalHash(ctx, testHash(t, "private-approval")); err != nil {
		t.Fatal(err)
	}
	target := credentials.NewFileForTest(home, "synthetic-password")
	if err := source.Migrate(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := target.ChangePassword(ctx, "other-password"); err != nil {
		t.Fatal(err)
	}
	got, err := credentials.NewFileForTest(home, "other-password").LoadApprovalHash(ctx)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(got), []byte("private-approval")) != nil {
		t.Fatalf("migration or password change lost the hash: %v", err)
	}
}
