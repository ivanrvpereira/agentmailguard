package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

const fileConfig = "version = 1\ndefault_account = 'work'\n[credentials]\nbackend = 'file'\n[accounts.work]\nprovider = 'm365'\nemail = 'synthetic@example.test'\nclient_id = 'synthetic'\ntenant_id = 'synthetic'\n"

// fileHome is a home with the file credential backend and one Microsoft 365 account without a login.
func fileHome(t *testing.T) string {
	t.Helper()
	return writeHome(t, fileConfig)
}

// refusingBrowser records that the sign-in opened the browser and then fails the sign-in.
func refusingBrowser(opened *bool) func(context.Context, string) error {
	return func(context.Context, string) error {
		*opened = true
		return errors.New("synthetic-refusal")
	}
}

func TestFilePasswordLinePreservesFollowingScreeningKey(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	code, out, errout := amgd(t, home, daemon.Options{}, "synthetic-password\nsynthetic-key\n", "config", "set", "screening.api_key")
	if code != 0 {
		t.Fatalf("password and key lines were not accepted separately: exit %d stderr %q", code, errout)
	}
	if key, err := credentials.NewFile(home, "synthetic-password").LoadScreeningKey(t.Context()); err != nil || key != "synthetic-key" {
		t.Fatal("following key line was lost")
	}
	if strings.Contains(out+errout, "synthetic-") {
		t.Fatal("secret appeared in output")
	}
}

func TestFilePasswordHostileInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"\n", strings.Repeat("x", 4097) + "\n", "private-\x1b[31m\n", "private-\x00\n"} {
		home := fileHome(t)
		code, out, errout := amgd(t, home, daemon.Options{}, input+"synthetic-key\n", "config", "set", "screening.api_key")
		if code != 1 || strings.Contains(out+errout, "private-") {
			t.Fatalf("unsafe password accepted or echoed: exit %d stderr %q", code, errout)
		}
		if exists(t, home, "credentials.age") {
			t.Fatal("unsafe password persisted a file")
		}
	}
}

func TestFilePasswordDoesNotWaitForEOF(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "synthetic-password\n") }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opened := false
	opts := daemon.Options{Stdin: input, LookupEnv: noEnv, Server: server.Options{M365: m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: refusingBrowser(&opened)}}}
	daemon.Run(ctx, []string{"--home", home, "account", "login", "work"}, io.Discard, io.Discard, opts)
	if !opened || ctx.Err() != nil {
		t.Fatal("one password line waited for EOF")
	}
}

// cancelingReader cancels the command at its first read and then blocks until the command gives up, as a user
// who presses Ctrl-C at the password prompt.
type cancelingReader struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r cancelingReader) Read([]byte) (int, error) {
	r.cancel()
	<-r.ctx.Done()
	return 0, errors.New("synthetic read after cancel")
}

func TestFilePasswordCancellation(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	if code := daemon.Run(ctx, []string{"--home", home, "config", "set", "screening.api_key"}, io.Discard, &stderr, daemon.Options{Stdin: cancelingReader{ctx, cancel}, LookupEnv: noEnv}); code != 1 || !strings.HasSuffix(stderr.String(), "error: credential password entry canceled\n") {
		t.Fatalf("password read ignored cancellation: exit %d stderr %q", code, stderr.String())
	}
}

// childMode is the role of a test binary that a password test started again as a child process.
const childMode = "AMGD_PASSWORD_TEST_CHILD"

// runChild runs the test again in a child process with the environment env and fails when the child fails.
func runChild(t *testing.T, test string, env ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$")
	child.Env = append([]string{"HOME=" + t.TempDir()}, env...)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child %v: %v\n%s", env, err, out)
	}
}

