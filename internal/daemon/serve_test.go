package daemon_test

import (
	"context"
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

	"github.com/ivanrvpereira/agentmailguard/internal/client"
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
		host, console     string
		warning, loggedIn bool
	}{
		{"127.0.0.1", "", false, true},
		// localhost is loopback too: no exposure warning.
		{"localhost", "", false, true},
		// The hosts, the username, and a second account with sends that wait show in the start lines.
		{"0.0.0.0", "hosts = ['daemon-host.example.ts.net', '192.0.2.7:8788']\nusername = 'ops'\n", true, true},
		// --listen :PORT is a wildcard listener too.
		{"", "hosts = ['daemon-host.example.ts.net', '192.0.2.7:8788']\nusername = 'ops'\n", true, true},
		{"127.0.0.1", "", false, false},
	} {
		console := freeAddress(t)
		other := ""
		if tc.warning {
			other = "[accounts.demo]\nprovider = 'fake'\nemail = 'demo@example.test'\n[accounts.demo.policy]\nmail_send = 'approve'\n"
		}
		home := writeHome(t, "version = 1\ndefault_account = 'work'\n[credentials]\nbackend = 'keychain'\n[console]\nlisten = '"+console+"'\n"+tc.console+"[accounts.work]\nprovider = 'm365'\nemail = 'synthetic@example.test'\nclient_id = 'synthetic'\ntenant_id = 'synthetic'\n"+other)
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
		listen := reserve(t, tc.host)
		d := runServe(t, home, listen, daemon.Options{Platform: k, Server: server.Options{M365: m365.Options{AuthorityURL: authority.URL, BaseURL: authority.URL}}})
		d.log.await(t, "amg account list\n", 1, d.exit)
		if tc.warning {
			d.log.await(t, "local-and-remote-clients\n", 1, d.exit)
		}
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
		demo, signIn := "", "user admin"
		if tc.console != "" {
			signIn = "user ops"
		}
		start := "approval console: http://" + console + "/ (" + signIn + ")\n" +
			"daemon listening: http://" + listen + "\n  agents connect with: AMG_HOST=http://" + listen + " amg account list\n"
		if tc.warning {
			demo = "credential check: account \"demo\" (fake): no login needed\n"
			_, port, _ := net.SplitHostPort(listen)
			// The daemon prints no host name that it would have to guess for a wildcard listener.
			start = "approval console: http://" + console + "/ (" + signIn + ")\n" +
				"  also accepted as: daemon-host.example.ts.net, 192.0.2.7:8788 (from console.hosts)\n" +
				"daemon listening: http://" + listen + "\n  agents connect with: AMG_HOST=http://DAEMON-HOST:" + port + " amg account list\n" +
				"WARNING: this API has no login and no TLS. Anyone who can reach port " + port + " on any address of this host can read screened mail and run every action that the write policy allows (sends: approve or deny).\n" +
				"  Restrict it: allow only the agent machine (Tailscale grant or firewall), or keep --listen on 127.0.0.1 and use an SSH tunnel. See docs/daemon.md#local-and-remote-clients\n"
		}
		want := "checking credentials\ncredential check: credentials (keychain): ok\n" + demo + "credential check: account \"work\" (m365): " + account + "\ncredential check: screening key: ok\ncredential check: console password: ok\n" + start
		if text != want {
			t.Errorf("start output:\n got %q\nwant %q", text, want)
		}
	}
}

// Without a console password and without a terminal to ask on, serve refuses to start, so that no one else can be
// the first to set the password in the console. The refusal names the command for the same home.
func TestServeRefusesToStartWithoutAConsolePassword(t *testing.T) {
	home := writeHome(t, consoleConfig(freeAddress(t)))
	for _, tc := range []struct {
		args []string
		env  string
		want string
	}{
		{[]string{"--home", home}, "", "amgd config set console.password --home " + home},
		{nil, home, "amgd config set console.password --home " + home},
	} {
		t.Setenv("AMG_HOME", tc.env)
		var errout strings.Builder
		// A daemon that started would run until ctx ends: it fails the test instead of hanging it.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		code := daemon.Run(ctx, append(tc.args, "serve", "--listen", "127.0.0.1:0"), io.Discard, &errout, daemon.Options{Platform: &keychain{}, LookupEnv: noEnv, Stdin: strings.NewReader(""), Listen: listenReserved})
		cancel()
		want := "credential check: console password: not set\nerror: start refused: run " + tc.want + ", then start the daemon again\n"
		if code != 1 || !strings.HasSuffix(errout.String(), want) || strings.Contains(errout.String(), "listening") {
			t.Errorf("%v AMG_HOME=%q: exit %d stderr %q", tc.args, tc.env, code, errout.String())
		}
	}
}

