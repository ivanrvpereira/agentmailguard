package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
)

const statusID = "0123456789abcdef0123456789abcdef"

// approvalsServer answers approvals.status for statusID with a done result and any other ID with the
// server's not-found refusal. It fails the test on any other request.
func approvalsServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string
			Params    struct{ ID string }
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Operation != "approvals.status" {
			t.Errorf("request %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Params.ID != statusID {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"approval not found"}`)
			return
		}
		io.WriteString(w, `{"state":"done","result":{"id":"w1"}}`)
	}))
	t.Cleanup(h.Close)
	return h
}

func runApprovals(t *testing.T, base string, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"--host", base, "approvals"}, args...), &out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
	return code, out.String(), errout.String()
}

func TestApprovalsStatusPrintsTheState(t *testing.T) {
	h := approvalsServer(t)
	code, out, errout := runApprovals(t, h.URL, "status", statusID)
	want := "{\n  \"state\": \"done\",\n  \"result\": {\n    \"id\": \"w1\"\n  }\n}\n"
	if code != 0 || out != want || errout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errout)
	}
}

func TestApprovalsStatusShowsNotFound(t *testing.T) {
	h := approvalsServer(t)
	code, out, errout := runApprovals(t, h.URL, "status", "ffffffffffffffffffffffffffffffff")
	if code != 1 || out != "" || errout != "error: approval not found\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errout)
	}
}

func TestApprovalsStatusNeedsExactlyOneID(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", statusID, statusID}, {"approve", statusID}, {"deny", statusID}} {
		code, out, errout := runOffline(t, append([]string{"approvals"}, args...)...)
		if code != 2 || out != "" || errout != "error: invalid command usage; use --help\n" {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, errout)
		}
	}
}
