package config_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

func TestApprovalsDefaultsWithoutTable(t *testing.T) {
	got, err := config.LoadApprovals(context.Background(), file(t, work))
	if err != nil || !reflect.DeepEqual(got, config.Approvals{Listen: "localhost:8788"}) {
		t.Fatalf("approvals=%+v error=%v", got, err)
	}
}

func TestApprovalsReadsListenAndHosts(t *testing.T) {
	path := file(t, "[approvals]\nlisten = '100.64.0.1:9000'\nhosts = ['my-mac.example.ts.net', 'my-mac.example.ts.net:9000', '100.64.0.1', '[fd7a::1]:9000', '[fd7a::1]']\n"+work)
	got, err := config.LoadApprovals(context.Background(), path)
	want := config.Approvals{Listen: "100.64.0.1:9000", Hosts: []string{"my-mac.example.ts.net", "my-mac.example.ts.net:9000", "100.64.0.1", "[fd7a::1]:9000", "[fd7a::1]"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("approvals=%+v error=%v", got, err)
	}
	got, err = config.LoadApprovals(context.Background(), file(t, "[approvals]\nhosts = []\n"+work))
	if err != nil || got.Listen != "localhost:8788" || len(got.Hosts) != 0 {
		t.Fatalf("approvals=%+v error=%v", got, err)
	}
}

func TestInvalidApprovalsNamesKeyPathWithoutValue(t *testing.T) {
	for _, tc := range []struct{ name, text, path string }{
		{"not a table", "approvals = 'SECRET-CANARY'\n", "approvals"},
		{"unknown key", "[approvals]\nlisten_on = 'SECRET-CANARY'\n", "approvals.listen_on"},
		{"odd key", "[approvals]\n\"a.b\\u001b\" = 'SECRET-CANARY'\n", `approvals."a.b\x1b"`},
		{"listen not a string", "[approvals]\nlisten = 8788\n", "approvals.listen"},
		{"listen without port", "[approvals]\nlisten = 'SECRET-CANARY'\n", "approvals.listen"},
		{"listen empty host", "[approvals]\nlisten = ':8788'\n", "approvals.listen"},
		{"listen port zero", "[approvals]\nlisten = 'localhost:0'\n", "approvals.listen"},
		{"listen port too big", "[approvals]\nlisten = 'localhost:65536'\n", "approvals.listen"},
		{"listen named port", "[approvals]\nlisten = 'localhost:http'\n", "approvals.listen"},
		{"listen odd host", "[approvals]\nlisten = 'SECRET/CANARY:8788'\n", "approvals.listen"},
		{"hosts not an array", "[approvals]\nhosts = 'SECRET-CANARY'\n", "approvals.hosts"},
		{"hosts empty entry", "[approvals]\nhosts = ['ok.example', '']\n", "approvals.hosts"},
		{"hosts not a string", "[approvals]\nhosts = [1]\n", "approvals.hosts"},
		{"hosts odd characters", "[approvals]\nhosts = ['SECRET-CANARY/evil']\n", "approvals.hosts"},
		{"hosts bad port", "[approvals]\nhosts = ['SECRET-CANARY:99999']\n", "approvals.hosts"},
		{"hosts empty port", "[approvals]\nhosts = ['SECRET-CANARY:']\n", "approvals.hosts"},
		{"hosts IPv6 without brackets", "[approvals]\nhosts = ['fd7a::1']\n", "approvals.hosts"},
		{"hosts name in brackets", "[approvals]\nhosts = ['[SECRET-CANARY]:9000']\n", "approvals.hosts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadApprovals(context.Background(), file(t, tc.text+work))
			// An exact match also proves that the value is absent.
			if want := "invalid approvals: " + tc.path; err == nil || err.Error() != want {
				t.Fatalf("error=%v, want %q", err, want)
			}
			var typed config.ApprovalsError
			if !errors.As(err, &typed) {
				t.Fatalf("error %T is not an ApprovalsError", err)
			}
		})
	}
}
