package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
)

// signInArgs are the account add flags of each provider that signs in.
var signInArgs = map[string][]string{
	"m365":  {"--provider", "m365", "--client-id", "synthetic-client", "--tenant-id", "synthetic-tenant"},
	"gmail": {"--provider", "gmail", "--client-id", "synthetic-client", "--client-secret", "synthetic-secret"},
}

// otherProvider is the provider that the refusal tests add the same email with.
var otherProvider = map[string]string{"m365": "gmail", "gmail": "m365"}

const accountConfig = "version = 1\n[credentials]\nbackend = 'keychain'\n[screening]\nprovider = 'fake'\n"

func TestAccountAddTakesTheLowercaseEmailOfTheSignInAndRefusesAnEmailThatHasAnAccount(t *testing.T) {
	for _, provider := range []string{"m365", "gmail"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			home, k, c := writeHome(t, accountConfig), &keychain{}, newClock()
			ms, g := newM365(t), newGmail(t)
			opts := providerOptions(k, ms, g, c)
			ms.signInAs("Ana@Example.TEST")
			g.signInAs("Ana@Example.TEST")

			code, out, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs[provider]...)...)
			if code != 0 || !strings.Contains(errout, "Added account work: ana@example.test ("+provider+")\n") {
				t.Fatalf("add: exit %d stderr %q", code, errout)
			}
			var result struct{ Account, Provider, Email string }
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Account != "work" || result.Provider != provider || result.Email != "ana@example.test" {
				t.Fatalf("add stdout %q", out)
			}
			cfg, err := config.Load(t.Context(), filepath.Join(home, "config.toml"))
			if a, ok := cfg.Account("work"); err != nil || !ok || a.Email != "ana@example.test" || a.Provider != provider || cfg.DefaultAccount != "work" {
				t.Fatalf("config.toml:\n%s", readFile(t, filepath.Join(home, "config.toml")))
			}
			if got := auditEvents(t, home, "account_added"); !reflect.DeepEqual(got, []map[string]any{{"event": "account_added", "account": "ana@example.test"}}) {
				t.Fatalf("audit: %v", got)
			}
			if login := k.login(t, home, provider, "ana@example.test"); login.RefreshToken == "" {
				t.Fatal("the login of the new account is not saved")
			}

			// The same email, in other case and through the other provider, is refused, and nothing is saved.
			configBefore := readFile(t, filepath.Join(home, "config.toml"))
			itemBefore, writesBefore := k.item()
			ms.signInAs("ANA@example.test")
			g.signInAs("ANA@example.test")
			code, out, errout = amgd(t, home, opts, "", append([]string{"account", "add", "second"}, signInArgs[otherProvider[provider]]...)...)
			if code != 1 || out != "" || !strings.HasSuffix(errout, "error: ana@example.test is already account work\n") {
				t.Fatalf("second add: exit %d stdout %q stderr %q", code, out, errout)
			}
			if readFile(t, filepath.Join(home, "config.toml")) != configBefore {
				t.Fatalf("config.toml changed:\n%s", readFile(t, filepath.Join(home, "config.toml")))
			}
			if item, writes := k.item(); string(item) != string(itemBefore) || writes != writesBefore {
				t.Fatal("the refused sign-in was saved in the credential store")
			}
			if got := auditEvents(t, home, "account_added"); len(got) != 1 {
				t.Fatalf("audit: %v", got)
			}
		})
	}
}

func TestAccountLoginRefusesAnotherEmailAndSavesTheSameEmail(t *testing.T) {
	for _, tc := range []struct{ provider, refresh string }{
		// m365 saves the refresh token of the sign-in; Gmail refreshes once at the sign-in and saves that one.
		{"m365", "synthetic-refresh-login-3"},
		{"gmail", "synthetic-refresh-renewed-3"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			home, k, c := writeHome(t, accountConfig), &keychain{}, newClock()
			ms, g := newM365(t), newGmail(t)
			opts := providerOptions(k, ms, g, c)
			if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs[tc.provider]...)...); code != 0 {
				t.Fatalf("add: exit %d stderr %q", code, errout)
			}
			itemBefore, writesBefore := k.item()

			ms.signInAs("bea@example.test")
			g.signInAs("bea@example.test")
			code, out, errout := amgd(t, home, opts, "", "account", "login", "work")
			want := "error: You signed in as bea@example.test, but work is ana@example.test. Nothing was saved. To add bea@example.test, run amgd account add.\n"
			if code != 1 || out != "" || !strings.HasSuffix(errout, want) {
				t.Fatalf("other email: exit %d stdout %q stderr %q", code, out, errout)
			}
			if item, writes := k.item(); string(item) != string(itemBefore) || writes != writesBefore {
				t.Fatal("the refused sign-in was saved in the credential store")
			}
			if got := auditEvents(t, home, "account_login"); len(got) != 0 {
				t.Fatalf("audit of a refused login: %v", got)
			}

			ms.signInAs("ANA@example.test")
			g.signInAs("ANA@example.test")
			code, out, errout = amgd(t, home, opts, "", "account", "login", "work")
			if code != 0 || !strings.Contains(errout, "Signed in as ana@example.test\n") || !strings.Contains(out, `"email": "ana@example.test"`) {
				t.Fatalf("same email: exit %d stdout %q stderr %q", code, out, errout)
			}
			if login := k.login(t, home, tc.provider, "ana@example.test"); login.RefreshToken != tc.refresh {
				t.Fatalf("saved login has refresh token %q, want %q", login.RefreshToken, tc.refresh)
			}
			if got := auditEvents(t, home, "account_login"); !reflect.DeepEqual(got, []map[string]any{{"event": "account_login", "account": "ana@example.test"}}) {
				t.Fatalf("audit: %v", got)
			}
		})
	}
}

