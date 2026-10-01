package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"filippo.io/age"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestPasswordChangeBlocksRunningServerRefreshWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	home := twoAccountHome(t)
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	store := credentials.NewFile(home, "old-password")
	token := credentials.OAuth{AccessToken: statusJWT(m365Grant), RefreshToken: "original-refresh", ExpiresAt: now.Add(time.Hour), Scopes: strings.Fields(m365Grant)}
	var fixture bytes.Buffer
	recipient, _ := age.NewScryptRecipient("old-password")
	recipient.SetWorkFactor(1)
	encrypted, err := age.Encrypt(&fixture, recipient)
	if err != nil {
		t.Fatal("fixture encryption")
	}
	if json.NewEncoder(encrypted).Encode(credentials.Record{Version: 1, OAuth: map[string]credentials.OAuth{"m365/work": token}, OpenRouterKey: "original-key"}) != nil || encrypted.Close() != nil || os.WriteFile(filepath.Join(home, "credentials.age"), fixture.Bytes(), 0600) != nil {
		t.Fatal("fixture write")
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response(w, map[string]any{"access_token": statusJWT(m365Grant), "refresh_token": "refreshed-token", "expires_in": 3600})
	}))
	defer api.Close()
	s, err := server.New(ctx, home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, Now: clock, M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Preload(ctx, nil); err != nil {
		t.Fatal("preload")
	}
	external := credentials.NewFile(home, "old-password")
	if external.ChangePassword(ctx, "new-password") != nil {
		t.Fatal("password change")
	}
	before, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	h := httptest.NewServer(s)
	defer h.Close()
	// Existing memory remains usable until refresh; no automatic reconfiguration.
	if code, body := rpc(t, h.URL, `{"operation":"auth.status","account":"work","params":{}}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatal("password change reconfigured running server")
	}
	mu.Lock()
	now = now.Add(2 * time.Hour)
	mu.Unlock()
	if _, body := rpc(t, h.URL, `{"operation":"auth.status","account":"work","params":{}}`); strings.Contains(body, `"ok":true`) {
		t.Fatal("old-password server saved refresh")
	}
	after, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	if !bytes.Equal(before, after) {
		t.Fatal("old-password server overwrote changed record")
	}
	fresh := credentials.NewFile(home, "new-password")
	if key, err := fresh.LoadOpenRouterKey(ctx); err != nil || key != "original-key" {
		t.Fatal("external key lost")
	}
	if saved, err := fresh.LoadOAuth(ctx, "m365", "work"); err != nil || saved.RefreshToken != "original-refresh" {
		t.Fatal("failed refresh changed persisted token")
	}
}
