package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
)

type fakePlatform struct {
	values  map[[2]string][]byte
	failure error
	writes  int
}

func (f *fakePlatform) Read(ctx context.Context, service, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.failure != nil {
		return nil, f.failure
	}
	return append([]byte(nil), f.values[[2]string{service, account}]...), nil
}
func (f *fakePlatform) Write(ctx context.Context, service, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.failure != nil {
		return f.failure
	}
	f.writes++
	f.values[[2]string{service, account}] = append([]byte(nil), value...)
	return nil
}
func TestCredentialFailuresAreSafeAndNeverFallBack(t *testing.T) {
	platform := &fakePlatform{values: map[[2]string][]byte{}}
	store := credentials.New(platform)
	ctx := context.Background()
	token := credentials.OAuth{RefreshToken: "SAVED-CANARY"}
	if err := store.SaveOAuth(ctx, "m365", "work", token); err != nil {
		t.Fatal(err)
	}
	platform.failure = errors.New("SECRET-CANARY from native backend")
	for _, operation := range []func() error{
		func() error { _, err := store.LoadOAuth(ctx, "m365", "work"); return err },
		func() error {
			return store.SaveOAuth(ctx, "m365", "work", credentials.OAuth{RefreshToken: "NEW-CANARY"})
		},
		func() error { _, err := store.LoadOpenRouterKey(ctx); return err },
		func() error { return store.SaveOpenRouterKey(ctx, "NEW-CANARY") },
	} {
		err := operation()
		if err == nil || strings.Contains(err.Error(), "CANARY") {
			t.Errorf("unsafe error: %v", err)
		}
	}
	platform.failure = nil
	got, err := store.LoadOAuth(ctx, "m365", "work")
	if err != nil || got.RefreshToken != "SAVED-CANARY" {
		t.Fatal("failed write lost old credential")
	}
	for _, operation := range []func() error{
		func() error { return store.SaveOAuth(ctx, "m365", "", token) },
		func() error { return store.SaveOAuth(ctx, "unsupported", "work", token) },
		func() error { return store.SaveOpenRouterKey(ctx, "  ") },
		func() error { return store.SaveOAuth(ctx, "gmail", "work", credentials.OAuth{}) },
	} {
		if err := operation(); err == nil {
			t.Error("invalid credential accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.SaveOpenRouterKey(cancelled, "NEW"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if platform.writes != 1 {
		t.Fatalf("unsafe writes reached platform: %d", platform.writes)
	}
	if _, err := credentials.New(nil).LoadOpenRouterKey(ctx); !errors.Is(err, credentials.ErrUnavailable) {
		t.Fatalf("nil backend=%v", err)
	}
}

func TestCredentialsRoundTripWithSeparateServices(t *testing.T) {
	platform := &fakePlatform{values: map[[2]string][]byte{}}
	var store credentials.Store = credentials.New(platform)
	ctx := context.Background()
	token := credentials.OAuth{AccessToken: "ACCESS-CANARY", RefreshToken: "REFRESH-CANARY", ExpiresAt: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC), Scopes: []string{"Mail.ReadWrite"}, Username: "synthetic@example.test"}
	for _, provider := range []string{"m365", "gmail"} {
		if err := store.SaveOAuth(ctx, provider, "work", token); err != nil {
			t.Fatal(err)
		}
		got, err := store.LoadOAuth(ctx, provider, "work")
		if err != nil || !reflect.DeepEqual(got, token) {
			t.Fatalf("round trip failed: %v", err)
		}
		token.RefreshToken = "OTHER-CANARY"
	}
	if err := store.SaveOpenRouterKey(ctx, "KEY-CANARY"); err != nil {
		t.Fatal(err)
	}
	key, err := store.LoadOpenRouterKey(ctx)
	if err != nil || key != "KEY-CANARY" {
		t.Fatalf("key round trip failed: %v", err)
	}
	for _, key := range [][2]string{{"agentmailguard-go-m365", "work"}, {"agentmailguard-go-gmail", "work"}, {"agentmailguard-go-openrouter", "agentmailguard-go"}} {
		if _, ok := platform.values[key]; !ok {
			t.Errorf("missing Go-specific namespace %v", key)
		}
	}
	if _, err := store.LoadOAuth(ctx, "gmail", "other"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("missing account=%v", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%f"} {
		if strings.Contains(fmt.Sprintf(format, token), "CANARY") {
			t.Fatal("token formatting exposes secrets")
		}
	}
}
