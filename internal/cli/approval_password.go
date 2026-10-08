package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

const invalidApprovalPassword = "approval password must be 8 to 72 bytes without control characters"

// approvalHashes is the part of the record store that keeps the approval password hash.
// It stays out of credentials.Store, which the provider adapters use.
type approvalHashes interface {
	LoadApprovalHash(context.Context) (string, error)
	SaveApprovalHash(context.Context, string) error
}

// setApprovalPassword reads passwords from stdin only, so they never appear in arguments or shell history.
// When a password is already set, the current one must match before anything changes.
func setApprovalPassword(ctx context.Context, home string, stdin io.Reader, stderr io.Writer, opts server.Options, lookup func(string) string) int {
	if stdin == nil {
		stdin = os.Stdin
	}
	store, err := localCredentials(ctx, home, opts.Credentials, stdin, stderr, lookup)
	if err != nil {
		return failure(stderr, err.Error())
	}
	hashes, ok := store.(approvalHashes)
	if !ok {
		return failure(stderr, "credential store unavailable")
	}
	current, err := hashes.LoadApprovalHash(ctx)
	if err != nil && !errors.Is(err, credentials.ErrNotFound) {
		return failure(stderr, "credential read failed")
	}
	password, problem := readApprovalPassword(ctx, stdin, stderr, current)
	if problem != "" {
		return failure(stderr, problem)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return failure(stderr, "approval password hashing failed")
	}
	if hashes.SaveApprovalHash(ctx, string(hash)) != nil {
		return failure(stderr, "credential save failed")
	}
	fmt.Fprintln(stderr, "Approval password saved. Restart the server to use it.")
	return 0
}

// readApprovalPassword checks the current password against currentHash when one is set, then returns the
// new password or a fixed problem message. Echo stays off on a terminal until it returns.
func readApprovalPassword(ctx context.Context, stdin io.Reader, stderr io.Writer, currentHash string) (string, string) {
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
			return "", "approval password setup canceled"
		case errors.Is(err, credentials.ErrInvalidPassword):
			return "", invalidApprovalPassword
		case err != nil:
			return "", "cannot read approval password"
		}
		return v, ""
	}
	if currentHash != "" {
		current, problem := line("Current approval password: ")
		if problem != "" {
			return "", problem
		}
		if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(current)) != nil {
			return "", "approval password is wrong"
		}
	}
	password, problem := line("New approval password: ")
	if problem != "" {
		return "", problem
	}
	if !validApprovalPassword(password) {
		return "", invalidApprovalPassword
	}
	if interactive {
		confirmation, problem := line("Confirm new approval password: ")
		if problem != "" {
			return "", problem
		}
		if confirmation != password {
			return "", "approval passwords do not match"
		}
	}
	return password, ""
}

// validApprovalPassword allows 8 to 72 bytes: bcrypt ignores or refuses anything longer.
func validApprovalPassword(password string) bool {
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
