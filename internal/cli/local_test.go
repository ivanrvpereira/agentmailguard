package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

type loginStore struct {
	credentials.Store
	saved *credentials.OAuth
}

func (s *loginStore) SaveOAuth(_ context.Context, provider, account string, v credentials.OAuth) error {
	if provider != "m365" || account != "work" {
		panic("incorrect credential namespace")
	}
	s.saved = &v
	return nil
}
func TestLocalDeviceLoginAndRemoteRefusal(t *testing.T) {
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/devicecode":
			io.WriteString(w, `{"device_code":"private-device-token","user_code":"SYNTHETIC-CODE","verification_uri":"https://example.test/verify","expires_in":60,"interval":1}`)
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("device_code") != "private-device-token" {
				t.Error("device flow payload")
			}
			token := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"}`)) + ".x"
			json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "private-refresh-token", "expires_in": 3600})
		case "/me":
			io.WriteString(w, `{"userPrincipalName":"synthetic@example.test"}`)
		default:
			t.Error("unexpected local login endpoint")
			w.WriteHeader(500)
		}
	}))
	defer h.Close()
	creds := &loginStore{}
	opts := cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: creds, M365: m365.Options{AuthorityURL: h.URL, BaseURL: h.URL, Wait: func(context.Context, time.Duration) error { return nil }, OpenBrowser: func(context.Context, string) error { t.Fatal("device flow opened browser"); return nil }}}}
	var out, errout bytes.Buffer
	code := cli.Run(context.Background(), []string{"--home", home, "auth", "login", "--device-code"}, &out, &errout, opts)
	if code != 0 || creds.saved == nil || calls != 3 || !strings.Contains(out.String(), `"username": "synthetic@example.test"`) || !strings.Contains(errout.String(), "SYNTHETIC-CODE") || strings.Contains(out.String()+errout.String(), "private-") {
		t.Fatalf("local login: code %d calls %d output %s %s", code, calls, out.String(), errout.String())
	}
	before := calls
	for _, args := range [][]string{{"--server", h.URL, "--home", home, "auth", "login"}, {"--server", "https://remote.example.test", "auth", "login"}} {
		out.Reset()
		errout.Reset()
		if code := cli.Run(context.Background(), args, &out, &errout, opts); code != 1 || out.Len() != 0 || calls != before {
			t.Fatal("remote login became a local operation")
		}
	}
	opts.LookupEnv = func(string) string { return h.URL }
	out.Reset()
	errout.Reset()
	if code := cli.Run(context.Background(), []string{"auth", "login"}, &out, &errout, opts); code != 1 || calls != before {
		t.Fatal("remote environment ignored")
	}
}
func TestDeviceCodeAliasesUseLastOption(t *testing.T) {
	for _, tc := range []struct {
		flags  []string
		device bool
	}{
		{[]string{"--no-device-code"}, false},
		{[]string{"--device-code", "--no-device-code"}, false},
		{[]string{"--no-device-code", "--device-code"}, true},
		{[]string{"--no-device-code=false"}, true},
		{[]string{"--no-device-code", "--device-code=false"}, false},
	} {
		home := t.TempDir()
		if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"), 0600); e != nil {
			t.Fatal(e)
		}
		deviceCalls, browserCalls := 0, 0
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/devicecode" {
				t.Error("unexpected provider request")
			}
			deviceCalls++
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"synthetic-refusal"}`)
		}))
		opts := cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: &loginStore{}, M365: m365.Options{AuthorityURL: h.URL, BaseURL: h.URL, OpenBrowser: func(context.Context, string) error {
			browserCalls++
			return errors.New("synthetic-refusal") // Never launch a browser.
		}}}}
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), append([]string{"--home", home, "auth", "login"}, tc.flags...), &out, &errout, opts)
		h.Close()
		if code != 1 || out.Len() != 0 || !strings.Contains(errout.String(), "error: provider login failed\n") || (deviceCalls == 1) != tc.device || (browserCalls == 1) == tc.device || deviceCalls+browserCalls != 1 {
			t.Errorf("flags=%v exit=%d device=%d browser seam=%d stderr=%q", tc.flags, code, deviceCalls, browserCalls, errout.String())
		}
	}
}

func TestCLIExitSemanticsAndServerErrors(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		status     int
		body       string
		exit       int
		wantOutput bool
	}{
		{[]string{"mail", "archive", "a", "b"}, 200, `[{"id":"a","ok":true,"error":null},{"id":"b","ok":false,"error":"failed"}]`, 0, true},
		{[]string{"auth", "status"}, 200, `{"account":"work","ok":false,"logged_in":false}`, 1, true},
		{[]string{"mail", "get", "x"}, 500, `{"error":"private-server-canary"}`, 1, false},
		{[]string{"mail", "organize", "--since", "2026-01-01"}, 400, `{"error":"invalid request"}`, 2, false},
		{[]string{"mail", "list", "--count", "101"}, 400, `{"error":"invalid request"}`, 1, false},
	} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Account string }
			json.NewDecoder(r.Body).Decode(&req)
			if req.Account != "" {
				t.Error("default account must remain server-selected")
			}
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		}))
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), tc.args, &out, &errout, cli.Options{LookupEnv: func(string) string { return h.URL }})
		h.Close()
		if code != tc.exit || (out.Len() > 0) != tc.wantOutput || strings.Contains(errout.String(), "canary") {
			t.Errorf("exit %d output %s stderr %s", code, out.String(), errout.String())
		}
	}
}
