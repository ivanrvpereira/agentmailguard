//go:build darwin

package cli_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func passwordTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal("cannot open test terminal")
	}
	t.Cleanup(func() { master.Close() })
	for _, op := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), op, 0); err != 0 {
			t.Fatal("cannot prepare test terminal")
		}
	}
	var name [128]byte
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); err != 0 {
		t.Fatal("cannot name test terminal")
	}
	slave, err := os.OpenFile(string(bytes.TrimRight(name[:], "\x00")), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal("cannot open terminal slave")
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}
func terminalEcho(t *testing.T, f *os.File) bool {
	t.Helper()
	var state syscall.Termios
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&state))); err != 0 {
		t.Fatal("cannot inspect test terminal")
	}
	return state.Lflag&syscall.ECHO != 0
}

type promptWriter struct {
	bytes.Buffer
	respond func(string)
}

// Color-profile writers may forward plain prompts through io.StringWriter.
func (w *promptWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *promptWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.respond(string(p))
	return n, err
}

func TestManualTerminalCallbackEchoAndCancellation(t *testing.T) {
	for _, mode := range []string{"invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := passwordTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			prompted := false
			stderr := &manualWriter{respond: func(text string) {
				if strings.Contains(text, "full callback URL") {
					prompted = true
					if terminalEcho(t, slave) {
						t.Error("callback prompt left echo enabled")
					}
					if mode == "cancel" {
						cancel()
					} else {
						_, _ = io.WriteString(master, "http://private-callback.invalid/?code=private-code\n")
					}
				}
			}}
			code := cli.Run(ctx, []string{"--home", manualHome(t), "auth", "login", "--manual"}, io.Discard, stderr, cli.Options{Stdin: slave, LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: &diagnosticStore{}, Gmail: gmail.Options{AuthURL: "http://127.0.0.1:1/auth", OpenBrowser: func(context.Context, string) error {
				t.Error("manual login opened browser")
				return nil
			}}}})
			if code != 1 || !prompted || !terminalEcho(t, slave) || strings.Contains(stderr.String(), "private-") {
				t.Fatal("callback terminal refusal unsafe or echo not restored")
			}
		})
	}
}

func TestFileTerminalPasswordConfirmationAndEcho(t *testing.T) {
	for _, mode := range []string{"match", "mismatch", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := passwordTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			prompts := 0
			stderr := &promptWriter{}
			stderr.respond = func(text string) {
				switch {
				case strings.Contains(text, "Credential file password:"):
					prompts++
					if terminalEcho(t, slave) {
						t.Error("password prompt left echo enabled")
					}
					if mode == "cancel" {
						cancel()
						return
					}
					_, _ = io.WriteString(master, "private-terminal-password\n")
				case strings.Contains(text, "Confirm credential file password:"):
					prompts++
					if terminalEcho(t, slave) {
						t.Error("confirmation left echo enabled")
					}
					password := "private-terminal-password\n"
					if mode == "mismatch" {
						password = "different\n"
					}
					_, _ = io.WriteString(master, password)
				case strings.Contains(text, "Jev (OpenRouter) API key:"):
					_, _ = io.WriteString(master, "synthetic-key\n")
				}
			}
			code := cli.Run(ctx, []string{"auth", "set-jev-key", "--home", fileHome(t)}, io.Discard, stderr, cli.Options{Stdin: slave, LookupEnv: func(string) string { return "" }})
			if (code == 0) != (mode == "match") || (mode != "cancel" && prompts != 2) {
				t.Fatal("terminal confirmation behavior incorrect")
			}
			if !terminalEcho(t, slave) {
				t.Fatal("terminal echo not restored")
			}
			if strings.Contains(stderr.String(), "private-terminal-password") {
				t.Fatal("terminal password leaked")
			}
		})
	}
}
