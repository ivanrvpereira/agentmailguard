package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/client"
)

// A login that amgd account login saves while the daemon runs survives the daemon's next token refresh: the
// daemon refreshes the login it loaded, and the next load uses the newer login, not the refreshed old one.
func TestLoginWhileTheDaemonRunsSurvivesItsTokenRefresh(t *testing.T) {
	t.Parallel()
	api, console := freeAddress(t), freeAddress(t)
	home, k, c, ms := writeHome(t, accountConfig+"[console]\nlisten = '"+console+"'\n"), &keychain{}, newClock(), newM365(t)
	opts := providerOptions(k, ms, nil, c)
	if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["m365"]...)...); code != 0 {
		t.Fatalf("add: exit %d stderr %q", code, errout)
	}

	reloads := make(chan os.Signal, 1)
	serveOpts := opts
	serveOpts.Reloads = reloads
	d := startServe(t, home, api, serveOpts)

	// The login asks the daemon to reload, but the reload has not run yet.
	var notified []int
	loginOpts := opts
	loginOpts.Notify = func(pid int) error { notified = append(notified, pid); return nil }
	if code, _, errout := amgd(t, home, loginOpts, "", "account", "login", "work"); code != 0 || !reflect.DeepEqual(notified, []int{os.Getpid()}) {
		t.Fatalf("login: exit %d notified %v stderr %q", code, notified, errout)
	}

	// Both access tokens expire; an API call makes the daemon refresh the login that it loaded.
	c.advance(2 * time.Hour)
	accountStatus(t, api, "work")
	if got := ms.refreshTokens(); !reflect.DeepEqual(got, []string{"synthetic-refresh-login-1"}) {
		t.Fatalf("refresh tokens before the reload: %v", got)
	}

	// The reload reads the store again and refreshes the newer login, the one of amgd account login.
	reloads <- syscall.SIGUSR1
	d.log.await(t, "reload ok\n", 1, d.exit)
	if got := ms.refreshTokens(); !reflect.DeepEqual(got, []string{"synthetic-refresh-login-1", "synthetic-refresh-login-2"}) {
		t.Fatalf("refresh tokens after the reload: %v; want the refresh token of the newer login", got)
	}
	if log := d.log.String(); !strings.Contains(log[strings.LastIndex(log, "reload requested\n"):], "credential check: account \"work\" (m365): ok\n") {
		t.Fatalf("daemon log:\n%s", log)
	}
}

// accountStatus calls account.status of the account on the daemon at api and fails unless the sign-in works.
func accountStatus(t *testing.T, api, account string) {
	t.Helper()
	call, err := client.New("http://"+api, client.Options{Timeout: bound})
	if err != nil {
		t.Fatal(err)
	}
	out, err := call.Call(t.Context(), "account.status", account, nil)
	var status struct {
		OK bool `json:"ok"`
	}
	if err != nil || json.Unmarshal(out, &status) != nil || !status.OK {
		t.Fatalf("account.status: %s %v", out, err)
	}
}

// When the reload after amgd account login is refused, the daemon keeps the state that it loaded before the
// login. Its refreshes still never overwrite the newer login: after a lost save it uses the stored login, so the
// next refresh uses the refresh token of the newer login.
func TestGmailRefreshesAfterARefusedReloadKeepTheNewerLogin(t *testing.T) {
	t.Parallel()
	api, console := freeAddress(t), freeAddress(t)
	home, k, c, g := writeHome(t, accountConfig+"[console]\nlisten = '"+console+"'\n"), &keychain{}, newClock(), newGmail(t)
	opts := providerOptions(k, nil, g, c)
	// A Gmail sign-in refreshes once: account add sends synthetic-refresh-login-1, account login sends -login-2.
	if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["gmail"]...)...); code != 0 {
		t.Fatalf("add: exit %d stderr %q", code, errout)
	}
	reloads := make(chan os.Signal, 1)
	serveOpts := opts
	serveOpts.Reloads = reloads
	d := startServe(t, home, api, serveOpts)
	loginOpts := opts
	loginOpts.Notify = func(int) error { return nil }
	if code, _, errout := amgd(t, home, loginOpts, "", "account", "login", "work"); code != 0 {
		t.Fatalf("login: exit %d stderr %q", code, errout)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(readFile(t, path)+"\nunknown_key = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reloads <- syscall.SIGUSR1
	d.log.await(t, "reload refused", 1, d.exit)

	// The first refresh uses the login that the daemon loaded (renewed-1) and loses the save to the newer login
	// (renewed-2). The second refresh must use the newer login.
	for range 2 {
		c.advance(2 * time.Hour)
		accountStatus(t, api, "work")
	}
	want := []string{"synthetic-refresh-login-1", "synthetic-refresh-login-2", "synthetic-refresh-renewed-1", "synthetic-refresh-renewed-2"}
	if got := g.refreshTokens(); !reflect.DeepEqual(got, want) {
		t.Fatalf("refresh tokens %v; want %v", got, want)
	}
}