func TestEmptyEnvironmentPasswordNeverFallsBackToStdin(t *testing.T) {
	if os.Getenv(childMode) == "empty" {
		var stderr bytes.Buffer
		code := daemon.Run(context.Background(), []string{"--home", fileHome(t), "config", "set", "screening.api_key"}, io.Discard, &stderr, daemon.Options{Stdin: strings.NewReader("synthetic-password\nsynthetic-key\n")})
		if code != 1 || !strings.Contains(stderr.String(), "password missing") {
			t.Fatalf("empty environment password fell back to stdin: exit %d stderr %q", code, stderr.String())
		}
		return
	}
	t.Parallel()
	runChild(t, "TestEmptyEnvironmentPasswordNeverFallsBackToStdin", childMode+"=empty", credentials.PasswordEnvironment+"=")
}

func TestInjectedPasswordLookupIgnoresProcessEnvironment(t *testing.T) {
	if os.Getenv(childMode) == "injected" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		opened := false
		var out, stderr bytes.Buffer
		opts := daemon.Options{Stdin: strings.NewReader("synthetic-password\n"), LookupEnv: noEnv, Server: server.Options{M365: m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: func(context.Context, string) error {
			opened = true
			if _, present := os.LookupEnv(credentials.PasswordEnvironment); present {
				t.Error("process password remained available to browser")
			}
			return errors.New("synthetic-refusal")
		}}}}
		daemon.Run(ctx, []string{"--home", fileHome(t), "account", "login", "work"}, &out, &stderr, opts)
		if !opened || strings.Contains(out.String()+stderr.String(), "private-") {
			t.Fatalf("injected environment did not isolate password selection: stderr %q", stderr.String())
		}
		return
	}
	t.Parallel()
	for _, value := range []string{"", "private-process-password"} {
		runChild(t, "TestInjectedPasswordLookupIgnoresProcessEnvironment", childMode+"=injected", credentials.PasswordEnvironment+"="+value)
	}
}

func TestFilePasswordEnvironmentDoesNotReachBrowserChild(t *testing.T) {
	switch os.Getenv(childMode) {
	case "browser":
		if _, exists := os.LookupEnv(credentials.PasswordEnvironment); exists {
			t.Fatal("browser child inherited password")
		}
		return
	case "login", "login-keychain":
		home := fileHome(t)
		opts := daemon.Options{Stdin: strings.NewReader("")}
		if os.Getenv(childMode) == "login-keychain" {
			home = writeHome(t, keychainAccountConfig)
			opts.Platform = &keychain{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		opened := false
		opts.Server.M365 = m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: func(context.Context, string) error {
			opened = true
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilePasswordEnvironmentDoesNotReachBrowserChild$")
			child.Env = append(os.Environ(), childMode+"=browser")
			if child.Run() != nil {
				t.Error("password inherited by browser child")
			}
			return errors.New("synthetic-refusal")
		}}
		var out, stderr bytes.Buffer
		daemon.Run(ctx, []string{"--home", home, "account", "login", "work"}, &out, &stderr, opts)
		if !opened {
			t.Fatalf("environment password did not bypass stdin: stderr %q", stderr.String())
		}
		if strings.Contains(out.String()+stderr.String(), "private-password") {
			t.Fatal("password leaked")
		}
		return
	}
	t.Parallel()
	for _, mode := range []string{"login", "login-keychain"} {
		runChild(t, "TestFilePasswordEnvironmentDoesNotReachBrowserChild", childMode+"="+mode, credentials.PasswordEnvironment+"=private-password")
	}
}

func TestFileServePipedPasswordNeverPromptsForScreeningKey(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "synthetic-password\n") }()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	var stderr bytes.Buffer
	code := daemon.Run(ctx, []string{"--home", home, "serve", "--listen", "127.0.0.1:0"}, io.Discard, &stderr, daemon.Options{Stdin: input, LookupEnv: noEnv})
	if code != 1 || ctx.Err() != nil || !strings.Contains(stderr.String(), "credential check: screening key: missing; run amgd config set screening.api_key --home "+shellQuoted(home)+"\n") || strings.Contains(stderr.String(), "Enter the screening") {
		t.Fatalf("piped password led to a screening key prompt: exit %d stderr %q", code, stderr.String())
	}
}

