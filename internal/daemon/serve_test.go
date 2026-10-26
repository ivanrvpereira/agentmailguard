package daemon_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// refusingAuthority is a fake token endpoint that refuses every refresh, as for a revoked login.
func refusingAuthority(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestServeExposureWarningCheckListAndBoundedShutdown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		listen            string
		warning, loggedIn bool
	}{{"127.0.0.1:0", false, true}, {"0.0.0.0:0", true, true}, {"127.0.0.1:0", false, false}} {
		home := writeHome(t, "version = 1\ndefault_account = 'work'\n[console]\nlisten = '"+freeAddress(t)+"'\n[accounts.work]\nprovider = 'm365'\nemail = 'synthetic@example.test'\nclient_id = 'synthetic'\ntenant_id = 'synthetic'\n")
		k := &keychain{}
		store := credentials.New(k, home)
		if err := store.SaveScreeningKey(t.Context(), "private-jev-canary"); err != nil {
			t.Fatal(err)
		}
		if tc.loggedIn {
			if err := store.SaveOAuth(t.Context(), "m365", "synthetic@example.test", credentials.OAuth{RefreshToken: "private-refresh-canary"}); err != nil {
				t.Fatal(err)
			}
		}
		// The fake authority refuses every refresh, so the logged-in account fails its sign-in check.
		authority := refusingAuthority(t)
		d := runServe(t, home, tc.listen, daemon.Options{Platform: k, Server: server.Options{M365: m365.Options{AuthorityURL: authority.URL, BaseURL: authority.URL}}})
		d.log.await(t, "daemon listening\n", 1, d.exit)
		// An idle daemon stops at once: it never waits out the 5 s HTTP drain.
		began := time.Now()
		if code := d.stop(); code != 0 {
			t.Fatalf("shutdown exit %d", code)
		}
		if took := time.Since(began); took >= 5*time.Second {
			t.Fatalf("the idle daemon took %v to stop; it waited out the HTTP drain", took)
		}
		text := d.log.String()
		account := "sign-in failed; check the network, or run amgd account login"
		if !tc.loggedIn {
			// An account that is not logged in does not block the start; its commands fail.
			account = "not logged in; commands for this account fail until amgd account login"
		}
		checks := "checking credentials\ncredential check: credentials (keychain): ok\ncredential check: account \"work\" (m365): " + account + "\ncredential check: screening key: ok\ncredential check: console password: not set; the console refuses every request until amgd config set console.password\napproval console listening\ndaemon listening\n"
		if !strings.HasSuffix(text, checks) || strings.Contains(text, "private-") {
			t.Errorf("credential checks must precede listening: %q", text)
		}
		if strings.Contains(text, "WARNING: unauthenticated mailbox access; no built-in TLS. A protected network or tunnel is required.\n") != tc.warning {
			t.Errorf("exposure warning mismatch: %s", text)
		}
	}
}

func TestServeReportsRefusedSignInOnTheAccountLine(t *testing.T) {
	t.Parallel()
	home := writeHome(t, "version = 1\ndefault_account = 'good'\n[console]\nlisten = '"+freeAddress(t)+"'\n[accounts.good]\nprovider = 'gmail'\nemail = 'good@example.test'\nclient_id = 'synthetic'\nclient_secret = 'synthetic'\n[accounts.revoked]\nprovider = 'gmail'\nemail = 'revoked@example.test'\nclient_id = 'revoked'\nclient_secret = 'synthetic'\n")
	k := &keychain{}
	store := credentials.New(k, home)
	if err := store.SaveScreeningKey(t.Context(), "private-jev-canary"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"good@example.test", "revoked@example.test"} {
		if err := store.SaveOAuth(t.Context(), "gmail", email, credentials.OAuth{RefreshToken: "private-refresh-canary"}); err != nil {
			t.Fatal(err)
		}
	}
	// Google refuses the revoked client's refresh token and accepts the other one.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/profile" {
			_, _ = io.WriteString(w, `{"emailAddress":"good@example.test"}`)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("client_id") == "revoked" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"private-access-canary","expires_in":3600,"scope":"`+gmailScopes+`"}`)
	}))
	defer api.Close()
	d := startServe(t, home, "127.0.0.1:0", daemon.Options{Platform: k, Server: server.Options{Gmail: gmail.Options{HTTPClient: api.Client(), TokenURL: api.URL + "/token", ProfileURL: api.URL + "/profile", OpenBrowser: refusingBrowser(new(bool))}}})
	want := "checking credentials\ncredential check: credentials (keychain): ok\ncredential check: account \"good\" (gmail): ok\ncredential check: account \"revoked\" (gmail): sign-in failed; check the network, or run amgd account login\ncredential check: screening key: ok\n"
	if text := d.log.String(); !strings.Contains(text, want) || strings.Contains(text, "private-") {
		t.Fatalf("sign-in checks: %q", text)
	}
}

// consoleConfig is a home without accounts, with fake screening and the console on listen.
func consoleConfig(listen string) string {
	return "version = 1\n[screening]\nprovider = 'fake'\n[console]\nlisten = '" + listen + "'\n"
}

func TestServeWarnsWhenTheApprovalConsoleListensBeyondLoopback(t *testing.T) {
	t.Parallel()
	_, port, err := net.SplitHostPort(freeAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	d := startServe(t, writeHome(t, consoleConfig("0.0.0.0:"+port)), "127.0.0.1:0", daemon.Options{Platform: &keychain{}})
	if text := d.log.String(); !strings.Contains(text, "WARNING: the approval console listens beyond loopback. Use an HTTPS tunnel such as tailscale serve.\napproval console listening\n") {
		t.Fatalf("log=%q", text)
	}
}

func TestServeRefusesToStartWhenTheConsolePortIsTaken(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	code, _, errout := amgd(t, writeHome(t, consoleConfig(taken.Addr().String())), daemon.Options{Platform: &keychain{}}, "", "serve", "--listen", "127.0.0.1:0")
	if code != 1 || !strings.HasSuffix(errout, "error: console listener unavailable\n") || strings.Contains(errout, "listening") {
		t.Fatalf("exit %d stderr %q", code, errout)
	}
}

// A reload that loaded the new settings but could not write its audit record is not a refused reload: the new
// settings apply, and the log reports the audit failure on its own line.
func TestReloadWhoseAuditFailsStillAppliesAndSaysSo(t *testing.T) {
	t.Parallel()
	home := writeHome(t, consoleConfig(freeAddress(t)))
	reloads := make(chan os.Signal, 1)
	d := startServe(t, home, "127.0.0.1:0", daemon.Options{Platform: &keychain{}, Reloads: reloads})
	if err := os.Mkdir(filepath.Join(home, "audit.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	reloads <- syscall.SIGUSR1
	d.log.awaitAny(t, 1, d.exit, "reload audit record failed\n", "old settings kept\n")
	tail := d.log.String()[strings.LastIndex(d.log.String(), "reload requested\n"):]
	if !strings.HasSuffix(tail, "reload ok\nerror: reload audit record failed\n") || strings.Contains(tail, "refused") {
		t.Fatalf("reload log:\n%s", tail)
	}
}
