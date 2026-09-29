package m365_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

// No printable fields: the canaries must come from the client's own cached state.
type formattingStore struct{}

func (formattingStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	return credentials.OAuth{AccessToken: goodToken(), RefreshToken: "format-refresh-canary", ExpiresAt: now.Add(time.Hour)}, nil
}
func (formattingStore) SaveOAuth(context.Context, string, string, credentials.OAuth) error {
	panic("unexpected save")
}
func (formattingStore) LoadOpenRouterKey(context.Context) (string, error) {
	panic("unexpected key access")
}
func (formattingStore) SaveOpenRouterKey(context.Context, string) error {
	panic("unexpected key access")
}

func TestClientFormattingNeverExposesCachedSecrets(t *testing.T) {
	a := account()
	a.ClientSecret = "format-config-canary"
	c := m365.New(a, formattingStore{}, m365.Options{Now: func() time.Time { return now }})
	if _, _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{c, *c} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%f", "%t", "%c", "%U", "%20.5s"} {
			printed := fmt.Sprintf(format, value)
			for _, secret := range []string{goodToken(), "format-refresh-canary", "format-config-canary"} {
				if strings.Contains(printed, secret) {
					t.Errorf("%T formatting with %s exposed a secret", value, format)
				}
			}
		}
	}
}
