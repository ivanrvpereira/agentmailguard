package server_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

type failedPreload struct {
	credentials.Store
	err error
}

func (s failedPreload) Preload(context.Context) (credentials.Record, error) {
	return credentials.Record{}, s.err
}

func TestCredentialStoreCheckFirst(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status string
	}{
		{nil, "ok"}, {credentials.ErrUnavailable, "access denied"}, {credentials.ErrDecrypt, "wrong password or damaged file"}, {credentials.ErrPasswordMissing, "password missing"}, {credentials.ErrUnsupportedVersion, "unsupported version"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			home := twoAccountHome(t)
			f, _ := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
			_, err := f.WriteString("\n[credentials]\nbackend='file'\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			s, err := server.New(context.Background(), home, server.Options{Credentials: failedPreload{err: tc.err}, LookupEnv: func(string) string { return "synthetic-env-key" }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			checks, err := s.Preload(context.Background(), nil)
			if len(checks) == 0 || checks[0] != (server.CredentialCheck{Item: "credentials (file)", Status: tc.status}) {
				t.Fatal("first store check missing or incorrect")
			}
			if (err != nil) != (tc.err != nil) {
				t.Fatal("store failure did not control startup")
			}
		})
	}
}

func TestServerNewConfiguredFileNeverUsesKeychain(t *testing.T) {
	home := twoAccountHome(t)
	f, _ := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
	_, err := f.WriteString("\n[credentials]\nbackend='file'\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(context.Background(), home, server.Options{LookupEnv: func(name string) string {
		if name == "OPENROUTER_API_KEY" {
			return "synthetic-env-key"
		}
		return ""
	}})
	if err != nil {
		t.Fatal("file constructor failed")
	}
	defer s.Close()
	checks, err := s.Preload(context.Background(), nil)
	if err == nil || len(checks) != 1 || checks[0].Status != "password missing" {
		t.Fatal("missing file password bypassed")
	}
}

func TestCredentialBackendTableType(t *testing.T) {
	for _, value := range []string{"'file'", "1", "true", "['file']"} {
		home := twoAccountHome(t)
		path := filepath.Join(home, "config.toml")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if os.WriteFile(path, append([]byte("credentials="+value+"\n"), original...), 0600) != nil {
			t.Fatal("fixture write")
		}
		s, err := server.New(context.Background(), home, server.Options{Credentials: failedPreload{}})
		if err == nil {
			defer s.Close()
			_, err = s.Preload(context.Background(), nil)
		}
		if err == nil || err.Error() != "configuration unavailable" {
			t.Fatal("invalid credentials table type accepted")
		}
	}
}

func TestCredentialBackendConfiguration(t *testing.T) {
	for _, setting := range []string{"backend='other'", "backend=1", "backend=['file']", "backend='file'\nunknown=true", "unknown=true", ""} {
		t.Run(setting, func(t *testing.T) {
			home := twoAccountHome(t)
			f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n[credentials]\n" + setting + "\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			s, err := server.New(context.Background(), home, server.Options{Credentials: credentials.New(&recordPlatform{}, home, nil), LookupEnv: func(string) string { return "synthetic-env-key" }})
			if err == nil {
				defer s.Close()
				_, err = s.Preload(context.Background(), nil)
			}
			if err == nil || err.Error() != "configuration unavailable" {
				t.Fatal("invalid credentials configuration accepted")
			}
		})
	}
}
