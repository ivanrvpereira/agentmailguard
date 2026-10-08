package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

func approvalStore(t *testing.T) *credentials.RecordStore {
	t.Helper()
	return credentials.New(&localPlatform{values: map[[2]string][]byte{}}, t.TempDir(), nil)
}

func runSetApproval(store credentials.Store, input string, env map[string]string, args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	opts := cli.Options{Stdin: strings.NewReader(input), LookupEnv: func(k string) string { return env[k] }, Server: server.Options{Credentials: store}}
	code := cli.Run(context.Background(), append(args, "auth", "set-approval-password"), &out, &stderr, opts)
	return code, out.String(), stderr.String()
}

func storedApprovalHash(t *testing.T, store *credentials.RecordStore) string {
	t.Helper()
	hash, err := store.LoadApprovalHash(context.Background())
	if err != nil {
		t.Fatalf("no approval hash: %v", err)
	}
	return hash
}

// The tests that hash run in parallel: bcrypt.DefaultCost is slow under -race.
func TestSetApprovalPasswordStoresAHashOfThePipedPassword(t *testing.T) {
	t.Parallel()
	store := approvalStore(t)
	code, out, stderr := runSetApproval(store, "private-first-pass\n", nil)
	if code != 0 || out != "" || stderr != "Approval password saved. Restart the server to use it.\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	hash := storedApprovalHash(t, store)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("private-first-pass")) != nil {
		t.Fatal("stored hash does not verify the password")
	}
}

func TestSetApprovalPasswordChangeNeedsTheCurrentPassword(t *testing.T) {
	t.Parallel()
	store := approvalStore(t)
	if code, _, stderr := runSetApproval(store, "private-first-pass\n", nil); code != 0 {
		t.Fatalf("seed: %q", stderr)
	}
	before := storedApprovalHash(t, store)

	code, out, stderr := runSetApproval(store, "private-wrong-pass\nprivate-second-pass\n", nil)
	if code != 1 || out != "" || stderr != "error: approval password is wrong\n" || storedApprovalHash(t, store) != before {
		t.Fatalf("wrong current password: exit=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runSetApproval(store, "private-first-pass\nprivate-second-pass\n", nil)
	if code != 0 || strings.Contains(stderr, "private-") {
		t.Fatalf("change: exit=%d stderr=%q", code, stderr)
	}
	hash := storedApprovalHash(t, store)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("private-second-pass")) != nil {
		t.Fatal("changed hash does not verify the new password")
	}
}

func TestSetApprovalPasswordRefusesUnsafeInputWithoutSaving(t *testing.T) {
	const invalid = "error: approval password must be 8 to 72 bytes without control characters\n"
	for _, tc := range []struct {
		name, input string
		env         map[string]string
		args        []string
		want        string
	}{
		{name: "empty", input: "\n", want: invalid},
		{name: "eof", input: "", want: invalid},
		{name: "short", input: "private\n", want: invalid},
		{name: "control", input: "private-\x1b[31m-pass\n", want: invalid},
		{name: "carriage return", input: "private-pass\r\n", want: invalid},
		{name: "too long", input: "private-" + strings.Repeat("p", 65) + "\n", want: invalid},
		{name: "bad utf8", input: "private-\xff-pass\n", want: invalid},
		{name: "remote flag", input: "private-first-pass\n", args: []string{"--server", "http://127.0.0.1:1"}, want: "error: approval password setup is local only; remove --server and AGENTMAILGUARD_SERVER\n"},
		{name: "remote env", input: "private-first-pass\n", env: map[string]string{"AGENTMAILGUARD_SERVER": "http://127.0.0.1:1"}, want: "error: approval password setup is local only; remove --server and AGENTMAILGUARD_SERVER\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := approvalStore(t)
			code, out, stderr := runSetApproval(store, tc.input, tc.env, tc.args...)
			if code != 1 || out != "" || stderr != tc.want {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if _, err := store.LoadApprovalHash(context.Background()); !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("refused password was stored")
			}
		})
	}
}

func TestSetApprovalPasswordAcceptsSeventyTwoBytes(t *testing.T) {
	t.Parallel()
	store := approvalStore(t)
	password := strings.Repeat("é", 36)
	if code, _, stderr := runSetApproval(store, password+"\n", nil); code != 0 {
		t.Fatalf("72-byte password refused: %q", stderr)
	}
	if bcrypt.CompareHashAndPassword([]byte(storedApprovalHash(t, store)), []byte(password)) != nil {
		t.Fatal("stored hash does not verify the password")
	}
}

func TestSetApprovalPasswordRejectsAccountFlag(t *testing.T) {
	store := approvalStore(t)
	if code, _, _ := runSetApproval(store, "private-first-pass\n", nil, "--account", "work"); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if _, err := store.LoadApprovalHash(context.Background()); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal("usage error stored a password")
	}
}

type failingApprovalStore struct {
	credentials.Store
}

func (failingApprovalStore) LoadApprovalHash(context.Context) (string, error) {
	return "", credentials.ErrNotFound
}
func (failingApprovalStore) SaveApprovalHash(context.Context, string) error {
	return errors.New("private-canary backend detail")
}

func TestSetApprovalPasswordReportsStoreFailureWithoutSecrets(t *testing.T) {
	t.Parallel()
	code, out, stderr := runSetApproval(failingApprovalStore{}, "private-first-pass\n", nil)
	if code != 1 || out != "" || stderr != "error: credential save failed\n" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

func TestSetApprovalPasswordReadsLinesAfterTheFilePassword(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	first, err := bcrypt.GenerateFromPassword([]byte("private-first-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	// A low scrypt work factor keeps the fixture fast to read; the command writes with the production factor.
	var encrypted bytes.Buffer
	r, _ := age.NewScryptRecipient("synthetic-password")
	r.SetWorkFactor(1)
	w, err := age.Encrypt(&encrypted, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, `{"version":1,"oauth":{},"openrouter_key":"","approval_hash":"`+string(first)+`"}`); err != nil || w.Close() != nil {
		t.Fatal("write fixture")
	}
	if os.WriteFile(filepath.Join(home, "credentials.age"), encrypted.Bytes(), 0600) != nil {
		t.Fatal("file fixture")
	}
	var out, stderr bytes.Buffer
	input := "synthetic-password\nprivate-first-pass\nprivate-second-pass\n"
	code := cli.Run(context.Background(), []string{"auth", "set-approval-password", "--home", home}, &out, &stderr, cli.Options{Stdin: strings.NewReader(input), LookupEnv: func(string) string { return "" }})
	if code != 0 || strings.Contains(stderr.String(), "private-") {
		t.Fatalf("change: exit=%d stderr=%q", code, stderr.String())
	}
	hash, err := credentials.NewFile(home, "synthetic-password").LoadApprovalHash(context.Background())
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("private-second-pass")) != nil {
		t.Fatalf("file backend hash: %v", err)
	}
}