func TestServeReportsRefusedSignInOnTheAccountLine(t *testing.T) {
	t.Parallel()
	home := writeHome(t, "version = 1\ndefault_account = 'good'\n[credentials]\nbackend = 'keychain'\n[console]\nlisten = '"+freeAddress(t)+"'\n[accounts.good]\nprovider = 'gmail'\nemail = 'good@example.test'\nclient_id = 'synthetic'\nclient_secret = 'synthetic'\n[accounts.revoked]\nprovider = 'gmail'\nemail = 'revoked@example.test'\nclient_id = 'revoked'\nclient_secret = 'synthetic'\n")
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

// The AMG_HOST hint of a scoped IPv6 listener escapes the zone, so amg accepts it as it is printed.
func TestServeHintOfAScopedIPv6ListenerIsAURLThatAmgAccepts(t *testing.T) {
	t.Parallel()
	standIn := freeAddress(t)
	_, port, _ := net.SplitHostPort(standIn)
	listen := "[fe80::1%synthetic0]:" + port
	opts := daemon.Options{Platform: &keychain{}, Listen: func(network, address string) (net.Listener, error) {
		if address == listen {
			// No host has this interface: a loopback socket on the same port stands in for it.
			address = standIn
		}
		return listenReserved(network, address)
	}}
	d := runServe(t, writeHome(t, consoleConfig(freeAddress(t))), listen, opts)
	d.log.await(t, "local-and-remote-clients\n", 1, d.exit)
	text := d.log.String()
	base := "http://[fe80::1%25synthetic0]:" + port
	if want := "daemon listening: " + base + "\n  agents connect with: AMG_HOST=" + base + " amg account list\n"; !strings.Contains(text, want) {
		t.Fatalf("log=%q", text)
	}
	if _, err := client.New(base, client.Options{}); err != nil {
		t.Fatalf("amg refuses the hint: %v", err)
	}
}

// Without a daemon, a setting change ends with the command that starts one, for the home that the command used.
func TestChangeWithoutDaemonNamesTheServeCommandOfTheHome(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "home")
	quoted := filepath.Join(t.TempDir(), "it's my home")
	user := t.TempDir()
	t.Setenv("HOME", user)
	for _, tc := range []struct {
		args     []string
		env, dir string
		want     string
	}{
		{[]string{"--home", plain}, "", plain, "amgd serve --home " + plain},
		{nil, quoted, quoted, "amgd serve --home '" + strings.ReplaceAll(quoted, "'", `'\''`) + "'"},
		{nil, "", filepath.Join(user, ".agentmailguard"), "amgd serve"},
	} {
		t.Setenv("AMG_HOME", tc.env)
		if err := os.MkdirAll(tc.dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tc.dir, "config.toml"), []byte(consoleConfig("localhost:8788")), 0600); err != nil {
			t.Fatal(err)
		}
		var errout strings.Builder
		code := daemon.Run(t.Context(), append(tc.args, "config", "set", "policy.mail_send", "approve"), io.Discard, &errout, daemon.Options{Platform: &keychain{}, LookupEnv: noEnv})
		if code != 0 || !strings.HasSuffix(errout.String(), "no daemon runs for this home. Now run: "+tc.want+"\n") {
			t.Errorf("AMG_HOME=%q %v: exit %d stderr %q", tc.env, tc.args, code, errout.String())
		}
	}
}

// consoleConfig is a home without accounts, with fake screening and the console on listen.
func consoleConfig(listen string) string {
	return "version = 1\n[screening]\nprovider = 'fake'\n[credentials]\nbackend = 'keychain'\n[console]\nlisten = '" + listen + "'\n"
}

func TestServeWarnsWhenTheApprovalConsoleListensBeyondLoopback(t *testing.T) {
	t.Parallel()
	d := startServe(t, writeHome(t, consoleConfig(reserve(t, "0.0.0.0"))), "127.0.0.1:0", daemon.Options{Platform: &keychain{}})
	if text := d.log.String(); !strings.Contains(text, "WARNING: the approval console listens beyond loopback. Use an HTTPS tunnel such as tailscale serve.\napproval console: http://") {
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
	home, k := writeHome(t, consoleConfig(taken.Addr().String())), &keychain{}
	setConsolePassword(t, credentials.New(k, home))
	code, _, errout := amgd(t, home, daemon.Options{Platform: k}, "", "serve", "--listen", "127.0.0.1:0")
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
