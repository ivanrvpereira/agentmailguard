// Package e2e drives the real binaries: one process runs amgd serve on a temporary home with a fake account and
// fake screening, amg talks to it as a separate process, as an agent would, and amgd commands manage it as the
// operator would. The tests observe only what a user, an agent, or the mail provider can see: command output and
// exit codes, the approval console, the rows of fake-provider.db, the daemon log, config.toml, and audit.jsonl.
// The check for leaked mail content also reads cache.db.
package e2e_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	filePassword    = "e2e-file-password"
	consolePassword = "e2e-console-password"
	// processTimeout bounds one serve start, serve stop, or CLI call. Race builds on slow CI machines need
	// several seconds for the password-based key derivation at start.
	processTimeout = 60 * time.Second
)

// amgBinary and amgdBinary are the binaries that TestMain builds once for every test. When the tests run with
// the race detector, the binaries have it too (buildFlags), so a data race in serve or a command fails the test.
var amgBinary, amgdBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentmailguard-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	amgBinary, amgdBinary = filepath.Join(dir, "amg"), filepath.Join(dir, "amgd")
	for binary, pkg := range map[string]string{amgBinary: "./cmd/amg", amgdBinary: "./cmd/amgd"} {
		build := exec.Command("go", append(append([]string{"build"}, buildFlags...), "-o", binary, pkg)...)
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// server is the home of one daemon, the daemon process while it runs, and the client side of it.
type server struct {
	t                *testing.T
	home, clientHome string
	api, console     string
	pid              int
	stop             func()
	mu               sync.Mutex
	log              bytes.Buffer
}

// demo is the key of the fake account demo: the email that amgd account add gives a fake account.
const demo = "demo@fake.invalid"

// start writes a home with the fake account demo, fake screening, the file credential backend, a console
// password, and the extra config text (for example a [policy] table), seeds fake-provider.db from
// testdata/seed.sql, and runs the daemon until the test ends. When the test ends, it checks that the daemon
// stopped cleanly and that no seeded text reached the daemon log, audit.jsonl, or cache.db.
func start(t *testing.T, extra string) *server {
	t.Helper()
	s := home(t, extra)
	s.seed()
	s.amgdOK(strings.NewReader(consolePassword+"\n"), "config", "set", "console.password")
	s.serve()
	return s
}

// home writes the home of start without a console password, a seed, or a daemon.
func home(t *testing.T, extra string) *server {
	t.Helper()
	s := &server{t: t, home: t.TempDir(), clientHome: t.TempDir(), api: freeAddress(t), console: freeAddress(t)}
	t.Cleanup(s.assertNoMailContent) // registered first, so it runs after the daemon stopped
	config := fmt.Sprintf("version = 1\ndefault_account = 'demo'\n[credentials]\nbackend='file'\n[screening]\nprovider='fake'\n[console]\nlisten='%s'\n%s\n[accounts.demo]\nprovider='fake'\nemail='%s'\n", s.console, extra, demo)
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

// amgd runs an amgd command on the home with stdin, as the operator on the daemon host would.
func (s *server) amgd(stdin io.Reader, args ...string) (string, string, int) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), processTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, amgdBinary, append([]string{"--home", s.home}, args...)...)
	cmd.Env = append(s.serverEnv(), "GORACE=atexit_sleep_ms=0")
	cmd.Stdin = stdin
	var out, errout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errout
	code := exitCode(cmd.Run())
	return out.String(), errout.String(), code
}

// amgdOK runs an amgd command and fails the test unless it exits 0.
func (s *server) amgdOK(stdin io.Reader, args ...string) (string, string) {
	s.t.Helper()
	out, errout, code := s.amgd(stdin, args...)
	if code != 0 {
		s.t.Fatalf("amgd %v: exit %d stdout %q stderr %q", args, code, out, errout)
	}
	return out, errout
}

// exitCode is the exit code of a finished command: -1 when it did not run.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}

