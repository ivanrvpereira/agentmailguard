package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type keyStore struct {
	credentials.Store
	saved   []string
	saveErr error
}

func (s *keyStore) SaveOpenRouterKey(_ context.Context, key string) error {
	s.saved = append(s.saved, key)
	return s.saveErr
}

func runSetKey(input string, store *keyStore, env map[string]string, args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	opts := cli.Options{Stdin: strings.NewReader(input), LookupEnv: func(k string) string { return env[k] }, Server: server.Options{Credentials: store}}
	code := cli.Run(context.Background(), append(args, "auth", "set-jev-key"), &out, &stderr, opts)
	return code, out.String(), stderr.String()
}

func TestSetJevKeySavesTrimmedKeyFromStdin(t *testing.T) {
	store := &keyStore{}
	code, out, stderr := runSetKey("  sk-or-private-canary \n", store, nil)
	if code != 0 || out != "" || len(store.saved) != 1 || store.saved[0] != "sk-or-private-canary" {
		t.Fatalf("exit=%d saved=%q stderr=%q", code, store.saved, stderr)
	}
	if strings.Contains(stderr, "private-canary") || !strings.Contains(stderr, "Jev key saved") {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}

func TestSetJevKeyRefusesUnsafeInputWithoutSaving(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		env         map[string]string
		args        []string
		want        string
	}{
		{name: "empty", input: " \n", want: "error: Jev key is empty\n"},
		{name: "eof", input: "", want: "error: Jev key is empty\n"},
		{name: "inner space", input: "sk-or private-canary\n", want: "error: Jev key must be one line without spaces or control characters\n"},
		{name: "control", input: "sk-or\x1bprivate-canary\n", want: "error: Jev key must be one line without spaces or control characters\n"},
		{name: "too long", input: "private-canary" + strings.Repeat("k", 4096), want: "error: Jev key is too long\n"},
		{name: "two lines", input: "sk-or-a\nprivate-canary\n", want: "error: Jev key must be one line without spaces or control characters\n"},
		{name: "remote flag", input: "sk-or-private-canary\n", args: []string{"--server", "http://127.0.0.1:1"}, want: "error: Jev key setup is local only; remove --server and AGENTMAILGUARD_SERVER\n"},
		{name: "remote env", input: "sk-or-private-canary\n", env: map[string]string{"AGENTMAILGUARD_SERVER": "http://127.0.0.1:1"}, want: "error: Jev key setup is local only; remove --server and AGENTMAILGUARD_SERVER\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &keyStore{}
			code, out, stderr := runSetKey(tc.input, store, tc.env, tc.args...)
			if code != 1 || out != "" || len(store.saved) != 0 || !strings.HasSuffix(stderr, tc.want) || strings.Contains(stderr, "private-canary") {
				t.Fatalf("exit=%d saved=%q stderr=%q", code, store.saved, stderr)
			}
		})
	}
}

func TestSetJevKeyRejectsAccountFlag(t *testing.T) {
	store := &keyStore{}
	if code, _, _ := runSetKey("sk-or-key\n", store, nil, "--account", "work"); code != 2 || len(store.saved) != 0 {
		t.Fatalf("exit=%d saved=%q", code, store.saved)
	}
}

func TestSetJevKeyReportsStoreFailureWithoutKey(t *testing.T) {
	store := &keyStore{saveErr: errors.New("private-canary backend detail")}
	code, out, stderr := runSetKey("sk-or-private-canary\n", store, nil)
	if code != 1 || out != "" || !strings.HasSuffix(stderr, "error: credential save failed\n") || strings.Contains(stderr, "private-canary") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}
