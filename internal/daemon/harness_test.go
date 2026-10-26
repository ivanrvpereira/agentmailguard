// The tests of package daemon run amgd in-process through daemon.Run, on temporary homes, with a fake Keychain
// platform and fake Microsoft 365 and Gmail HTTP endpoints. They observe only exit codes, stdout and stderr,
// config.toml, audit.jsonl, the fake Keychain item, and the requests that the fake endpoints received.
package daemon_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// productionWorkFactor is the scrypt work factor of credential files before TestMain lowers it.
var productionWorkFactor int

// TestMain keeps scrypt cheap: at the production work factor, the race detector makes each file store
// operation take seconds. TestServeWritesTheCredentialFileWithTheProductionWorkFactor restores it.
func TestMain(m *testing.M) {
	productionWorkFactor = credentials.FileWorkFactor
	credentials.FileWorkFactor = 1
	os.Exit(m.Run())
}

// bound is how long a test waits for a command, a daemon start, or a daemon stop. Parallel race runs are slow.
const bound = 60 * time.Second

// noEnv is an empty environment: no color, no credential password, no systemd socket.
func noEnv(string) string { return "" }

// keychain is the fake macOS Keychain. It holds the one item of the credential store and counts writes.
type keychain struct {
	mu        sync.Mutex
	value     []byte
	writes    int
	failWrite bool
}

func (k *keychain) Read(_ context.Context, service, account string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if service != "agentmailguard" || account != "credentials" {
		return nil, errors.New("synthetic unknown item")
	}
	return bytes.Clone(k.value), nil
}

func (k *keychain) Write(_ context.Context, service, account string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writes++
	if k.failWrite || service != "agentmailguard" || account != "credentials" {
		return errors.New("private-keychain-canary")
	}
	k.value = bytes.Clone(value)
	return nil
}

// item returns the stored item and the number of writes so far.
func (k *keychain) item() ([]byte, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return bytes.Clone(k.value), k.writes
}

// login reads the login of an account back from the fake Keychain.
func (k *keychain) login(t *testing.T, home, provider, email string) credentials.OAuth {
	t.Helper()
	v, err := credentials.New(k, home).LoadOAuth(t.Context(), provider, email)
	if err != nil {
		t.Fatalf("login of %s/%s: %v", provider, email, err)
	}
	return v
}

// clock is an injected Now that tests move forward.
type clock struct{ now atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.now.Store(time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.now.Add(int64(d)) }

// writeHome writes config.toml into a new temporary home and returns the home.
func writeHome(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// exists reports whether the file name exists in home.
func exists(t *testing.T, home, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(home, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// auditEvents returns the records of audit.jsonl whose event is one of events, without their time.
func auditEvents(t *testing.T, home string, events ...string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for line := range strings.Lines(readFile(t, filepath.Join(home, "audit.jsonl"))) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(events, v["event"].(string)) {
			delete(v, "time")
			out = append(out, v)
		}
	}
	return out
}

// amgd runs one amgd command on home with stdin and returns its exit code, stdout, and stderr. A nil
// LookupEnv becomes the empty environment.
func amgd(t *testing.T, home string, opts daemon.Options, stdin string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), bound)
	defer cancel()
	if opts.LookupEnv == nil {
		opts.LookupEnv = noEnv
	}
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader(stdin)
	}
	if opts.Listen == nil {
		opts.Listen = listenReserved
	}
	var out, errout bytes.Buffer
	code := daemon.Run(ctx, append([]string{"--home", home}, args...), &out, &errout, opts)
	return code, out.String(), errout.String()
}

// reserved holds listeners that tests opened for serve, by address. Parallel tests open ports all the time, so a
// port that a test closes and serve opens again later can be taken in between.
var reserved = struct {
	sync.Mutex
	listeners map[string]net.Listener
}{listeners: map[string]net.Listener{}}

// freeAddress returns a loopback address for serve. The port stays open until serve takes its listener through
// listenReserved, so no other test can take it; the test closes a listener that serve did not take.
func freeAddress(t *testing.T) string { return reserve(t, "127.0.0.1") }

