package gmail_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

type formatStore struct{ credentials.Store }

func (formatStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	token := validStore().token
	token.AccessToken = "SYNTHETIC_ACCESS_SECRET"
	token.RefreshToken = "SYNTHETIC_REFRESH_SECRET"
	return token, nil
}

func TestCachedClientValuesAndPointersRedactAllFormattingVerbs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"emailAddress": "synthetic@example.test"})
	}))
	defer server.Close()
	a := account
	a.ClientSecret = "SYNTHETIC_CLIENT_SECRET"
	c := gmail.New(a, formatStore{}, gmail.Options{HTTPClient: server.Client(), ProfileURL: server.URL})
	if _, _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{c, *c} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%f"} {
			printed := fmt.Sprintf(verb, value)
			for _, secret := range []string{"SYNTHETIC_ACCESS_SECRET", "SYNTHETIC_REFRESH_SECRET", "SYNTHETIC_CLIENT_SECRET"} {
				if strings.Contains(printed, secret) {
					t.Errorf("%s leaks a synthetic credential for %T", verb, value)
				}
			}
			if printed != "GmailClient{redacted}" {
				t.Errorf("%s bypasses redaction for %T", verb, value)
			}
		}
	}
}
