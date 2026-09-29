package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
)

// Read one byte at a time: buffering here could consume the following Jev key line.
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

func readCredentialPassword(ctx context.Context, stdin io.Reader, stderr io.Writer, creating bool) (string, error) {
	f, interactive := terminal(stdin)
	if interactive {
		stty := func(arg string) error {
			// Echo restoration must still run after the caller is canceled.
			timeout, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cmd := exec.CommandContext(timeout, "/bin/stty", arg)
			cmd.Stdin = f
			return cmd.Run()
		}
		if stty("-echo") != nil {
			return "", errors.New("cannot hide terminal input; pipe the password on stdin instead")
		}
		defer func() { _ = stty("echo"); fmt.Fprintln(stderr) }()
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