// serve runs amgd serve on the home and waits until it listens. All that the daemon writes to stderr goes to the
// daemon log, with no line limit. Wait returns an error when the daemon fails, also when the race detector found
// a data race (exit code 66), or when the copy of stderr fails; stop reports it.
//
// freeAddress closes its port before the daemon opens it, so another process can take the port in between. A
// start that fails only for that reason runs again on a new free port, at most three times in all.
func (s *server) serve() {
	s.t.Helper()
	var cmd *exec.Cmd
	var done chan error
	for attempt := 1; ; attempt++ {
		mark := s.logLen()
		listening := make(chan struct{})
		done = make(chan error, 1)
		cmd = exec.Command(amgdBinary, "--home", s.home, "serve", "--listen", s.api)
		cmd.Env = s.serverEnv()
		cmd.Stderr = &logWriter{s: s, listening: listening}
		if err := cmd.Start(); err != nil {
			s.t.Fatal(err)
		}
		go func() { done <- cmd.Wait() }()
		select {
		case <-listening:
		case err := <-done:
			if attempt < 3 && s.movePort(mark) {
				continue
			}
			s.t.Fatalf("daemon stopped: %v\n%s", err, s.logText())
		case <-time.After(processTimeout):
			_ = cmd.Process.Kill()
			s.t.Fatalf("daemon did not listen\n%s", s.logText())
		}
		break
	}
	s.pid = cmd.Process.Pid
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					s.t.Errorf("daemon shutdown: %v\n%s", err, s.logText())
				}
			case <-time.After(processTimeout):
				_ = cmd.Process.Kill()
				s.t.Error("daemon did not stop")
			}
		})
	}
	s.t.Cleanup(s.stop)
}

// movePort handles a daemon start, logged after mark, that failed only because its API or console port was
// taken: it moves that address to a new free port and removes the lines of the failed start from the log. It
// reports whether it moved an address.
func (s *server) movePort(mark int) bool {
	s.t.Helper()
	s.mu.Lock()
	failed := s.log.String()[mark:]
	s.mu.Unlock()
	switch {
	case strings.HasSuffix(failed, "error: listener unavailable\n"):
		s.api = freeAddress(s.t)
	case strings.HasSuffix(failed, "error: console listener unavailable\n"):
		path := filepath.Join(s.home, "config.toml")
		config, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(config, []byte(s.console)) {
			s.t.Fatalf("console address %s not in config.toml: %v", s.console, err)
		}
		next := freeAddress(s.t)
		if err := os.WriteFile(path, bytes.ReplaceAll(config, []byte(s.console), []byte(next)), 0600); err != nil {
			s.t.Fatal(err)
		}
		s.console = next
	default:
		return false
	}
	s.mu.Lock()
	s.log.Truncate(mark)
	s.mu.Unlock()
	return true
}

func (s *server) logLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.Len()
}

// logWriter appends all that the daemon writes to stderr to the daemon log, and closes listening when the daemon
// writes the line "daemon listening". exec calls Write from one goroutine.
type logWriter struct {
	s         *server
	line      []byte // the part of the current line that serve wrote, until serve listens
	listening chan struct{}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	w.s.log.Write(p)
	w.s.mu.Unlock()
	if w.listening == nil {
		return len(p), nil
	}
	w.line = append(w.line, p...)
	for {
		end := bytes.IndexByte(w.line, '\n')
		if end < 0 {
			return len(p), nil
		}
		if string(w.line[:end]) == "daemon listening" {
			close(w.listening)
			w.line, w.listening = nil, nil
			return len(p), nil
		}
		w.line = w.line[end+1:]
	}
}

// restart stops the daemon with SIGTERM and runs it again on the same home.
func (s *server) restart() {
	s.t.Helper()
	s.stop()
	s.serve()
}

func (s *server) serverEnv() []string {
	return []string{"HOME=" + s.home, "AMG_CREDENTIALS_PASSWORD=" + filePassword}
}

func (s *server) seed() {
	s.t.Helper()
	schema, err := os.ReadFile("../providers/fake/schema.sql")
	if err != nil {
		s.t.Fatal(err)
	}
	seed, err := os.ReadFile("testdata/seed.sql")
	if err != nil {
		s.t.Fatal(err)
	}
	s.exec(string(schema))
	s.exec(string(seed))
}

// exec runs SQL on fake-provider.db, the mailbox that the fake provider reads and writes.
func (s *server) exec(statements string, args ...any) {
	s.t.Helper()
	if _, err := s.db().Exec(statements, args...); err != nil {
		s.t.Fatal(err)
	}
}

// column returns one text column of every row that query selects, in order.
func (s *server) column(query string, args ...any) []string {
	s.t.Helper()
	rows, err := s.db().Query(query, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.t.Fatal(err)
	}
	return out
}