type failedPasswordReader struct{}

func (failedPasswordReader) Read([]byte) (int, error) {
	return 0, errors.New("private-read-detail")
}

func TestFileServePasswordFailureReportsStoreCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		input  io.Reader
		status string
	}{
		{"missing", strings.NewReader("\n"), "password missing"},
		{"control", strings.NewReader("private-\x1b[31m\n"), "access denied"},
		{"oversized", strings.NewReader(strings.Repeat("x", 4097) + "\n"), "access denied"},
		{"read-failure", failedPasswordReader{}, "access denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fileHome(t)
			var out, stderr bytes.Buffer
			code := daemon.Run(t.Context(), []string{"--home", home, "serve", "--listen", "127.0.0.1:0"}, &out, &stderr, daemon.Options{Stdin: tc.input, LookupEnv: noEnv})
			text := stderr.String()
			if code != 1 || !strings.HasPrefix(text, "credential check: credentials (file): "+tc.status+"\n") || strings.Contains(text, "listening") || strings.Contains(text, "private-") || out.Len() != 0 {
				t.Fatalf("password failure omitted fixed first store check or exposed private input: exit %d stderr %q", code, text)
			}
			if exists(t, home, "credentials.age") {
				t.Fatal("password failure created credential file")
			}
		})
	}
}

func TestFileServeRefusesAWrongPassword(t *testing.T) {
	t.Parallel()
	// Fake screening and an account without a login: only the password can refuse the start.
	home := writeHome(t, "version = 1\n[credentials]\nbackend = 'file'\n[screening]\nprovider = 'fake'\n[console]\nlisten = '"+freeAddress(t)+"'\n")
	if code, _, errout := amgd(t, home, daemon.Options{}, "synthetic-password\nsynthetic-key\n", "config", "set", "screening.api_key"); code != 0 {
		t.Fatalf("seed: exit %d stderr %q", code, errout)
	}
	before := readFile(t, filepath.Join(home, "credentials.age"))
	code, out, errout := amgd(t, home, daemon.Options{}, "private-wrong-password\n", "serve", "--listen", "127.0.0.1:0")
	if code != 1 || out != "" || !strings.Contains(errout, "credential check: credentials (file): wrong password or damaged file\n") || strings.Contains(errout, "listening") || strings.Contains(errout, "private-") {
		t.Fatalf("wrong password: exit %d stderr %q", code, errout)
	}
	if readFile(t, filepath.Join(home, "credentials.age")) != before {
		t.Fatal("a refused start changed the credential file")
	}
}

// The daemon writes the credential file with the production scrypt work factor, 18. It runs alone: it raises the
// work factor that TestMain lowered for every other test.
func TestServeWritesTheCredentialFileWithTheProductionWorkFactor(t *testing.T) {
	home := writeHome(t, "version = 1\n[credentials]\nbackend = 'file'\n[screening]\nprovider = 'fake'\n[console]\nlisten = '"+freeAddress(t)+"'\n")
	// The file starts at the lowered work factor, so only the start of serve can raise it.
	setConsolePassword(t, credentials.NewFile(home, "synthetic-password"))
	credentials.FileWorkFactor = productionWorkFactor
	t.Cleanup(func() { credentials.FileWorkFactor = 1 })
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "synthetic-password\n") }()
	d := startServe(t, home, freeAddress(t), daemon.Options{Stdin: input})
	if code := d.stop(); code != 0 {
		t.Fatalf("serve exit %d:\n%s", code, d.log.String())
	}
	header := strings.Split(readFile(t, filepath.Join(home, "credentials.age")), "\n")
	if len(header) < 2 {
		t.Fatal("no credential file header")
	}
	if fields := strings.Fields(header[1]); len(fields) != 4 || fields[0] != "->" || fields[1] != "scrypt" || fields[3] != "18" {
		t.Fatalf("credential file recipient %q; want scrypt with work factor 18", header[1])
	}
}
