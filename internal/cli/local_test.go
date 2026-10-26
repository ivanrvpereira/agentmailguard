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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
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

// signInWriter captures stderr and hands the sign-in URL, printed bare on its own line, to the test.
type signInWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	uri chan *url.URL
}

func (w *signInWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for line := range strings.SplitSeq(string(p), "\n") {
		if u, err := url.Parse(line); err == nil && u.Scheme == "http" && u.Query().Get("redirect_uri") != "" {
			w.uri <- u
		}
	}
	return w.buf.Write(p)
}
func (w *signInWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestGmailLoginWithoutBrowserAcceptsPastedRedirect(t *testing.T) {
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.personal]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='synthetic'\n[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/profile" {
			io.WriteString(w, `{"emailAddress":"synthetic@example.test"}`)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") != "private-code-canary" {
			t.Error("pasted code not exchanged")
		}
		io.WriteString(w, `{"access_token":"private-access-canary","refresh_token":"private-refresh-canary","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"}`)
	}))
	defer api.Close()
	store := &diagnosticStore{}
	stdin, paste := io.Pipe()
	defer paste.Close()
	stderr := &signInWriter{uri: make(chan *url.URL, 1)}
	opts := cli.Options{Stdin: stdin, LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: store, Gmail: gmail.Options{HTTPClient: api.Client(), TokenURL: api.URL + "/token", AuthURL: api.URL + "/auth", ProfileURL: api.URL + "/profile", OpenBrowser: func(context.Context, string) error {
		t.Error("--no-browser opened a browser")
		return nil
	}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"--home", home, "--account", "personal", "auth", "login", "--no-browser"}, &out, stderr, opts)
	}()
	select {
	case u := <-stderr.uri:
		q := u.Query()
		io.WriteString(paste, q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&code=private-code-canary\n")
	case code := <-done:
		t.Fatalf("login ended before printing the sign-in URL: exit=%d stderr=%q", code, stderr.String())
	}
	code, screen := <-done, stderr.String()
	if code != 0 || store.saves.Load() != 1 || !strings.Contains(out.String(), `"username": "synthetic@example.test"`) || strings.Contains(out.String()+screen, "private-") {
		t.Fatalf("pasted login: exit=%d saves=%d stdout=%q stderr=%q", code, store.saves.Load(), out.String(), screen)
	}
	for _, want := range []string{"Gmail sign-in for account personal\n", "1. Open this URL in a browser on any computer and approve access:\n\nhttp", "Address: ", "\n\n✓ Signed in as synthetic@example.test\n  Restart the server to use the new login.\n"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, `"http`) {
		t.Fatalf("sign-in URL is quoted:\n%s", screen)
	}
	var errout bytes.Buffer
	if code := cli.Run(ctx, []string{"--home", home, "--account", "work", "auth", "login", "--no-browser"}, io.Discard, &errout, opts); code != 1 || !strings.HasSuffix(errout.String(), "error: --no-browser is for Gmail; use --device-code for Microsoft 365\n") {
		t.Fatalf("Microsoft 365 accepted --no-browser: exit=%d stderr=%q", code, errout.String())
	}
	// Closed input leaves only the loopback callback; it must not spin on EOF.
	short, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	opts.Stdin = strings.NewReader("")
	closed := &signInWriter{uri: make(chan *url.URL, 1)}
	if code := cli.Run(short, []string{"--home", home, "--account", "personal", "auth", "login", "--no-browser"}, io.Discard, closed, opts); code != 1 || strings.Count(closed.String(), "Address: ") != 1 {
		t.Fatalf("closed input: exit=%d stderr=%q", code, closed.String())
	}
}
func TestFakeAccountLoginIsRefusedWithoutCredentialAccess(t *testing.T) {
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.demo]\nprovider='fake'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	// loginStore panics on any credential access other than an M365 work save. Provider seams fail, so a
	// broken refusal cannot reach a browser or the network.
	refuse := func(context.Context, string) error { t.Error("login opened a browser"); return errors.New("refused") }
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("provider request"); w.WriteHeader(400) }))
	defer closed.Close()
	opts := cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: &loginStore{}, M365: m365.Options{AuthorityURL: closed.URL, BaseURL: closed.URL, OpenBrowser: refuse}, Gmail: gmail.Options{TokenURL: closed.URL, AuthURL: closed.URL, ProfileURL: closed.URL, OpenBrowser: refuse}}}
	for _, flags := range [][]string{nil, {"--no-browser"}, {"--device-code"}} {
		var out, errout bytes.Buffer
		if code := cli.Run(context.Background(), append([]string{"--home", home, "auth", "login"}, flags...), &out, &errout, opts); code != 1 || out.Len() != 0 || !strings.HasSuffix(errout.String(), "error: fake accounts need no login\n") {
			t.Errorf("flags=%v exit=%d stderr=%q", flags, code, errout.String())
		}
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
		wantError := "error: provider login failed: could not open the browser\n"
		if tc.device {
			wantError = "error: provider login failed: device-code request failed\n"
		}
		if code != 1 || out.Len() != 0 || !strings.HasSuffix(errout.String(), wantError) || (deviceCalls == 1) != tc.device || (browserCalls == 1) == tc.device || deviceCalls+browserCalls != 1 {
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
		{[]string{"mail", "list", "--count", "-1"}, 400, `{"error":"invalid request"}`, 1, false},
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
