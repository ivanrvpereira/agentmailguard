package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

const maxJevKeyBytes = 4096

// setJevKey reads the key from stdin so it never appears in arguments or shell history.
// A terminal gets a prompt with echo disabled; other input is read to EOF.
func setJevKey(ctx context.Context, home string, stdin io.Reader, stderr io.Writer, opts server.Options) int {
	if stdin == nil {
		stdin = os.Stdin
	}
	key, problem := readJevKey(ctx, stdin, stderr, "Jev (OpenRouter) API key: ")
	if problem != "" {
		return failure(stderr, problem)
	}
	store, err := localCredentials(ctx, home, opts.Credentials)
	if err != nil {
		return failure(stderr, err.Error())
	}
	if store.SaveOpenRouterKey(ctx, key) != nil {
		return failure(stderr, "Keychain save failed")
	}
	fmt.Fprintln(stderr, "Jev key saved to Keychain. Restart the server if it already loaded a previous key.")
	return 0
}

// terminal reports whether r is an interactive terminal.
func terminal(r io.Reader) (*os.File, bool) {
	f, ok := r.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := f.Stat()
	return f, err == nil && info.Mode()&os.ModeCharDevice != 0
}

// readJevKey returns a validated key, or a fixed problem message. It never echoes or prints the key.
func readJevKey(ctx context.Context, stdin io.Reader, stderr io.Writer, prompt string) (string, string) {
	read := func() (string, error) {
		b, err := io.ReadAll(io.LimitReader(stdin, maxJevKeyBytes+1))
		return string(b), err
	}
	restore := func() {}
	if f, ok := terminal(stdin); ok {
		fmt.Fprint(stderr, prompt)
		// ponytail: stty avoids a terminal dependency; the server host is macOS only.
		stty := func(arg string) error { c := exec.Command("/bin/stty", arg); c.Stdin = f; return c.Run() }
		if stty("-echo") != nil {
			fmt.Fprintln(stderr)
			return "", "cannot hide terminal input; pipe the key on stdin instead"
		}
		restore = func() { _ = stty("echo"); fmt.Fprintln(stderr) }
		read = func() (string, error) {
			line, err := bufio.NewReader(io.LimitReader(f, maxJevKeyBytes+1)).ReadString('\n')
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
		return "", "Jev key setup canceled"
	case r = <-done:
		restore()
	}
	if r.err != nil {
		return "", "cannot read Jev key"
	}
	if len(r.raw) > maxJevKeyBytes {
		return "", "Jev key is too long"
	}
	key := strings.TrimSpace(r.raw)
	if key == "" {
		return "", "Jev key is empty"
	}
	if strings.IndexFunc(key, func(c rune) bool { return c <= ' ' || c > '~' }) >= 0 {
		return "", "Jev key must be one line without spaces or control characters"
	}
	return key, ""
}
