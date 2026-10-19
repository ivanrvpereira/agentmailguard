package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

const approvalNotSet = "not set; to turn on the approval console, run: agentmailguard auth set-approval-password, then restart the server"

func TestPreloadReportsTheApprovalPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("private-approval"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, record, want string
	}{
		{"set", `{"version":1,"oauth":{},"openrouter_key":"key","approval_hash":"` + string(hash) + `"}`, "ok"},
		{"unset", `{"version":1,"oauth":{},"openrouter_key":"key"}`, approvalNotSet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := twoAccountHome(t)
			store := credentials.New(&recordPlatform{data: []byte(tc.record)}, home, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, err := server.New(ctx, home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			checks, err := s.Preload(ctx, nil)
			if err != nil {
				t.Fatalf("preload failed: %v", err)
			}
			want := server.CredentialCheck{Item: "approval password", Status: tc.want}
			if checks[len(checks)-1] != want {
				t.Fatalf("checks=%v", checks)
			}
			for _, c := range checks {
				if strings.Contains(c.Item+c.Status, "$2a$") || strings.Contains(c.Item+c.Status, "private-") {
					t.Fatal("check exposed the hash")
				}
			}
		})
	}
}
