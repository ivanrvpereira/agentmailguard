package cli_test

import (
	"bytes"
	"context"
	"filippo.io/age"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func credentialFileFixture(t *testing.T, home string) []byte {
	t.Helper()
	var encrypted bytes.Buffer
	r, _ := age.NewScryptRecipient("private-old-password")
	r.SetWorkFactor(1)
	w, err := age.Encrypt(&encrypted, r)
	if err != nil {
		t.Fatal("encrypt fixture")
	}
	if _, err := io.WriteString(w, `{"version":1,"oauth":{"gmail/work":{"refresh_token":"private-token"}},"openrouter_key":"private-key"}`); err != nil || w.Close() != nil {
		t.Fatal("write fixture")
	}
	data := encrypted.Bytes()
	if os.WriteFile(filepath.Join(home, "credentials.age"), data, 0600) != nil {
		t.Fatal("file fixture")
	}
	return data
}

func TestCredentialCommandsRefuseRemoteAccountAndPasswordArguments(t *testing.T) {
	for _, command := range []string{"migrate", "change-password"} {
		for _, mode := range []string{"server", "empty-server", "server-env", "account", "empty-account", "password-flag", "positional-password"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				home := fileHome(t)
				args := []string{"auth", command, "--home", home}
				if command == "migrate" {
					args = append(args, "--to", "keychain")
				}
				switch mode {
				case "server":
					args = append(args, "--server", "http://127.0.0.1:1")
				case "empty-server":
					args = append(args, "--server=")
				case "account":
					args = append(args, "--account", "work")
				case "empty-account":
					args = append([]string{"--account="}, args...)
				case "password-flag":
					args = append(args, "--password", "private-canary")
				case "positional-password":
					args = append(args, "private-canary")
				}
				var out, stderr bytes.Buffer
				opts := cli.Options{Stdin: strings.NewReader(""), LookupEnv: func(k string) string {
					if mode == "server-env" && k == "AGENTMAILGUARD_SERVER" {
						return "http://127.0.0.1:1"
					}
					return ""
				}}
				if cli.Run(context.Background(), args, &out, &stderr, opts) == 0 {
					t.Fatal("unsafe command accepted")
				}
				if strings.Contains(out.String()+stderr.String(), "private-canary") {
					t.Fatal("argument leaked")
				}
				if _, err := os.Stat(filepath.Join(home, "credentials.lock")); !os.IsNotExist(err) {
					t.Fatal("refused command accessed credentials")
				}
			})
		}
	}
}

func TestCredentialCommandsRefuseInvalidBackendAndNonterminalChange(t *testing.T) {
	for _, mode := range []string{"same", "invalid", "missing-target", "keychain-password", "nonterminal-password"} {
		t.Run(mode, func(t *testing.T) {
			home := fileHome(t)
			args := []string{"auth", "migrate", "--home", home}
			switch mode {
			case "same":
				args = append(args, "--to", "file")
			case "invalid":
				args = append(args, "--to", "private-canary")
			case "keychain-password":
				path := filepath.Join(home, "config.toml")
				data, _ := os.ReadFile(path)
				data = bytes.Replace(data, []byte("backend='file'"), []byte("backend='keychain'"), 1)
				if os.WriteFile(path, data, 0600) != nil {
					t.Fatal("config")
				}
				args = []string{"auth", "change-password", "--home", home}
			case "nonterminal-password":
				args = []string{"auth", "change-password", "--home", home}
			}
			var out, stderr bytes.Buffer
			if cli.Run(context.Background(), args, &out, &stderr, cli.Options{Stdin: strings.NewReader("private-old\nprivate-new\nprivate-new\n"), LookupEnv: func(string) string { return "" }}) == 0 {
				t.Fatal("unsafe command accepted")
			}
			if strings.Contains(out.String()+stderr.String(), "private-") {
				t.Fatal("secret output")
			}
			if _, err := os.Stat(filepath.Join(home, "credentials.lock")); !os.IsNotExist(err) {
				t.Fatal("refusal accessed credential backend")
			}
		})
	}
}

func TestMigrateFileToKeychainUsesStdinAndPreservesSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home := fileHome(t)
	credentialFileFixture(t, home)
	before, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	configBefore, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	p := &localPlatform{values: map[[2]string][]byte{}}
	var out, stderr bytes.Buffer
	code := cli.Run(ctx, []string{"--home", home, "auth", "migrate", "--to", "keychain"}, &out, &stderr, cli.Options{CredentialPlatform: p, Stdin: strings.NewReader("private-old-password\n"), LookupEnv: func(string) string { return "" }})
	if code != 0 {
		t.Fatalf("migration failed: %s", stderr.String())
	}
	target := credentials.New(p, home, nil)
	if key, err := target.LoadScreeningKey(ctx); err != nil || key != "private-key" {
		t.Fatal("key lost")
	}
	if token, err := target.LoadOAuth(ctx, "gmail", "work"); err != nil || token.RefreshToken != "private-token" {
		t.Fatal("token lost")
	}
	after, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	configAfter, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if !bytes.Equal(before, after) || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("source or config changed")
	}
	if strings.Contains(out.String()+stderr.String(), "private-") || !strings.Contains(out.String(), "backend = \"keychain\"") {
		t.Fatal("unsafe or missing instruction")
	}
}

func TestMigrateCommandCopiesLegacyAndKeepsConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	home := fileHome(t)
	path := filepath.Join(home, "config.toml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original = bytes.Replace(original, []byte("backend='file'"), []byte("backend='keychain'"), 1)
	if os.WriteFile(path, original, 0600) != nil {
		t.Fatal("config fixture")
	}
	p := &localPlatform{values: map[[2]string][]byte{{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte("private-legacy-key")}}
	opts := cli.Options{CredentialPlatform: p, Stdin: strings.NewReader(""), LookupEnv: func(k string) string {
		if k == credentials.PasswordEnvironment {
			return "private-file-password"
		}
		return ""
	}}
	var out, stderr bytes.Buffer
	code := cli.Run(ctx, []string{"auth", "migrate", "--to", "file", "--home", home}, &out, &stderr, opts)
	if code != 0 {
		t.Fatalf("migration failed: %s", stderr.String())
	}
	if key, err := credentials.NewFile(home, "private-file-password").LoadScreeningKey(ctx); err != nil || key != "private-legacy-key" {
		t.Fatal("target lost key or target password env was discarded")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("config changed")
	}
	if string(p.values[[2]string{"agentmailguard-go-openrouter", "agentmailguard-go"}]) != "private-legacy-key" {
		t.Fatal("legacy deleted")
	}
	text := out.String() + stderr.String()
	if strings.Contains(text, "private-") || !strings.Contains(text, "[credentials]\nbackend = \"file\"") || !strings.Contains(text, "Restart") {
		t.Fatal("unsafe or incomplete instructions")
	}
	before, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	if cli.Run(ctx, []string{"auth", "migrate", "--to", "file", "--home", home}, &out, &stderr, opts) == 0 {
		t.Fatal("existing target overwritten")
	}
	after, _ = os.ReadFile(filepath.Join(home, "credentials.age"))
	if !bytes.Equal(before, after) {
		t.Fatal("target changed")
	}
}
