package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// consoleGet requests the approval console list with a password, as a browser would, and returns the status
// and the body.
func (s *server) consoleGet(password string) (int, string) {
	s.t.Helper()
	r, err := http.NewRequest("GET", "http://"+s.console+"/", nil)
	if err != nil {
		s.t.Fatal(err)
	}
	if password != "" {
		r.SetBasicAuth("me", password)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// auditEvents returns the records of audit.jsonl whose event is one of events, without their time.
func (s *server) auditEvents(events ...string) []map[string]any {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join(s.home, "audit.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	out := []map[string]any{}
	for line := range strings.Lines(string(data)) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			s.t.Fatal(err)
		}
		if slices.Contains(events, v["event"].(string)) {
			delete(v, "time")
			out = append(out, v)
		}
	}
	return out
}

func (s *server) config() string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.home, "config.toml"))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func TestSecondDaemonForAHomeFails(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	_, errout, code := s.amgd(nil, "serve", "--listen", freeAddress(t))
	if code != 1 || !strings.HasSuffix(errout, "error: another daemon runs for this home (PID "+strconv.Itoa(s.pid)+")\n") {
		t.Fatalf("second daemon: exit %d stderr %q", code, errout)
	}
	// The running daemon still answers.
	s.ok("stats")
}

func TestConfigSetReloadsTheDaemonAndIsAudited(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	s.refused("refused by policy: mail_send", "mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-a", "--body", "canary-body-a")
	before := s.reloads()
	out, errout := s.amgdOK(nil, "config", "set", "policy.mail_send", "approve")
	if out != "" || errout != "reload sent to daemon (PID "+strconv.Itoa(s.pid)+"); see the daemon log\n" {
		t.Fatalf("config set: stdout %q stderr %q", out, errout)
	}
	if got := s.awaitReload(before); got != "reload ok" {
		t.Fatalf("reload: %s", got)
	}
	s.held("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-b", "--body", "canary-body-b")
	// The log shows the request, the check list, and the result, as at a start.
	log := s.logText()
	tail := log[strings.LastIndex(log, "reload requested\n"):]
	for _, want := range []string{"reload requested\n", "credential check: credentials (file): ok\n", "credential check: account \"demo\" (fake): no login needed\n", "credential check: console password: ok\n", "reload ok\n"} {
		if !strings.Contains(tail, want) {
			t.Errorf("reload log lacks %q:\n%s", want, tail)
		}
	}
	if !strings.HasPrefix(s.config(), "# Written by amgd. Comments are not kept.\n") {
		t.Fatalf("config.toml:\n%s", s.config())
	}
	s.amgdOK(nil, "config", "set", "screening.endpoint", "https://jev.example.test/v1")
	s.amgdOK(nil, "config", "unset", "screening.endpoint")
	want := []map[string]any{
		{"event": "config_changed", "key": "policy.mail_send", "value": "approve"},
		{"event": "config_changed", "key": "screening.endpoint"},
		{"event": "config_removed", "key": "screening.endpoint"},
	}
	if got := s.auditEvents("config_changed", "config_removed"); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit: %v", got)
	}
	if got := s.auditEvents("reload"); len(got) == 0 || !reflect.DeepEqual(got[0], map[string]any{"event": "reload", "result": "ok"}) {
		t.Fatalf("reload audit: %v", got)
	}
}

func TestConfigSetRefusesBadValuesAndLeavesTheFile(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	original := s.config()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"policy.mail_send", "always"}, "error: config.toml: policy.mail_send: must be allow, approve, or deny\n"},
		{[]string{"policy.mail_sned", "allow"}, "error: unknown key policy.mail_sned\n"},
		{[]string{"accounts.demo.email", "other@fake.invalid"}, "error: accounts.demo.email changes only with amgd account remove and amgd account add\n"},
		{[]string{"accounts.demo.provider", "m365"}, "error: accounts.demo.provider changes only with amgd account remove and amgd account add\n"},
		{[]string{"screening.api_key", "canary-key"}, "error: screening.api_key is a secret; give the value on stdin, never as an argument\n"},
	} {
		out, errout, code := s.amgd(nil, append([]string{"config", "set"}, tc.args...)...)
		if code != 1 || out != "" || errout != tc.want {
			t.Errorf("config set %v: exit %d stdout %q stderr %q", tc.args, code, out, errout)
		}
	}
	if s.config() != original {
		t.Fatalf("config.toml changed:\n%s", s.config())
	}
}

