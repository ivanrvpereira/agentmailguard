package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
)

// mcpFake is a /v1/call server that records the operation and account of every call.
type mcpFake struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string
}

func newMCPFake(t *testing.T) *mcpFake {
	t.Helper()
	f := &mcpFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Operation, Account string }
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, req.Operation+" "+req.Account)
		f.mu.Unlock()
		io.WriteString(w, `[{"id":"f1","name":"Inbox"}]`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *mcpFake) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// mcpRun is one mcp command running over pipes, with an SDK client on the other ends.
type mcpRun struct {
	stdin  *io.PipeWriter // the client's writer; closing it is stdin EOF
	stdout *io.PipeReader
	stderr bytes.Buffer
	done   chan int
}

func startMCP(ctx context.Context, t *testing.T, args []string, lookup func(string) string) *mcpRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	m := &mcpRun{stdin: inW, stdout: outR, done: make(chan int, 1)}
	go func() {
		m.done <- cli.Run(ctx, args, outW, &m.stderr, cli.Options{Stdin: inR, LookupEnv: lookup})
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	return m
}

func (m *mcpRun) connect(ctx context.Context, t *testing.T) *mcp.ClientSession {
	t.Helper()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, &mcp.IOTransport{Reader: m.stdout, Writer: m.stdin}, nil)
	if e != nil {
		t.Fatal(e)
	}
	return cs
}

func (m *mcpRun) exit(ctx context.Context, t *testing.T) int {
	t.Helper()
	select {
	case code := <-m.done:
		return code
	case <-ctx.Done():
		t.Fatal("mcp did not exit")
		return -1
	}
}

func noEnv(string) string { return "" }

func TestMCPServesToolsOverStdioUntilTheClientCloses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		env     bool // the server URL comes from AGENTMAILGUARD_GO_SERVER
		account string
	}{
		{"server option", nil, false, ""},
		{"server environment", nil, true, ""},
		{"default account", []string{"--account", "work"}, false, "work"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f := newMCPFake(t)
			args := append([]string{"mcp"}, tc.args...)
			lookup := noEnv
			if tc.env {
				lookup = func(k string) string {
					if k == "AGENTMAILGUARD_GO_SERVER" {
						return f.URL
					}
					return ""
				}
			} else {
				args = append(args, "--server", f.URL)
			}
			m := startMCP(ctx, t, args, lookup)
			cs := m.connect(ctx, t)
			tools, e := cs.ListTools(ctx, nil)
			if e != nil || len(tools.Tools) != 11 {
				t.Fatalf("tools/list error %v", e)
			}
			res, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mail_folders"})
			if e != nil || res.IsError {
				t.Fatalf("mail_folders error %v result %+v", e, res)
			}
			if calls := f.recorded(); len(calls) != 1 || calls[0] != "mail.folders "+tc.account {
				t.Errorf("server calls %q, want mail.folders for account %q", calls, tc.account)
			}
			cs.Close()
			if code := m.exit(ctx, t); code != 0 || m.stderr.Len() != 0 {
				t.Errorf("exit %d stderr %q", code, m.stderr.String())
			}
		})
	}
}

func TestMCPServesTheWriteToolsOnlyWithAllowWrites(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		count int
		trash bool
	}{
		{nil, 11, false},
		{[]string{"--allow-writes"}, 22, true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m := startMCP(ctx, t, append([]string{"mcp", "--server", newMCPFake(t).URL}, tc.args...), noEnv)
		cs := m.connect(ctx, t)
		tools, e := cs.ListTools(ctx, nil)
		if e != nil {
			t.Fatalf("%v: tools/list error %v", tc.args, e)
		}
		trash := false
		for _, tool := range tools.Tools {
			trash = trash || tool.Name == "mail_trash"
		}
		if len(tools.Tools) != tc.count || trash != tc.trash {
			t.Errorf("%v: tools %d mail_trash %t, want %d %t", tc.args, len(tools.Tools), trash, tc.count, tc.trash)
		}
		cs.Close()
		if code := m.exit(ctx, t); code != 0 || m.stderr.Len() != 0 {
			t.Errorf("%v: exit %d stderr %q", tc.args, code, m.stderr.String())
		}
		cancel()
	}
}

func TestMCPExitsCleanlyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, stop := context.WithCancel(ctx)
	m := startMCP(run, t, []string{"mcp", "--server", newMCPFake(t).URL}, noEnv)
	cs := m.connect(ctx, t)
	defer cs.Close()
	stop()
	if code := m.exit(ctx, t); code != 0 || m.stderr.Len() != 0 {
		t.Errorf("exit %d stderr %q", code, m.stderr.String())
	}
}

func TestMCPAppliesTheTimeoutToEachCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the server notices the client going away only after the body is read
		<-r.Context().Done()
	}))
	defer hung.Close()
	m := startMCP(ctx, t, []string{"mcp", "--server", hung.URL, "--timeout", "50ms"}, noEnv)
	cs := m.connect(ctx, t)
	defer cs.Close()
	res, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mail_folders"})
	if e != nil || !res.IsError || len(res.Content) != 1 || res.Content[0].(*mcp.TextContent).Text != "server request failed" {
		t.Fatalf("error %v result %+v, want the tool error server request failed", e, res)
	}
}

func TestMCPTransportFailureExitsOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := startMCP(ctx, t, []string{"mcp", "--server", newMCPFake(t).URL}, noEnv)
	go io.Copy(io.Discard, m.stdout)
	go io.WriteString(m.stdin, "not-json canary\n")
	if code := m.exit(ctx, t); code != 1 || m.stderr.String() != "error: mcp transport failed\n" {
		t.Errorf("exit %d stderr %q", code, m.stderr.String())
	}
}

func TestMCPRejectedCommandLines(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"mcp", "--home", "canary"}, 2, "error: invalid command usage; use --help\n"},
		{[]string{"mcp", "extra"}, 2, "error: invalid command usage; use --help\n"},
		{[]string{"mcp", "--timeout", "0s"}, 2, "error: invalid command usage; use --help\n"},
		{[]string{"mcp", "--server", ":bad"}, 1, "error: invalid server URL\n"},
		{[]string{"mcp", "--account", " "}, 1, "error: account must not be blank\n"},
	} {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), tc.args, &out, &errout, cli.Options{Stdin: strings.NewReader(""), LookupEnv: noEnv})
		if code != tc.code || out.Len() != 0 || errout.String() != tc.stderr {
			t.Errorf("%v: exit %d stdout %q stderr %q", tc.args, code, out.String(), errout.String())
		}
	}
}

func TestMCPAppearsInHelp(t *testing.T) {
	code, out, _ := runOffline(t, "--help")
	if code != 0 || !strings.Contains(out, "mcp") || !strings.Contains(out, "Serve MCP tools over stdio for an AI agent") {
		t.Errorf("exit %d help %q", code, out)
	}
	code, out, _ = runOffline(t, "mcp", "--help")
	if code != 0 || !strings.Contains(out, "--allow-writes") || !strings.Contains(out, "also serve the mail change and draft tools") {
		t.Errorf("exit %d mcp help %q", code, out)
	}
}
