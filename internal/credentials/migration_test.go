package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
)

func TestFirstOperationMigratesAllConfiguredLegacyCredentials(t *testing.T) {
	for _, first := range []string{"load", "login", "set-key"} {
		t.Run(first, func(t *testing.T) {
			legacy := map[[2]string][]byte{
				{"agentmailguard-go-m365", "work"}:                    []byte(`{"refresh_token":"legacy-work"}`),
				{"agentmailguard-go-gmail", "personal"}:               []byte(`{"refresh_token":"legacy-personal"}`),
				{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte("legacy-key"),
			}
			p := &fakePlatform{values: map[[2]string][]byte{}}
			for k, v := range legacy {
				p.values[k] = append([]byte(nil), v...)
			}
			accounts := []config.Account{{Provider: "m365", Name: "work"}, {Provider: "gmail", Name: "personal"}, {Provider: "gmail", Name: "missing"}}
			store := credentials.New(p, t.TempDir(), accounts)
			ctx := context.Background()
			var err error
			switch first {
			case "load":
				_, err = store.LoadOAuth(ctx, "gmail", "missing")
				if errors.Is(err, credentials.ErrNotFound) {
					err = nil
				}
			case "login":
				err = store.SaveOAuth(ctx, "gmail", "new", credentials.OAuth{RefreshToken: "new-login"})
			case "set-key":
				err = store.SaveOpenRouterKey(ctx, "new-key")
			}
			if err != nil {
				t.Fatal(err)
			}
			// A later process needs only the single item, not the legacy account list.
			reopened := credentials.New(p, t.TempDir(), nil)
			for _, a := range accounts[:2] {
				v, err := reopened.LoadOAuth(ctx, a.Provider, a.Name)
				if err != nil || v.RefreshToken != "legacy-"+a.Name {
					t.Fatal("migration omitted a configured account")
				}
			}
			wantKey := "legacy-key"
			if first == "set-key" {
				wantKey = "new-key"
			}
			if key, err := reopened.LoadOpenRouterKey(ctx); err != nil || key != wantKey {
				t.Fatal("migration lost Jev key")
			}
			for k, v := range legacy {
				if !reflect.DeepEqual(p.values[k], v) {
					t.Fatal("legacy item changed")
				}
			}
			if _, err := reopened.LoadOAuth(ctx, "gmail", "missing"); !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("missing account changed")
			}
		})
	}
}

func TestPlatformErrorsAreNeverTreatedAsMissingItems(t *testing.T) {
	for _, mode := range []string{"read", "write"} {
		t.Run(mode, func(t *testing.T) {
			p := &fakePlatform{values: map[[2]string][]byte{
				{"agentmailguard-go-m365", "work"}: []byte(`{"refresh_token":"legacy-work"}`),
			}}
			failure := fmt.Errorf("private-canary: %w", credentials.ErrNotFound)
			if mode == "read" {
				p.readFailure = map[[2]string]error{{"agentmailguard-go-m365", "work"}: failure}
			} else {
				p.writeFailure = failure
			}
			store := credentials.New(p, t.TempDir(), []config.Account{{Provider: "m365", Name: "work"}})
			if err := store.SaveOpenRouterKey(context.Background(), "replacement"); err != credentials.ErrUnavailable {
				t.Errorf("platform error was treated as absence: %v", err)
			}
			if p.writes != 0 {
				t.Error("failed migration wrote a partial record")
			}
		})
	}
}

func TestMigrationDenialsNeverCreatePartialRecord(t *testing.T) {
	for _, mode := range []string{"account-read", "key-read", "write"} {
		t.Run(mode, func(t *testing.T) {
			p := &fakePlatform{values: map[[2]string][]byte{
				{"agentmailguard-go-m365", "work"}:                    []byte(`{"refresh_token":"legacy-work"}`),
				{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte("legacy-key"),
			}}
			failure := errors.New("private-backend-canary")
			switch mode {
			case "account-read":
				p.readFailure = map[[2]string]error{{"agentmailguard-go-gmail", "personal"}: failure}
			case "key-read":
				p.readFailure = map[[2]string]error{{"agentmailguard-go-openrouter", "agentmailguard-go"}: failure}
			case "write":
				p.writeFailure = failure
			}
			store := credentials.New(p, t.TempDir(), []config.Account{{Provider: "m365", Name: "work"}, {Provider: "gmail", Name: "personal"}})
			if err := store.SaveOpenRouterKey(context.Background(), "replacement"); err != credentials.ErrUnavailable {
				t.Fatal("migration did not return fixed unavailable error")
			}
			if p.writes != 0 || len(p.values) != 2 {
				t.Fatal("denied migration changed state")
			}
		})
	}
}

func TestMigrationRefusesPresentButEmptyLegacyKey(t *testing.T) {
	for _, raw := range []string{"", " \n\t"} {
		p := &fakePlatform{values: map[[2]string][]byte{
			{"agentmailguard-go-m365", "work"}:                    []byte(`{"refresh_token":"legacy-work"}`),
			{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte(raw),
		}}
		store := credentials.New(p, t.TempDir(), []config.Account{{Provider: "m365", Name: "work"}})
		if err := store.SaveOpenRouterKey(context.Background(), "new-key"); err != credentials.ErrInvalidRecord {
			t.Errorf("present invalid key was treated as missing: %v", err)
		}
		if p.writes != 0 {
			t.Error("invalid legacy key was hidden by migration")
		}
	}
}

func TestMigrationFailsClosedWithoutWritingPartialRecord(t *testing.T) {
	for _, raw := range []string{
		`{"refresh_token":`,
		`{"refresh_token":"first","refresh_token":"second"}`,
		`{"refresh_token":"first","REFRESH_TOKEN":"second"}`,
		`{"refresh_token":"first","unknown":"private-canary"}`,
		"{\"refresh_token\":\"\xff\"}",
	} {
		t.Run("malformed legacy token", func(t *testing.T) {
			p := &fakePlatform{values: map[[2]string][]byte{
				{"agentmailguard-go-m365", "work"}:      []byte(`{"refresh_token":"legacy-work"}`),
				{"agentmailguard-go-gmail", "personal"}: []byte(raw),
			}}
			store := credentials.New(p, t.TempDir(), []config.Account{{Provider: "m365", Name: "work"}, {Provider: "gmail", Name: "personal"}})
			if err := store.SaveOpenRouterKey(context.Background(), "new-key"); err != credentials.ErrInvalidRecord {
				t.Fatalf("failure=%v", err)
			}
			if p.writes != 0 {
				t.Fatal("failed migration wrote a partial record")
			}
		})
	}
}