// reserve opens a listener on a free port of host and returns its address, host:port.
func reserve(t *testing.T, host string) string {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort(host, port)
	reserved.Lock()
	reserved.listeners[address] = l
	reserved.Unlock()
	t.Cleanup(func() {
		if l := takeReserved(address); l != nil {
			_ = l.Close()
		}
	})
	return address
}

func takeReserved(address string) net.Listener {
	reserved.Lock()
	defer reserved.Unlock()
	l := reserved.listeners[address]
	delete(reserved.listeners, address)
	return l
}

// listenReserved is the Listen seam of serve: it hands over a reserved listener, else it opens the address.
func listenReserved(network, address string) (net.Listener, error) {
	if l := takeReserved(address); l != nil {
		return l, nil
	}
	return net.Listen(network, address)
}

// daemonLog is the stderr of an in-process amgd serve. await waits for text in it.
type daemonLog struct {
	mu      sync.Mutex
	text    strings.Builder
	written chan struct{} // closed and replaced at each write
}

func newDaemonLog() *daemonLog { return &daemonLog{written: make(chan struct{})} }

func (l *daemonLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text.Write(p)
	close(l.written)
	l.written = make(chan struct{})
	return len(p), nil
}

func (l *daemonLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// await waits until the log contains text n times, or until exit receives the exit code of serve.
func (l *daemonLog) await(t *testing.T, text string, n int, exit <-chan int) {
	t.Helper()
	l.awaitAny(t, n, exit, text)
}

// awaitAny waits until the log contains one of texts, all together n times, or until exit receives the exit
// code of serve.
func (l *daemonLog) awaitAny(t *testing.T, n int, exit <-chan int, texts ...string) {
	t.Helper()
	timeout := time.After(bound)
	for {
		l.mu.Lock()
		found := 0
		for _, text := range texts {
			found += strings.Count(l.text.String(), text)
		}
		written := l.written
		l.mu.Unlock()
		if found >= n {
			return
		}
		select {
		case <-written:
		case code := <-exit:
			t.Fatalf("serve exited %d before %q:\n%s", code, texts, l.String())
		case <-timeout:
			t.Fatalf("no %q in the daemon log:\n%s", texts, l.String())
		}
	}
}

// daemonRun is an in-process amgd serve.
type daemonRun struct {
	log    *daemonLog
	cancel context.CancelFunc
	exit   chan int
}

// startServe runs amgd serve on home until the test ends and waits until it listens. The test fails when serve
// does not stop with exit code 0.
func startServe(t *testing.T, home, listen string, opts daemon.Options) *daemonRun {
	t.Helper()
	d := runServe(t, home, listen, opts)
	t.Cleanup(func() {
		if code := d.stop(); code != 0 {
			t.Errorf("serve exit %d:\n%s", code, d.log.String())
		}
	})
	d.log.await(t, "daemon listening\n", 1, d.exit)
	return d
}

// runServe starts amgd serve on home without waiting. It stops serve when the test ends, before the temporary
// home goes.
func runServe(t *testing.T, home, listen string, opts daemon.Options) *daemonRun {
	t.Helper()
	if opts.LookupEnv == nil {
		opts.LookupEnv = noEnv
	}
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader("")
	}
	if opts.Listen == nil {
		opts.Listen = listenReserved
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemonRun{log: newDaemonLog(), cancel: cancel, exit: make(chan int, 1)}
	go func() {
		d.exit <- daemon.Run(ctx, []string{"--home", home, "serve", "--listen", listen}, io.Discard, d.log, opts)
	}()
	t.Cleanup(func() { d.stop() })
	return d
}

// stop cancels serve and returns its exit code; -1 when it did not stop in time.
func (d *daemonRun) stop() int {
	d.cancel()
	select {
	case code := <-d.exit:
		d.exit <- code // a later stop returns the same code
		return code
	case <-time.After(bound):
		return -1
	}
}

// browser is the fake web browser of a sign-in: it approves the request and opens the redirect address with
// an authorization code, as the provider would.
func browser(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	q := u.Query()
	values := url.Values{"state": {q.Get("state")}, "code": {"private-authorization-canary"}}
	req, err := http.NewRequestWithContext(ctx, "GET", q.Get("redirect_uri")+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// identityProvider is a fake Microsoft 365 or Gmail endpoint. Each sign-in answers as user. It names the
// refresh token of sign-in n synthetic-refresh-login-n and the one of refresh n synthetic-refresh-renewed-n,
// and records the refresh tokens that it receives.
type identityProvider struct {
	*httptest.Server
	mu        sync.Mutex
	user      string
	logins    int
	refreshed []string
	profiles  int
}

func (p *identityProvider) signInAs(user string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user = user
}

// refreshTokens returns the refresh tokens that the token endpoint received.
func (p *identityProvider) refreshTokens() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.refreshed)
}

// grant answers a token request with the access token and the refresh token of the grant.
func (p *identityProvider) grant(r *http.Request) (access, refresh string, ok bool) {
	if r.Method != "POST" || r.ParseForm() != nil {
		return "", "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p.logins++
		return fmt.Sprintf("login-%d", p.logins), fmt.Sprintf("synthetic-refresh-login-%d", p.logins), true
	case "refresh_token":
		p.refreshed = append(p.refreshed, r.PostForm.Get("refresh_token"))
		return fmt.Sprintf("renewed-%d", len(p.refreshed)), fmt.Sprintf("synthetic-refresh-renewed-%d", len(p.refreshed)), true
	}
	return "", "", false
}

func (p *identityProvider) profile() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.profiles++
	return p.user
}