func TestInvalidHandEditIsRefusedAndTheOldSettingsStay(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	broken := strings.Replace(s.config(), "mail_send='approve'", "mail_send='always'", 1)
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"), []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if got := s.reload(); got != "reload refused: config_invalid; old settings kept" {
		t.Fatalf("reload: %s", got)
	}
	if !strings.Contains(s.logText(), "error: config.toml: policy.mail_send: must be allow, approve, or deny\n") {
		t.Fatalf("log lacks the detail:\n%s", s.logText())
	}
	s.held("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-x", "--body", "canary-body-x")
	if _, errout, code := s.amgd(nil, "config", "check"); code != 1 || errout != "error: config.toml: policy.mail_send: must be allow, approve, or deny\n" {
		t.Fatalf("config check: exit %d stderr %q", code, errout)
	}
	if got := s.auditEvents("reload"); !reflect.DeepEqual(got, []map[string]any{{"event": "reload", "result": "config_invalid"}}) {
		t.Fatalf("audit: %v", got)
	}
}

func TestRestartKeysWaitForARestartAndOtherChangesApply(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	before, other := s.reloads(), freeAddress(t)
	if _, errout := s.amgdOK(nil, "config", "set", "console.listen", other); errout != "restart needed: console.listen changed\n" {
		t.Fatalf("stderr %q", errout)
	}
	if s.reloads() != before {
		t.Fatal("a restart key reloaded the daemon")
	}
	s.amgdOK(nil, "config", "set", "policy.mail_send", "approve")
	if got := s.awaitReload(before); got != "reload ok" {
		t.Fatalf("reload: %s", got)
	}
	if !strings.Contains(s.logText(), "restart needed: console.listen changed\nreload ok\n") {
		t.Fatalf("log:\n%s", s.logText())
	}
	// The policy change applies; the console stays at its old address until the restart.
	p := s.held("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-r", "--body", "canary-body-r")
	if code, _ := s.consoleGet(consolePassword); code != 200 {
		t.Fatalf("console at the old address: HTTP %d", code)
	}
	s.console = other
	s.restart()
	if !strings.Contains(p.URL, "/a/"+p.ApprovalID) {
		t.Fatalf("url %s", p.URL)
	}
	s.approve(p.ApprovalID)
}

