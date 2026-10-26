package credentials_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func TestSwapOAuthNeverOverwritesANewerLogin(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{values: map[[2]string][]byte{}}
	s := credentials.New(p, t.TempDir())
	old := credentials.OAuth{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Username: "ivan@example.test", Scopes: []string{"a"}}
	if err := s.SaveOAuth(ctx, "m365", "ivan@example.test", old); err != nil {
		t.Fatal(err)
	}
	// The daemon read old; a time in another zone is the same instant, so it still matches.
	expected := old
	expected.ExpiresAt = old.ExpiresAt.In(time.FixedZone("x", 3600))
	refreshed := credentials.OAuth{AccessToken: "refreshed-access", RefreshToken: "old-refresh", Username: "ivan@example.test"}
	got, err := s.SwapOAuth(ctx, "m365", "ivan@example.test", &expected, refreshed)
	if err != nil || got.AccessToken != "refreshed-access" {
		t.Fatalf("swap=%v error=%v", got.AccessToken, err)
	}
	// A login made since then is kept, and the daemon gets it back.
	login := credentials.OAuth{AccessToken: "login-access", RefreshToken: "login-refresh", Username: "ivan@example.test"}
	if err := s.SaveOAuth(ctx, "m365", "ivan@example.test", login); err != nil {
		t.Fatal(err)
	}
	writes := p.writes
	got, err = s.SwapOAuth(ctx, "m365", "ivan@example.test", &refreshed, credentials.OAuth{AccessToken: "stale-access", RefreshToken: "old-refresh"})
	if err != nil || got.RefreshToken != "login-refresh" || p.writes != writes {
		t.Fatalf("swap=%v error=%v writes=%d", got.RefreshToken, err, p.writes-writes)
	}
	if v, _ := s.LoadOAuth(ctx, "m365", "ivan@example.test"); v.RefreshToken != "login-refresh" {
		t.Fatalf("stored=%v", v.RefreshToken)
	}
	// An entry that appeared where none was expected is kept too.
	if got, err := s.SwapOAuth(ctx, "m365", "ivan@example.test", nil, credentials.OAuth{RefreshToken: "stale"}); err != nil || got.RefreshToken != "login-refresh" {
		t.Fatalf("swap=%v error=%v", got.RefreshToken, err)
	}
}

func TestSwapOAuthDoesNotRestoreARemovedLogin(t *testing.T) {
	ctx := context.Background()
	p := &fakePlatform{values: map[[2]string][]byte{}}
	s := credentials.New(p, t.TempDir())
	old := credentials.OAuth{RefreshToken: "old-refresh"}
	if err := s.SaveOAuth(ctx, "gmail", "ivan@example.test", old); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOAuth(ctx, "gmail", "ivan@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadOAuth(ctx, "gmail", "ivan@example.test"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("deleted entry: %v", err)
	}
	if _, err := s.SwapOAuth(ctx, "gmail", "ivan@example.test", &old, credentials.OAuth{RefreshToken: "refreshed"}); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("swap after delete: %v", err)
	}
	if _, err := s.LoadOAuth(ctx, "gmail", "ivan@example.test"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("swap restored the entry: %v", err)
	}
	if err := s.DeleteOAuth(ctx, "gmail", "ivan@example.test"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}
