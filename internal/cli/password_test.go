package cli_test

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

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func fileHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if os.WriteFile(filepath.Join(home, "config.toml"), []byte("[credentials]\nbackend='file'\n[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"), 0600) != nil {
		t.Fatal("config write failed")
	}
	return home
}

func TestFilePasswordLinePreservesFollowingScreeningKey(t *testing.T) {
	home := fileHome(t)
	var out, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"auth", "set-screening-key", "--home", home}, &out, &stderr, cli.Options{Stdin: strings.NewReader("synthetic-password\nsynthetic-key\n"), LookupEnv: func(string) string { return "" }})
	if code != 0 {
		t.Fatal("password and key lines were not accepted separately")
	}
	if key, err := credentials.NewFile(home, "synthetic-password").LoadScreeningKey(context.Background()); err != nil || key != "synthetic-key" {
		t.Fatal("following key line was lost")
	}
	if strings.Contains(out.String()+stderr.String(), "synthetic-") {
		t.Fatal("secret appeared in output")
	}
}

func TestFilePasswordHostileInput(t *testing.T) {
	for _, input := range []string{"\n", strings.Repeat("x", 4097) + "\n", "private-\x1b[31m\n", "private-\x00\n"} {
		t.Run("refused", func(t *testing.T) {
			home := fileHome(t)
			var out, stderr bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			code := cli.Run(ctx, []string{"auth", "set-screening-key", "--home", home}, &out, &stderr, cli.Options{Stdin: strings.NewReader(input + "synthetic-key\n"), LookupEnv: func(string) string { return "" }})
			if code != 1 || strings.Contains(out.String()+stderr.String(), "private-") {
				t.Fatal("unsafe password accepted or echoed")
			}
			if _, err := os.Stat(filepath.Join(home, "credentials.age")); !os.IsNotExist(err) {
				t.Fatal("unsafe password persisted a file")
			}
		})
	}
}

func TestFilePasswordDoesNotWaitForEOF(t *testing.T) {
	home := fileHome(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "synthetic-password\n") }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	opened := false
	opts := cli.Options{Stdin: input, LookupEnv: func(string) string { return "" }, Server: server.Options{M365: m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: func(context.Context, string) error { opened = true; return errors.New("synthetic-refusal") }}}}
	var out, stderr bytes.Buffer
	cli.Run(ctx, []string{"auth", "login", "--home", home}, &out, &stderr, opts)
	if !opened || ctx.Err() != nil {
		t.Fatal("one password line waited for EOF")
	}
}

func TestEmptyEnvironmentPasswordNeverFallsBackToStdin(t *testing.T) {
	if os.Getenv("CREDENTIAL_PASSWORD_TEST_CHILD") == "empty" {
		var out, stderr bytes.Buffer
		code := cli.Run(context.Background(), []string{"auth", "set-screening-key", "--home", fileHome(t)}, &out, &stderr, cli.Options{Stdin: strings.NewReader("synthetic-password\nsynthetic-key\n")})
		if code != 1 || !strings.Contains(stderr.String(), "password missing") {
			t.Fatal("empty environment password fell back to stdin")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEmptyEnvironmentPasswordNeverFallsBackToStdin$")
	child.Env = []string{"HOME=" + t.TempDir(), "CREDENTIAL_PASSWORD_TEST_CHILD=empty", credentials.PasswordEnvironment + "="}
	if child.Run() != nil {
		t.Fatal("empty environment password was not refused")
	}
}

func TestInjectedPasswordLookupIgnoresProcessEnvironment(t *testing.T) {
	if os.Getenv("CREDENTIAL_PASSWORD_TEST_CHILD") == "injected" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		opened := false
		var out, stderr bytes.Buffer
		opts := cli.Options{Stdin: strings.NewReader("synthetic-password\n"), LookupEnv: func(string) string { return "" }, Server: server.Options{M365: m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: func(context.Context, string) error {
			opened = true
			if _, present := os.LookupEnv(credentials.PasswordEnvironment); present {
				t.Error("process password remained available to browser")
			}
			return errors.New("synthetic-refusal")
		}}}}
		cli.Run(ctx, []string{"auth", "login", "--home", fileHome(t)}, &out, &stderr, opts)
		if !opened || strings.Contains(out.String()+stderr.String(), "private-") {
			t.Fatal("injected environment did not isolate password selection")
		}
		return
	}
	for _, value := range []string{"", "private-process-password"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInjectedPasswordLookupIgnoresProcessEnvironment$")
		child.Env = []string{"HOME=" + t.TempDir(), "CREDENTIAL_PASSWORD_TEST_CHILD=injected", credentials.PasswordEnvironment + "=" + value}
		err := child.Run()
		cancel()
		if err != nil {
			t.Fatal("process environment interfered with injected password lookup")
		}
	}
}