func TestSignalsDuringAReloadMergeIntoOneMoreReload(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	before := s.reloads()
	for range 20 {
		if err := syscall.Kill(s.pid, syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
	}
	s.awaitReload(before)
	// Wait until no more reloads come.
	for last := -1; last != s.reloads(); {
		last = s.reloads()
		time.Sleep(3 * time.Second)
	}
	if n := s.reloads() - before; n < 1 || n > 2 {
		t.Fatalf("%d reloads for 20 signals:\n%s", n, s.logText())
	}
	s.ok("stats")
}

func TestConsoleIsOnWithoutAPasswordAndANewPasswordTakesEffectAtTheReload(t *testing.T) {
	t.Parallel()
	s := home(t, "")
	s.seed()
	s.serve()
	if code, body := s.consoleGet("anything"); code != 503 || body != "console password not set; run amgd config set console.password\n" {
		t.Fatalf("console without password: HTTP %d %q", code, body)
	}
	before := s.reloads()
	s.amgdOK(strings.NewReader(consolePassword+"\n"), "config", "set", "console.password")
	s.awaitReload(before)
	if code, _ := s.consoleGet(consolePassword); code != 200 {
		t.Fatalf("console with the new password: HTTP %d", code)
	}
	before = s.reloads()
	s.amgdOK(strings.NewReader("e2e-second-password\n"), "config", "set", "console.password")
	s.awaitReload(before)
	if code, _ := s.consoleGet(consolePassword); code != 401 {
		t.Fatalf("old password after the change: HTTP %d", code)
	}
	if code, _ := s.consoleGet("e2e-second-password"); code != 200 {
		t.Fatalf("new password: HTTP %d", code)
	}
	if got := s.auditEvents("console_password_set"); len(got) != 2 {
		t.Fatalf("audit: %v", got)
	}
	if strings.Contains(s.config(), "password") {
		t.Fatalf("config.toml holds the password:\n%s", s.config())
	}
}

func TestFakeAccountsAreAddedListedAndRemoved(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	before := s.reloads()
	out, _ := s.amgdOK(nil, "account", "add", "second", "--provider", "fake")
	if got := decode[struct{ Account, Provider, Email string }](t, out); got.Account != "second" || got.Email != "second@fake.invalid" {
		t.Fatalf("add: %s", out)
	}
	s.awaitReload(before)
	type listed struct {
		Account, Provider, Email string
		Default                  bool
	}
	want := []listed{{"demo", "fake", demo, true}, {"second", "fake", "second@fake.invalid", false}}
	if got := decode[[]listed](t, s.ok("account", "list")); !reflect.DeepEqual(got, want) {
		t.Fatalf("list: %+v", got)
	}
	status := decode[struct {
		Account, Email string
		LoggedIn       bool `json:"logged_in"`
	}](t, s.ok("--account", "second", "account", "status"))
	if status.Account != "second" || status.Email != "second@fake.invalid" || !status.LoggedIn {
		t.Fatalf("status: %+v", status)
	}
	// An alias for an email that already has an account is refused.
	if _, errout, code := s.amgd(nil, "account", "add", "SECOND", "--provider", "fake"); code != 1 || errout != "error: second@fake.invalid is already account second\n" {
		t.Fatalf("duplicate: exit %d stderr %q", code, errout)
	}
	// The default account cannot go while another exists.
	if _, errout, code := s.amgd(nil, "account", "remove", "demo", "--yes"); code != 1 || !strings.Contains(errout, "demo is the default account") {
		t.Fatalf("remove default: exit %d stderr %q", code, errout)
	}
	// A pending send of the removed account is denied and never runs.
	p := s.held("--account", "second", "mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-gone", "--body", "canary-body-gone")
	if _, errout, code := s.amgd(nil, "account", "remove", "second"); code != 1 || errout != "error: confirmation needs a terminal; run again with --yes\n" {
		t.Fatalf("remove without confirmation: exit %d stderr %q", code, errout)
	}
	before = s.reloads()
	_, errout := s.amgdOK(nil, "account", "remove", "second", "--yes")
	if !strings.Contains(errout, "Removed account second (second@fake.invalid). 1 pending actions denied.") {
		t.Fatalf("remove: %q", errout)
	}
	s.awaitReload(before)
	if state := s.state(p.ApprovalID); state != "denied" {
		t.Fatalf("pending after remove: %s", state)
	}
	if code := s.decide(p.ApprovalID, "approve"); code != http.StatusConflict {
		t.Fatalf("approve after remove: HTTP %d", code)
	}
	s.refused("unknown account; run amg account list", "--account", "second", "mail", "list")
	if strings.Contains(s.config(), "second") {
		t.Fatalf("config.toml keeps the account:\n%s", s.config())
	}
	wantAudit := []map[string]any{
		{"event": "account_added", "account": "second@fake.invalid"},
		{"event": "approval", "account": "second@fake.invalid", "operation": "mail.send", "state": "created", "approval_id": p.ApprovalID},
		{"event": "approval", "account": "second@fake.invalid", "operation": "mail.send", "state": "denied", "approval_id": p.ApprovalID},
		{"event": "account_removed", "account": "second@fake.invalid"},
	}
	var got []map[string]any
	for _, r := range s.auditEvents("account_added", "account_removed", "approval") {
		if r["account"] == "second@fake.invalid" {
			got = append(got, r)
		}
	}
	if !reflect.DeepEqual(got, wantAudit) {
		t.Fatalf("audit:\n%v\nwant\n%v", got, wantAudit)
	}
}

func TestAccountAddCreatesTheConfigurationOfANewHome(t *testing.T) {
	t.Parallel()
	s := &server{t: t, home: filepath.Join(t.TempDir(), "new"), clientHome: t.TempDir()}
	s.amgdOK(nil, "account", "add", "demo", "--provider", "fake")
	want := "# Written by amgd. Comments are not kept.\ndefault_account = \"demo\"\nversion = 1\n\n[accounts]\n  [accounts.demo]\n    email = \"demo@fake.invalid\"\n    provider = \"fake\"\n"
	if got := s.config(); got != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", got, want)
	}
	// Rules already written for the email apply to the new account, and the command says so.
	if err := os.WriteFile(filepath.Join(s.home, "rules.toml"), []byte("[accounts.\"other@fake.invalid\".bills]\nfolder = 'bills'\ninstructions = 'canary-rule: bills.'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, errout := s.amgdOK(nil, "account", "add", "other", "--provider", "fake")
	if !strings.Contains(errout, "rules.toml already has 1 rules for other@fake.invalid; they apply to other.\n") || !strings.Contains(errout, "no daemon runs for this home") {
		t.Fatalf("stderr %q", errout)
	}
	if !strings.Contains(s.config(), "default_account = \"demo\"") {
		t.Fatalf("the first account is not the default:\n%s", s.config())
	}
}

func TestRulesOfAnUnknownEmailAreIgnoredAndCheckWarns(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	if err := os.WriteFile(filepath.Join(s.home, "rules.toml"), []byte("[accounts.\"gone@fake.invalid\".x]\nfolder = 'f'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := s.reload(); got != "reload ok" {
		t.Fatalf("reload: %s", got)
	}
	out, errout, code := s.amgd(nil, "config", "check")
	if code != 0 || out != "config.toml and rules.toml are valid\n" || errout != "warning: rules.toml has rules for gone@fake.invalid, which is no account; the daemon ignores them\n" {
		t.Fatalf("check: exit %d stdout %q stderr %q", code, out, errout)
	}
}

func TestConfigListShowsValuesDefaultsAndOnlyTheStateOfSecrets(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	out, _ := s.amgdOK(nil, "config", "list")
	for _, want := range []string{"policy.mail_send = approve\n", "policy.mail_trash = allow (default)\n", "screening.endpoint = https://openrouter.ai/api/v1/systemone (default)\n", "screening.model = typesafe/jev-1.13 (default)\n", "console.password = set\n", "screening.api_key = not set\n", "accounts.demo.email = demo@fake.invalid\n", "accounts.demo.policy.mail_send = approve (default)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, consolePassword) {
		t.Fatal("list shows the console password")
	}
	if out, _ := s.amgdOK(nil, "config", "get", "policy.mail_send"); out != "approve\n" {
		t.Fatalf("get: %q", out)
	}
	if out, _ := s.amgdOK(nil, "config", "get", "console.password"); out != "set\n" {
		t.Fatalf("get secret: %q", out)
	}
	if out, _ := s.amgdOK(nil, "config", "path"); out != filepath.Join(s.home, "config.toml")+"\n" {
		t.Fatalf("path: %q", out)
	}
}

func TestStoreCommandsRefuseWhileTheDaemonRuns(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	for _, args := range [][]string{{"store", "migrate", "--to", "keychain"}, {"store", "change-password"}} {
		if _, errout, code := s.amgd(nil, args...); code != 1 || errout != "error: a daemon runs for this home; stop it first\n" {
			t.Errorf("%v: exit %d stderr %q", args, code, errout)
		}
	}
}

func TestAgentClientReachesTheDaemonByHostOptionAndHasNoManagementCommands(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	if out, errout, code := s.amgEnv(nil, "-H", "http://"+s.api, "account", "list"); code != 0 || errout != "" || !strings.Contains(out, demo) {
		t.Fatalf("-H: exit %d stdout %q stderr %q", code, out, errout)
	}
	for _, args := range [][]string{{"serve"}, {"reload"}, {"config", "list"}, {"cache", "clear"}, {"account", "add", "x", "--provider", "fake"}} {
		if _, _, code := s.amg(args...); code != 2 {
			t.Errorf("amg %v: exit %d", args, code)
		}
	}
	if out, _ := s.amgdOK(nil, "cache", "clear"); !strings.Contains(out, `"deleted"`) {
		t.Fatalf("amgd cache clear: %q", out)
	}
}
