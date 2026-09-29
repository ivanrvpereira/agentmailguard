package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const nativeMCPCanary = "native-mcp-canary"

var (
	nativeReadTools  = []string{"auth_status", "calendar_get", "calendar_list", "mail_categories", "mail_folders", "mail_get", "mail_link", "mail_list", "mail_organize", "mail_search", "rules_list"}
	nativeWriteTools = []string{"draft_forward", "draft_new", "draft_reply", "draft_update", "mail_archive", "mail_categorize", "mail_flag", "mail_move", "mail_read", "mail_trash", "mail_unread"}
)

// newNativeMCPFake is a /v1/call server with fixed answers per operation. It records like newMCPFake.
func newNativeMCPFake(t *testing.T) *mcpFake {
	t.Helper()
	f := &mcpFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Operation, Account string }
		if r.URL.Path != "/v1/call" || json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid request"}`)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Operation+" "+req.Account)
		f.mu.Unlock()
		switch req.Operation {
		case "mail.folders":
			io.WriteString(w, `[{"id":"f1","name":"Inbox"}]`)
		case "mail.archive":
			io.WriteString(w, `[{"id":"m1","ok":true,"error":null}]`)
		case "mail.get":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"`+nativeMCPCanary+`"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid request"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// nativeMCP is one built-binary mcp process with an SDK client connected through CommandTransport.
type nativeMCP struct {
	cs     *mcp.ClientSession
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

// startNativeMCP connects with the given protocol version and fails unless the process negotiated that version.
// A version before 2026-07-28 makes the SDK client use the legacy initialize handshake; 2026-07-28 uses server/discover.
func startNativeMCP(ctx context.Context, t *testing.T, binary, server, protocol string, args ...string) *nativeMCP {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, append([]string{"--server", server, "--account", "work", "mcp"}, args...)...)
	// The child gets an empty temporary home, so it cannot read user configuration.
	home := t.TempDir()
	cmd.Env = []string{"HOME=" + home, "AGENTMAILGUARD_GO_HOME=" + filepath.Join(home, "does-not-exist")}
	n := &nativeMCP{cmd: cmd, stderr: &bytes.Buffer{}}
	cmd.Stderr = n.stderr
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "native-test", Version: "v0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if e != nil {
		t.Fatalf("connect %s: %v", protocol, e)
	}
	n.cs = cs
	if got := cs.InitializeResult().ProtocolVersion; got != protocol {
		cs.Close()
		t.Fatalf("negotiated protocol %q, want %q", got, protocol)
	}
	return n
}

// close closes stdin through the SDK session, which waits for the process, and requires exit 0 with empty stderr.
func (n *nativeMCP) close(t *testing.T) {
	t.Helper()
	e := n.cs.Close()
	if e != nil || n.cmd.ProcessState == nil || n.cmd.ProcessState.ExitCode() != 0 || n.stderr.Len() != 0 {
		t.Errorf("close error %v state %v stderr %q, want exit 0 and empty stderr", e, n.cmd.ProcessState, n.stderr.String())
	}
}

func (n *nativeMCP) toolNames(ctx context.Context, t *testing.T) (map[string]*mcp.Tool, []string) {
	t.Helper()
	res, e := n.cs.ListTools(ctx, nil)
	if e != nil {
		t.Fatalf("tools/list: %v", e)
	}
	byName := map[string]*mcp.Tool{}
	var names []string
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return byName, names
}

func (n *nativeMCP) call(ctx context.Context, t *testing.T, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, e := n.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatalf("%s: %v", name, e)
	}
	return res
}

// requireStructured compares the structured content of a successful result with the expected JSON.
func requireStructured(t *testing.T, name string, res *mcp.CallToolResult, want string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("%s: tool error %+v", name, res.Content)
	}
	got, e := json.Marshal(res.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var g, w any
	json.Unmarshal(got, &g)
	json.Unmarshal([]byte(want), &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: structured content %s, want %s", name, got, want)
	}
}