// m365Scopes are the delegated permissions that amgd requests from Microsoft 365.
const m365Scopes = "User.Read Mail.ReadWrite Mail.Send Calendars.Read Calendars.ReadWrite MailboxSettings.Read"

// m365Token is a synthetic Microsoft access token: a JWT whose payload has the scp claim.
func m365Token(name, scope string) string {
	claims, _ := json.Marshal(map[string]string{"scp": scope})
	return "synthetic-" + name + "." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
}

// newM365 starts a fake Microsoft identity platform and Graph: /token and /me.
func newM365(t *testing.T) *identityProvider {
	t.Helper()
	p := &identityProvider{user: "ana@example.test"}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			access, refresh, ok := p.grant(r)
			if !ok {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": m365Token(access, m365Scopes), "refresh_token": refresh, "expires_in": 3600})
		case "/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"userPrincipalName": p.profile()})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

// m365Options points amgd at the fake endpoint, with the fake browser and the clock.
func (p *identityProvider) m365Options(c *clock) m365.Options {
	return m365.Options{AuthorityURL: p.URL, BaseURL: p.URL, OpenBrowser: browser, Now: c.Now}
}

// gmailScopes are the scopes that amgd requests from Google.
const gmailScopes = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned"

// newGmail starts a fake Google OAuth and Gmail profile endpoint: /token and /profile.
func newGmail(t *testing.T) *identityProvider {
	t.Helper()
	p := &identityProvider{user: "ana@example.test"}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			access, refresh, ok := p.grant(r)
			if !ok {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-" + access, "refresh_token": refresh, "expires_in": 3600, "scope": gmailScopes})
		case "/profile":
			_ = json.NewEncoder(w).Encode(map[string]string{"emailAddress": p.profile()})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *identityProvider) gmailOptions(c *clock) gmail.Options {
	return gmail.Options{AuthURL: p.URL + "/authorize", TokenURL: p.URL + "/token", ProfileURL: p.URL + "/profile", OpenBrowser: browser, Now: c.Now}
}

// providerOptions are daemon options with the fake Keychain and both fake providers.
func providerOptions(k *keychain, ms, g *identityProvider, c *clock) daemon.Options {
	opts := daemon.Options{Platform: k, Now: c.Now, Server: server.Options{Now: c.Now}}
	if ms != nil {
		opts.Server.M365 = ms.m365Options(c)
	}
	if g != nil {
		opts.Server.Gmail = g.gmailOptions(c)
	}
	return opts
}