// tables is the content of a database: table name to rows in insert order, and each row column name to value,
// with nil for NULL.
type tables map[string][]map[string]any

// dump reads every row of every table of fake-provider.db, so a test can show that a step changed only what
// it should.
func (s *server) dump() tables {
	s.t.Helper()
	return readTables(s.t, s.db())
}

// readTables reads every row of every table of db.
func readTables(t *testing.T, db *sql.DB) tables {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	out := tables{}
	for names.Next() {
		var table string
		if err := names.Scan(&table); err != nil {
			t.Fatal(err)
		}
		out[table] = []map[string]any{}
	}
	if err := errors.Join(names.Err(), names.Close()); err != nil {
		t.Fatal(err)
	}
	for table := range out {
		rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, column := range columns {
				if text, ok := values[i].([]byte); ok {
					values[i] = string(text)
				}
				row[column] = values[i]
			}
			out[table] = append(out[table], row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// assertTables fails the test unless fake-provider.db holds exactly the rows of want. It shows each column
// that differs, or the whole row when a row is missing (map[]) or extra.
func (s *server) assertTables(want tables) {
	s.t.Helper()
	got := s.dump()
	names := slices.AppendSeq(slices.Collect(maps.Keys(got)), maps.Keys(want))
	slices.Sort(names)
	for _, table := range slices.Compact(names) {
		for i := range max(len(got[table]), len(want[table])) {
			var g, w map[string]any
			if i < len(got[table]) {
				g = got[table][i]
			}
			if i < len(want[table]) {
				w = want[table][i]
			}
			if g == nil || w == nil {
				s.t.Errorf("fake-provider.db %s row %d:\n got %v\nwant %v", table, i+1, g, w)
				continue
			}
			for _, column := range slices.Sorted(maps.Keys(w)) {
				if !reflect.DeepEqual(g[column], w[column]) {
					s.t.Errorf("fake-provider.db %s row %d: %s is %v, want %v", table, i+1, column, g[column], w[column])
				}
			}
		}
	}
}

// value returns the text of one column of one message.
func (s *server) value(column, id string) string {
	s.t.Helper()
	got := s.column(`SELECT `+column+` FROM messages WHERE account = ? AND id = ?`, demo, id)
	if len(got) != 1 {
		s.t.Fatalf("message %s: %d rows", id, len(got))
	}
	return got[0]
}

func (s *server) db() *sql.DB {
	s.t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(s.home, "fake-provider.db"))
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { db.Close() })
	return db
}

// reload runs amgd reload, which sends the signal and returns at once, then waits until the daemon log shows one
// more reload result. It returns that line: "reload ok" or "reload refused: CODE; old settings kept".
func (s *server) reload() string {
	s.t.Helper()
	before := s.reloads()
	if out, errout, code := s.amgd(nil, "reload"); code != 0 || out != "" || errout != fmt.Sprintf("reload sent to daemon (PID %d); see the daemon log\n", s.pid) {
		s.t.Fatalf("amgd reload: exit %d stdout %q stderr %q", code, out, errout)
	}
	return s.awaitReload(before)
}

// awaitReload waits until the daemon log has more reload results than before, and returns the newest.
func (s *server) awaitReload(before int) string {
	s.t.Helper()
	deadline := time.Now().Add(processTimeout)
	for time.Now().Before(deadline) {
		if lines := s.reloadLines(); len(lines) > before {
			return lines[len(lines)-1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("no reload result\n%s", s.logText())
	return ""
}

func (s *server) reloads() int { return len(s.reloadLines()) }

// reloadLines are the reload results in the daemon log, in order.
func (s *server) reloadLines() []string {
	var out []string
	for line := range strings.Lines(s.logText()) {
		if line = strings.TrimSuffix(line, "\n"); line == "reload ok" || strings.HasPrefix(line, "reload refused: ") {
			out = append(out, line)
		}
	}
	return out
}

func (s *server) logText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.String()
}

// amg runs the agent's client against the daemon with an empty client home, as an agent on another host would.
// AMG_HOST names the daemon. Times in meeting commands are in UTC. A binary with the race detector does not wait
// 1 s before it exits (GORACE atexit_sleep_ms); it still exits with code 66 when it found a data race.
func (s *server) amg(args ...string) (string, string, int) {
	s.t.Helper()
	return s.amgEnv([]string{"AMG_HOST=http://" + s.api}, args...)
}

// amgEnv runs amg with extra environment variables.
func (s *server) amgEnv(env []string, args ...string) (string, string, int) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), processTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, amgBinary, args...)
	cmd.Env = append([]string{"HOME=" + s.clientHome, "TZ=UTC", "GORACE=atexit_sleep_ms=0"}, env...)
	var out, errout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errout
	code := exitCode(cmd.Run())
	return out.String(), errout.String(), code
}

