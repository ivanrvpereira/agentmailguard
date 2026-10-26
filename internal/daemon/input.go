package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// Read one byte at a time: buffering here could consume the following screening key line.
func passwordLine(ctx context.Context, stdin io.Reader) (string, error) {
	type result struct {
		password string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		var data []byte
		var one [1]byte
		for {
			if ctx.Err() != nil {
				done <- result{err: errors.New("credential password entry canceled")}
				return
			}
			n, err := stdin.Read(one[:])
			if n > 0 {
				if one[0] == '\n' {
					done <- result{password: string(data)}
					return
				}
				data = append(data, one[0])
				if len(data) > 4096 {
					done <- result{err: credentials.ErrInvalidPassword}
					return
				}
			}
			if err == io.EOF {
				done <- result{password: string(data)}
				return
			}
			if err != nil {
				done <- result{err: errors.New("cannot read credential password")}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return "", errors.New("credential password entry canceled")
	case r := <-done:
		return r.password, r.err
	}
}

// hideInput turns off echo when stdin is a terminal. restore puts the saved terminal state back
// and ends the hidden line; it is a no-op for piped input.
func hideInput(ctx context.Context, stdin io.Reader, stderr io.Writer) (interactive bool, restore func(), err error) {
	f, interactive := terminal(stdin)
	if !interactive {
		return false, func() {}, nil
	}
	capture, cancel := context.WithTimeout(ctx, time.Second)
	cmd := exec.CommandContext(capture, "/bin/stty", "-g")
	cmd.Stdin = f
	state, err := cmd.Output()
	cancel()
	if err != nil || len(state) == 0 {
		return false, nil, errors.New("cannot read terminal state")
	}
	stty := func(arg string) error {
		// Echo restoration must still run after the caller is canceled.
		timeout, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cmd := exec.CommandContext(timeout, "/bin/stty", arg)
		cmd.Stdin = f
		return cmd.Run()
	}
	if stty("-echo") != nil {
		return false, nil, errors.New("cannot hide terminal input; pipe the password on stdin instead")
	}
	return true, func() { _ = stty(strings.TrimSpace(string(state))); fmt.Fprintln(stderr) }, nil
}

func readCredentialPassword(ctx context.Context, stdin io.Reader, stderr io.Writer, creating bool) (string, error) {
	interactive, restore, err := hideInput(ctx, stdin, stderr)
	if err != nil {
		return "", err
	}
	defer restore()
	if interactive {
		fmt.Fprint(stderr, "Credential file password: ")
	}
	password, err := passwordLine(ctx, stdin)
	if err != nil {
		return "", err
	}
	if err := credentials.ValidatePassword(password); err != nil {
		return "", err
	}
	if interactive && creating {
		fmt.Fprint(stderr, "\nConfirm credential file password: ")
		confirmation, err := passwordLine(ctx, stdin)
		if err != nil {
			return "", err
		}
		if confirmation != password {
			return "", errors.New("credential passwords do not match")
		}
	}
	return password, nil
}

const maxScreeningKeyBytes = 4096

// terminal reports whether r is an interactive terminal.
func terminal(r io.Reader) (*os.File, bool) {
	f, ok := r.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return f, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/stty", "-g")
	c.Stdin = f
	return f, c.Run() == nil
}

// readScreeningKey returns a validated key, or a fixed problem message. It never echoes or prints the key.
func readScreeningKey(ctx context.Context, stdin io.Reader, stderr io.Writer, prompt string) (string, string) {
	read := func() (string, error) {
		b, err := io.ReadAll(io.LimitReader(stdin, maxScreeningKeyBytes+1))
		return string(b), err
	}
	restore := func() {}
	if f, ok := terminal(stdin); ok {
		fmt.Fprint(stderr, prompt)
		// stty avoids a terminal dependency.
		stty := func(arg string) error { c := exec.Command("/bin/stty", arg); c.Stdin = f; return c.Run() }
		if stty("-echo") != nil {
			fmt.Fprintln(stderr)
			return "", "cannot hide terminal input; pipe the key on stdin instead"
		}
		restore = func() { _ = stty("echo"); fmt.Fprintln(stderr) }
		read = func() (string, error) {
			line, err := bufio.NewReader(io.LimitReader(f, maxScreeningKeyBytes+1)).ReadString('\n')
			if err == io.EOF {
				err = nil
			}
			return line, err
		}
	}
	type result struct {
		raw string
		err error
	}
	done := make(chan result, 1)
	go func() { raw, err := read(); done <- result{raw, err} }()
	var r result
	select {
	case <-ctx.Done():
		restore()
		return "", "Screening key setup canceled"
	case r = <-done:
		restore()
	}
	if r.err != nil {
		return "", "cannot read screening key"
	}
	if len(r.raw) > maxScreeningKeyBytes {
		return "", "Screening key is too long"
	}
	key := strings.TrimSpace(r.raw)
	if key == "" {
		return "", "Screening key is empty"
	}
	if strings.IndexFunc(key, func(c rune) bool { return c <= ' ' || c > '~' }) >= 0 {
		return "", "Screening key must be one line without spaces or control characters"
	}
	return key, ""
}

const invalidConsolePassword = "console password must be 8 to 72 bytes without control characters"

// readConsolePassword returns the new console password or a fixed problem message. It never asks for the current
// password, so a forgotten one can be reset on the daemon host. Echo stays off on a terminal until it returns.
func readConsolePassword(ctx context.Context, stdin io.Reader, stderr io.Writer) (string, string) {
	interactive, restore, err := hideInput(ctx, stdin, stderr)
	if err != nil {
		return "", err.Error()
	}
	defer restore()
	prompts := 0
	line := func(prompt string) (string, string) {
		if interactive {
			if prompts > 0 {
				fmt.Fprintln(stderr)
			}
			fmt.Fprint(stderr, prompt)
			prompts++
		}
		v, err := passwordLine(ctx, stdin)
		switch {
		case ctx.Err() != nil:
			return "", "console password setup canceled"
		case errors.Is(err, credentials.ErrInvalidPassword):
			return "", invalidConsolePassword
		case err != nil:
			return "", "cannot read console password"
		}
		return v, ""
	}
	password, problem := line("New console password: ")
	if problem != "" {
		return "", problem
	}
	if !validConsolePassword(password) {
		return "", invalidConsolePassword
	}
	if interactive {
		confirmation, problem := line("Confirm new console password: ")
		if problem != "" {
			return "", problem
		}
		if confirmation != password {
			return "", "console passwords do not match"
		}
	}
	return password, ""
}

// validConsolePassword allows 8 to 72 bytes: bcrypt ignores or refuses anything longer.
func validConsolePassword(password string) bool {
	if len(password) < 8 || len(password) > 72 || !utf8.ValidString(password) {
		return false
	}
	for _, c := range password {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