// requireToolError requires one text block with exactly the given text.
func requireToolError(t *testing.T, name string, res *mcp.CallToolResult, text string) {
	t.Helper()
	if !res.IsError || len(res.Content) != 1 {
		t.Fatalf("%s: result %+v, want one tool error block", name, res)
	}
	if c, ok := res.Content[0].(*mcp.TextContent); !ok || c.Text != text {
		t.Errorf("%s: content %+v, want text %q", name, res.Content[0], text)
	}
}

func TestNativeMCPBinary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentmailguard-go")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agentmailguard-go")
	build.Dir = root
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	fake := newNativeMCPFake(t)
	allTools := append(append([]string{}, nativeReadTools...), nativeWriteTools...)
	sort.Strings(allTools)

	t.Run("legacy initialize, read-only", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n := startNativeMCP(ctx, t, binary, fake.URL, "2025-11-25")
		defer n.close(t)
		if _, names := n.toolNames(ctx, t); !reflect.DeepEqual(names, nativeReadTools) {
			t.Errorf("tools %q, want %q", names, nativeReadTools)
		}
		before := len(fake.recorded())
		requireStructured(t, "mail_folders", n.call(ctx, t, "mail_folders", nil), `{"folders":[{"id":"f1","name":"Inbox"}]}`)
		if calls := fake.recorded()[before:]; !reflect.DeepEqual(calls, []string{"mail.folders work"}) {
			t.Errorf("server calls %q, want mail.folders for account work", calls)
		}
		res := n.call(ctx, t, "mail_get", map[string]any{"id": "m1"})
		requireToolError(t, "mail_get", res, "server request failed")
		if b, _ := json.Marshal(res); strings.Contains(string(b), nativeMCPCanary) {
			t.Errorf("mail_get result shows the server body: %s", b)
		}
		before = len(fake.recorded())
		requireToolError(t, "mail_organize", n.call(ctx, t, "mail_organize", map[string]any{"apply": true}), "writes are disabled; start mcp with --allow-writes")
		if calls := fake.recorded()[before:]; len(calls) != 0 {
			t.Errorf("refused apply made server calls %q", calls)
		}
	})

	t.Run("legacy initialize, allow writes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n := startNativeMCP(ctx, t, binary, fake.URL, "2025-11-25", "--allow-writes")
		defer n.close(t)
		tools, names := n.toolNames(ctx, t)
		if !reflect.DeepEqual(names, allTools) {
			t.Errorf("tools %q, want %q", names, allTools)
		}
		if trash := tools["mail_trash"]; trash == nil || trash.Annotations == nil || trash.Annotations.DestructiveHint == nil || !*trash.Annotations.DestructiveHint || trash.Meta["anthropic/requiresUserInteraction"] != true {
			t.Errorf("mail_trash %+v, want a destructive hint and requiresUserInteraction", trash)
		}
		before := len(fake.recorded())
		requireStructured(t, "mail_archive", n.call(ctx, t, "mail_archive", map[string]any{"ids": []string{"m1"}}), `{"results":[{"id":"m1","ok":true,"error":null}]}`)
		if calls := fake.recorded()[before:]; !reflect.DeepEqual(calls, []string{"mail.archive work"}) {
			t.Errorf("server calls %q, want mail.archive for account work", calls)
		}
	})

	t.Run("2026-07-28 server/discover", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n := startNativeMCP(ctx, t, binary, fake.URL, "2026-07-28")
		defer n.close(t)
		if _, names := n.toolNames(ctx, t); !reflect.DeepEqual(names, nativeReadTools) {
			t.Errorf("tools %q, want %q", names, nativeReadTools)
		}
		before := len(fake.recorded())
		requireStructured(t, "mail_folders", n.call(ctx, t, "mail_folders", map[string]any{"account": "other"}), `{"folders":[{"id":"f1","name":"Inbox"}]}`)
		if calls := fake.recorded()[before:]; !reflect.DeepEqual(calls, []string{"mail.folders other"}) {
			t.Errorf("server calls %q, want mail.folders for account other", calls)
		}
	})
}