// ok runs the CLI and fails the test unless it exits 0 without stderr.
func (s *server) ok(args ...string) string {
	s.t.Helper()
	out, errout, code := s.amg(args...)
	if code != 0 || errout != "" {
		s.t.Fatalf("amg %v: exit %d stderr %q", args, code, errout)
	}
	return out
}

// refused runs the CLI and fails the test unless it exits 1 with exactly the error line want.
func (s *server) refused(want string, args ...string) {
	s.t.Helper()
	out, errout, code := s.amg(args...)
	if code != 1 || out != "" || errout != "error: "+want+"\n" {
		s.t.Fatalf("amg %v: exit %d stdout %q stderr %q, want error %q", args, code, out, errout, want)
	}
}

// pending is the result of a write that waits for approval.
type pending struct {
	Status     string `json:"status"`
	ApprovalID string `json:"approval_id"`
	URL        string `json:"url"`
}

// held runs a write command and fails the test unless the daemon stored it as a pending action.
func (s *server) held(args ...string) pending {
	s.t.Helper()
	p := decode[pending](s.t, s.ok(args...))
	if p.Status != "pending" || p.ApprovalID == "" || p.URL != "http://"+s.console+"/a/"+p.ApprovalID {
		s.t.Fatalf("amg %v: not pending: %+v", args, p)
	}
	return p
}

// state is the state of a pending action, from approvals status.
func (s *server) state(approvalID string) string {
	s.t.Helper()
	return decode[struct{ State string }](s.t, s.ok("approvals", "status", approvalID)).State
}

// decide approves or denies a pending action in the approval console, as the user does in a browser, and
// returns the HTTP status.
func (s *server) decide(approvalID, decision string) int {
	s.t.Helper()
	r, err := http.NewRequest("POST", "http://"+s.console+"/a/"+approvalID, strings.NewReader(url.Values{"decision": {decision}}.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+s.console)
	r.SetBasicAuth("admin", consolePassword)
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		s.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// approve approves a pending action and fails the test unless the console accepted the decision.
func (s *server) approve(approvalID string) {
	s.t.Helper()
	if code := s.decide(approvalID, "approve"); code != http.StatusSeeOther {
		s.t.Fatalf("approve %s: HTTP %d", approvalID, code)
	}
}

// assertNoMailContent checks that no name, subject, body, address, or code from the seed or the tests reached
// the daemon log, audit.jsonl, or a table of cache.db other than pending. Every name, subject, and body starts
// with canary-, and every address is in one of the domains below. It runs after the daemon stopped.
func (s *server) assertNoMailContent() {
	s.t.Helper()
	audit, err := os.ReadFile(filepath.Join(s.home, "audit.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	texts := map[string]string{"daemon log": s.logText(), "audit.jsonl": string(audit)}
	maps.Copy(texts, s.cacheText())
	for name, text := range texts {
		for _, canary := range []string{"canary-", "example.test", "evil.test", "demo.test", "482913", "hunter2"} {
			if strings.Contains(text, canary) {
				s.t.Errorf("%s contains %q", name, canary)
			}
		}
	}
}

// cacheText opens cache.db read-only and returns, for each column of each table except pending, the non-NULL
// values one per line, keyed "cache.db TABLE.COLUMN". pending is the one table that may hold mail content: the
// request arguments of writes that wait for approval. cacheText returns nothing when the daemon made no cache.db.
func (s *server) cacheText() map[string]string {
	s.t.Helper()
	path := filepath.Join(s.home, "cache.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		s.t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro")
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	out := map[string]string{}
	for table, rows := range readTables(s.t, db) {
		if table == "pending" {
			continue
		}
		for _, row := range rows {
			for column, value := range row {
				if value != nil {
					out["cache.db "+table+"."+column] += fmt.Sprint(value) + "\n"
				}
			}
		}
	}
	return out
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func decode[T any](t *testing.T, text string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return v
}
