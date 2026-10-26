package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func TestInvalidRecordsFailClosedWithoutFallbackOrOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"unsupported", `{"version":2,"oauth":{},"openrouter_key":"private-canary"}`, credentials.ErrUnsupportedVersion},
		{"empty item", "", credentials.ErrInvalidRecord},
		{"truncated", `{"version":1,"oauth":`, credentials.ErrInvalidRecord},
		{"null", `null`, credentials.ErrInvalidRecord},
		{"missing version", `{"oauth":{}}`, credentials.ErrInvalidRecord},
		{"missing oauth", `{"version":1,"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"null oauth", `{"version":1,"oauth":null,"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"bad token", `{"version":1,"oauth":{"gmail/work":{}},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"token case alias", `{"version":1,"oauth":{"gmail/work":{"REFRESH_TOKEN":"private-canary"}},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"token case duplicate", `{"version":1,"oauth":{"gmail/work":{"refresh_token":"first","REFRESH_TOKEN":"private-canary"}},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"bad provider", `{"version":1,"oauth":{"other/work":{"refresh_token":"private-canary"}},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"empty account", `{"version":1,"oauth":{"gmail/":{"refresh_token":"private-canary"}},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"duplicate", `{"version":2,"version":1,"oauth":{},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"case alias", `{"Version":1,"oauth":{},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"case duplicate", `{"version":2,"Version":1,"oauth":{},"openrouter_key":""}`, credentials.ErrInvalidRecord},
		{"trailing", `{"version":1,"oauth":{},"openrouter_key":""} {}`, credentials.ErrInvalidRecord},
		{"extra field", `{"version":1,"oauth":{},"openrouter_key":"","private-canary":true}`, credentials.ErrInvalidRecord},
		{"null key", `{"version":1,"oauth":{},"openrouter_key":null}`, credentials.ErrInvalidRecord},
		{"bad utf8", "{\"version\":1,\"oauth\":{},\"openrouter_key\":\"\xff\"}", credentials.ErrInvalidRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePlatform{values: map[[2]string][]byte{{"agentmailguard", "credentials"}: []byte(tc.raw)}}
			s := credentials.New(p, t.TempDir())
			for _, op := range []func() error{
				func() error { _, e := s.LoadScreeningKey(context.Background()); return e },
				func() error { return s.SaveScreeningKey(context.Background(), "replacement-canary") },
			} {
				err := op()
				if !errors.Is(err, tc.want) {
					t.Fatalf("wrong fixed failure: %v", err)
				}
				if strings.Contains(err.Error(), "canary") {
					t.Fatal("error exposed credential data")
				}
			}
			if p.writes != 0 {
				t.Fatal("invalid record overwritten")
			}
		})
	}
}