// The login of a new account is saved only once config.toml has the account: a failed edit leaves no login for
// an email that no account has.
func TestAccountAddSavesTheLoginOnlyAfterConfigHasTheAccount(t *testing.T) {
	t.Parallel()
	home, k, c := writeHome(t, accountConfig), &keychain{}, newClock()
	opts := providerOptions(k, newM365(t), nil, c)
	// config.toml cannot be edited: its lock file is a directory.
	if err := os.Mkdir(filepath.Join(home, "config.toml.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	code, out, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["m365"]...)...)
	if code != 1 || out != "" || !strings.HasSuffix(errout, "error: config.toml: cannot open the lock file\n") {
		t.Fatalf("add: exit %d stdout %q stderr %q", code, out, errout)
	}
	if item, writes := k.item(); item != nil || writes != 0 {
		t.Fatal("a login was saved for an account that config.toml does not have")
	}
}

// When the login cannot be saved after config.toml has the account, the command says so and how to sign in again.
func TestAccountAddThatCannotSaveTheLoginPointsToAccountLogin(t *testing.T) {
	t.Parallel()
	home, k, c := writeHome(t, accountConfig), &keychain{failWrite: true}, newClock()
	opts := providerOptions(k, newM365(t), nil, c)
	code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["m365"]...)...)
	if code != 1 || !strings.Contains(errout, "error: account work is added, but its login could not be saved; run amgd account login work\n") {
		t.Fatalf("add: exit %d stderr %q", code, errout)
	}
	if cfg, err := config.Load(t.Context(), filepath.Join(home, "config.toml")); err != nil || len(cfg.Accounts) != 1 {
		t.Fatalf("config.toml:\n%s", readFile(t, filepath.Join(home, "config.toml")))
	}
	if got := auditEvents(t, home, "account_added"); !reflect.DeepEqual(got, []map[string]any{{"event": "account_added", "account": "ana@example.test"}}) {
		t.Fatalf("audit: %v", got)
	}
}

// paster is the terminal of a user who signs in on another computer: it watches stderr for the sign-in URL and,
// at each Address prompt, pastes the next address that paste makes from that URL.
type paster struct {
	mu     sync.Mutex
	text   strings.Builder
	stdin  *io.PipeWriter
	paste  func(n int, authorize *url.URL) string
	pasted int
}

func (p *paster) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text.Write(b)
	if strings.Count(p.text.String(), "Address:") > p.pasted {
		p.pasted++
		var authorize *url.URL
		for line := range strings.Lines(p.text.String()) {
			if u, err := url.Parse(strings.TrimSpace(line)); err == nil && u.Query().Has("redirect_uri") {
				authorize = u
			}
		}
		go func(line string) { _, _ = io.WriteString(p.stdin, line+"\n") }(p.paste(p.pasted, authorize))
	}
	return len(b), nil
}

// With --no-browser, amgd never opens a browser, refuses an address of another sign-in, and saves the login of
// the pasted redirect after the email check.
func TestAccountLoginWithoutBrowserTakesThePastedRedirect(t *testing.T) {
	for _, tc := range []struct{ provider, host string }{{"m365", "localhost"}, {"gmail", "127.0.0.1"}} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			home, k, c := writeHome(t, accountConfig), &keychain{}, newClock()
			opts := providerOptions(k, newM365(t), newGmail(t), c)
			if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs[tc.provider]...)...); code != 0 {
				t.Fatalf("add: exit %d stderr %q", code, errout)
			}
			opts.Server.M365.OpenBrowser = func(context.Context, string) error { t.Error("--no-browser opened a browser"); return nil }
			opts.Server.Gmail.OpenBrowser = opts.Server.M365.OpenBrowser
			stdin, writer := io.Pipe()
			defer writer.Close()
			opts.Stdin = stdin
			terminal := &paster{stdin: writer, paste: func(n int, authorize *url.URL) string {
				q := authorize.Query()
				redirect := strings.TrimSuffix(q.Get("redirect_uri"), "/")
				if !strings.HasPrefix(redirect, "http://"+tc.host+":") {
					t.Errorf("redirect URI %q", redirect)
				}
				if n == 1 {
					return redirect + "/?state=synthetic-other&code=synthetic-code"
				}
				return redirect + "/?state=" + url.QueryEscape(q.Get("state")) + "&code=synthetic-code"
			}}
			ctx, cancel := context.WithTimeout(t.Context(), bound)
			defer cancel()
			var out bytes.Buffer
			code := daemon.Run(ctx, []string{"--home", home, "account", "login", "work", "--no-browser"}, &out, terminal, opts)
			terminal.mu.Lock()
			errout := terminal.text.String()
			terminal.mu.Unlock()
			want := "2. The browser then fails to load a " + tc.host + " page. That is expected.\n3. Copy the full address from the address bar and paste it here.\n\nAddress: ! That address is not from this sign-in. Paste the full address again.\nAddress: "
			if code != 0 || !strings.Contains(errout, want) || !strings.Contains(errout, "Signed in as ana@example.test\n") || strings.Contains(errout+out.String(), "synthetic-code") {
				t.Fatalf("exit %d stderr %q", code, errout)
			}
			if login := k.login(t, home, tc.provider, "ana@example.test"); login.RefreshToken == "" {
				t.Fatal("the pasted sign-in was not saved")
			}
		})
	}
}
