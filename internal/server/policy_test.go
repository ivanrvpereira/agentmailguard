package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestRunningServerRequiresRestartAfterPolicyEdit(t *testing.T) {
	const account = "[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"
	for _, tc := range []struct{ name, edited, want string }{
		{"global policy", "[policy]\nmail_send='approve'\n" + account, `{"error":"account configuration changed; restart required"}`},
		{"account policy", account + "[accounts.work.policy]\nmail_trash='deny'\n", `{"error":"account configuration changed; restart required"}`},
		{"sealed policy", account + "[accounts.work.policy.sealed]\nmail_trash='allow'\n", `{"error":"account configuration changed; restart required"}`},
		{"invalid policy", "[policy]\nmail_send='SECRET-CANARY'\n" + account, `{"error":"configuration unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			write := func(text string) {
				if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
			}
			write(account)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"value":[{"id":"f1","displayName":"Inbox","childFolderCount":0}]}`)
			}))
			defer api.Close()
			s, e := server.New(context.Background(), home, server.Options{Credentials: &store{}, M365: m365.Options{BaseURL: api.URL}})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			h := httptest.NewServer(s)
			defer h.Close()
			if status, b := rpc(t, h.URL, `{"operation":"mail.folders","params":{}}`); status != 200 {
				t.Fatalf("before edit %d %s", status, b)
			}
			write(tc.edited)
			if status, b := rpc(t, h.URL, `{"operation":"mail.folders","params":{}}`); status != 500 || b != tc.want {
				t.Fatalf("after edit %d %s", status, b)
			}
		})
	}
}
