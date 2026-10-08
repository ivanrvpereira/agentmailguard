package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type panicScreener struct{}

func (panicScreener) Screen(context.Context, map[string]any) (model.Screening, error) {
	panic("private-adapter-panic-canary")
}
func (panicScreener) Classify(context.Context, map[string]any, bool, *model.OrganizationQuestions) (model.Classification, error) {
	panic("private-adapter-panic-canary")
}
func (panicScreener) CheckAction(context.Context, string, string, map[string]any) (model.ActionCheck, error) {
	panic("private-adapter-panic-canary")
}
func TestExternalAdapterPanicsDoNotEscapeHTTP(t *testing.T) {
	for _, mode := range []string{"credential", "screening", "transport", "action check"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestExternalPanicChild$")
		cmd.Env = append(os.Environ(), "AGENTMAILGUARD_PANIC_CHILD="+mode)
		b, e := cmd.CombinedOutput()
		if e != nil || strings.Contains(string(b), "private-adapter-panic-canary") {
			t.Errorf("%s: request escaped recovery or exposed panic payload", mode)
		}
	}
}
func TestExternalPanicChild(t *testing.T) {
	mode := os.Getenv("AGENTMAILGUARD_PANIC_CHILD")
	if mode == "" {
		t.Skip("subprocess only")
	}
	home := t.TempDir()
	configFile(t, home)
	api := newNativeAPI()
	graph := httptest.NewServer(http.HandlerFunc(api.graph))
	defer graph.Close()
	opts := server.Options{Credentials: &nativeStore{}, Screener: panicScreener{}, M365: m365.Options{BaseURL: graph.URL}}
	if mode == "credential" {
		opts.Credentials = &hostileStore{mode: "panic"}
	}
	if mode == "transport" {
		opts.M365.HTTPClient = &http.Client{Transport: panicTransport{}}
	}
	s, e := server.New(context.Background(), home, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	body := `{"operation":"mail.list","params":{}}`
	switch mode {
	case "action check":
		// A failed action check holds the draft for approval.
		body = `{"operation":"draft.new","params":{"to":["a@example.test"],"subject":"private-subject","body":"private-body"}}`
	case "credential", "transport":
		body = `{"operation":"mail.get","params":{"id":"g"}}`
	}
	status, b := rpc(t, h.URL, body)
	held := status == 200 && strings.Contains(b, `"status":"pending"`)
	failedClosed := status == 500 || status == 200 && strings.Contains(b, `"verdict":"error"`)
	if mode == "action check" && !held || mode != "action check" && !failedClosed {
		t.Fatal("panic did not fail closed")
	}
	if strings.Contains(b, "private-") {
		t.Fatal("panic response exposed content")
	}
	fmt.Println("safe request recovery")
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("private-adapter-panic-canary")
}