func TestFilePasswordEnvironmentDoesNotReachBrowserChild(t *testing.T) {
	switch os.Getenv("CREDENTIAL_PASSWORD_TEST_CHILD") {
	case "browser":
		if _, exists := os.LookupEnv(credentials.PasswordEnvironment); exists {
			t.Fatal("browser child inherited password")
		}
		return
	case "login", "login-injected":
		home := fileHome(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		opened := false
		var out, stderr bytes.Buffer
		opts := cli.Options{Stdin: strings.NewReader(""), Server: server.Options{M365: m365.Options{AuthorityURL: "http://127.0.0.1:1", OpenBrowser: func(context.Context, string) error {
			opened = true
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilePasswordEnvironmentDoesNotReachBrowserChild$")
			child.Env = append(os.Environ(), "CREDENTIAL_PASSWORD_TEST_CHILD=browser")
			if child.Run() != nil {
				t.Error("password inherited by browser child")
			}
			return errors.New("synthetic-refusal")
		}}}}
		if os.Getenv("CREDENTIAL_PASSWORD_TEST_CHILD") == "login-injected" {
			opts.Server.Credentials = &keyStore{}
			path := filepath.Join(home, "config.toml")
			data, err := os.ReadFile(path)
			if err != nil || os.WriteFile(path, bytes.Replace(data, []byte("backend='file'"), []byte("backend='keychain'"), 1), 0600) != nil {
				t.Fatal("cannot configure injected Keychain store")
			}
		}
		cli.Run(ctx, []string{"auth", "login", "--home", home}, &out, &stderr, opts)
		if !opened {
			t.Fatal("environment password did not bypass stdin")
		}
		if strings.Contains(out.String()+stderr.String(), "private-password") {
			t.Fatal("password leaked")
		}
		return
	}
	for _, mode := range []string{"login", "login-injected"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilePasswordEnvironmentDoesNotReachBrowserChild$")
			cmd.Env = []string{"HOME=" + t.TempDir(), "CREDENTIAL_PASSWORD_TEST_CHILD=" + mode, credentials.PasswordEnvironment + "=private-password"}
			if cmd.Run() != nil {
				t.Fatal("isolated environment/login check failed")
			}
		})
	}
}

func TestFileServePipedPasswordNeverPromptsForScreeningKey(t *testing.T) {
	home := fileHome(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	go func() { _, _ = io.WriteString(writer, "synthetic-password\n") }()
	ctx, cancel := context.WithTimeout(context.Background(), serveBound)
	defer cancel()
	var out, stderr bytes.Buffer
	code := cli.Run(ctx, []string{"serve", "--home", home, "--listen", "127.0.0.1:0"}, &out, &stderr, cli.Options{Stdin: input, LookupEnv: func(string) string { return "" }})
	if code != 1 || ctx.Err() != nil || !strings.Contains(stderr.String(), "screening key: missing;") || strings.Contains(stderr.String(), "Enter the screening") {
		t.Fatal("piped password led to a screening key prompt")
	}
}

type failedPasswordReader struct{}

func (failedPasswordReader) Read([]byte) (int, error) {
	return 0, errors.New("private-read-detail")
}

func TestFileServePasswordFailureReportsStoreCheck(t *testing.T) {
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
			code := cli.Run(context.Background(), []string{"serve", "--home", home, "--listen", "127.0.0.1:0"}, &out, &stderr, cli.Options{Stdin: tc.input, LookupEnv: func(string) string { return "" }})
			text := stderr.String()
			if code != 1 || !strings.HasPrefix(text, "credential check: credentials (file): "+tc.status+"\n") || strings.Contains(text, "server listening") || strings.Contains(text, "private-") || out.Len() != 0 {
				t.Fatal("password failure omitted fixed first store check or exposed private input")
			}
			if _, err := os.Stat(filepath.Join(home, "credentials.age")); !os.IsNotExist(err) {
				t.Fatal("password failure created credential file")
			}
		})
	}
}

func TestFilePasswordCancellation(t *testing.T) {
	home := fileHome(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out, stderr bytes.Buffer
	if code := cli.Run(ctx, []string{"auth", "set-screening-key", "--home", home}, &out, &stderr, cli.Options{Stdin: input, LookupEnv: func(string) string { return "" }}); code != 1 || !strings.Contains(stderr.String(), "canceled") {
		t.Fatal("password read ignored cancellation")
	}
}
