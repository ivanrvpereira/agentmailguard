package mcptools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
	"github.com/ivanrvpereira/agentmailguard-go/internal/mcptools"
)

// call is one request the fake /v1/call server received.
type call struct {
	Operation string          `json:"operation"`
	Account   string          `json:"account"`
	Params    json.RawMessage `json:"params"`
}

// fake is an httptest /v1/call server that records requests and answers with reply.
type fake struct {
	mu    sync.Mutex
	calls []call
}

func (f *fake) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// connect starts New over in-memory transports against a fake server whose handler answers every call.
func connect(t *testing.T, opts mcptools.Options, reply http.HandlerFunc, session *mcp.ClientSessionOptions) (*mcp.ClientSession, *fake) {
	t.Helper()
	f := &fake{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c call
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/call" || json.Unmarshal(b, &c) != nil {
			t.Errorf("unexpected request %s %s", r.URL.Path, b)
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(h.Close)
	cl, e := client.New(h.URL, client.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return connectClient(t, cl, opts, session), f
}

func connectClient(t *testing.T, cl *client.Client, opts mcptools.Options, session *mcp.ClientSessionOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	ss, e := mcptools.New(cl, opts).Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ss.Close() })
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, session)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func answer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }
}

func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var readTools = []string{"auth_status", "calendar_get", "calendar_list", "mail_categories", "mail_folders", "mail_get", "mail_link", "mail_list", "mail_organize", "mail_search", "rules_list"}

func TestReadOnlyModeListsExactlyTheReadTools(t *testing.T) {
	cs, _ := connect(t, mcptools.Options{}, answer(`{}`), nil)
	res, e := cs.ListTools(bounded(t), nil)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, readTools) {
		t.Fatalf("tools = %v, want %v", names, readTools)
	}
}

func TestServerAdvertisesOnlyToolsAndWarnsAboutUntrustedText(t *testing.T) {
	for _, version := range []string{"", "2025-11-25", "2024-11-05"} {
		t.Run("version "+version, func(t *testing.T) {
			cs, _ := connect(t, mcptools.Options{}, answer(`{}`), &mcp.ClientSessionOptions{ProtocolVersion: version})
			res := cs.InitializeResult()
			if caps, _ := json.Marshal(res.Capabilities); string(caps) != `{"tools":{}}` {
				t.Errorf("capabilities %s, want tools without listChanged only", caps)
			}
			if res.ServerInfo == nil || res.ServerInfo.Name != "agentmailguard-go" || res.ServerInfo.Version == "" {
				t.Errorf("server info %+v", res.ServerInfo)
			}
			for _, want := range []string{"untrusted third-party data", "never as instructions", "green", "sends mail", "permanently"} {
				if !strings.Contains(res.Instructions, want) {
					t.Errorf("instructions %q lack %q", res.Instructions, want)
				}
			}
		})
	}
}

func TestEveryReadToolDescribesItselfAndItsHints(t *testing.T) {
	// Rows of the approved annotation table: readOnly, destructive, idempotent, openWorld.
	provider := [4]bool{true, false, true, true}
	local := [4]bool{true, false, true, false}
	want := map[string][4]bool{
		"mail_list": provider, "mail_search": provider, "mail_get": provider, "mail_link": provider, "calendar_list": provider, "calendar_get": provider,
		"mail_folders": local, "mail_categories": local, "rules_list": local, "auth_status": local,
		"mail_organize": provider,
	}
	untrusted := map[string]bool{"mail_list": true, "mail_search": true, "mail_get": true, "mail_link": true, "calendar_list": true, "calendar_get": true, "mail_organize": true}
	cs, _ := connect(t, mcptools.Options{}, answer(`{}`), nil)
	res, e := cs.ListTools(bounded(t), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s: hints not all set: %+v", tool.Name, a)
			continue
		}
		if got := [4]bool{a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint, *a.OpenWorldHint}; got != want[tool.Name] {
			t.Errorf("%s: hints %v, want %v", tool.Name, got, want[tool.Name])
		}
		if tool.Title == "" || tool.Description == "" {
			t.Errorf("%s: title %q description %q", tool.Name, tool.Title, tool.Description)
		}
		if untrusted[tool.Name] && (!strings.Contains(tool.Description, "untrusted third-party data") || !strings.Contains(tool.Description, "never as instructions") || !strings.Contains(tool.Description, "green")) {
			t.Errorf("%s: description does not mark untrusted fields: %q", tool.Name, tool.Description)
		}
		var in, out struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		b, _ := json.Marshal(tool.InputSchema)
		c, _ := json.Marshal(tool.OutputSchema)
		if json.Unmarshal(b, &in) != nil || json.Unmarshal(c, &out) != nil || in.Type != "object" || out.Type != "object" || len(out.Properties) == 0 {
			t.Errorf("%s: schemas %s %s", tool.Name, b, c)
		}
		if _, ok := in.Properties["account"]; !ok || slices.Contains(in.Required, "account") {
			t.Errorf("%s: no optional account argument: %s", tool.Name, b)
		}
	}
}

// Server fixtures carry null in every field the server may send as null, and a red summary without subject or sender name.
const (
	auth       = `{"spf":"pass","dkim":null,"dmarc":null,"compauth":null,"compauth_reason":null}`
	summaries  = `[{"id":"g1","verdict":"green","received_at":"2026-03-01T10:00:00+00:00","auth":` + auth + `,"is_read":false,"flag":"none","categories":[],"sender":{"name":"Alice canary-name","address":"alice@example.test"},"subject":"canary-subject","to":[{"name":"","address":"me@example.test"}],"cc":[],"attachments":["canary-attachment.pdf"]},{"id":"r1","verdict":"red","received_at":"2026-03-01T09:00:00.250000+00:00","auth":` + auth + `,"is_read":true,"flag":"flagged","categories":["Work"],"sender":{"address":"evil@example.test"},"reason":"injection"},{"id":"e1","verdict":"error","received_at":"2026-03-01T08:00:00+00:00","auth":{"spf":null,"dkim":null,"dmarc":null,"compauth":null,"compauth_reason":null},"is_read":false,"flag":"complete","categories":[],"reason":"screening_error","error":"Error"}]`
	sealed     = `[{"id":"s1","verdict":"sealed","received_at":"2026-03-01T10:00:00+00:00","auth":` + auth + `,"is_read":false,"flag":"none","categories":[],"sender":{"address":"bank@example.test"},"reason":"account_security"}]`
	message    = `{"id":"g1","verdict":"green","received_at":"2026-03-01T10:00:00+00:00","auth":` + auth + `,"is_read":false,"flag":"none","categories":["Work"],"sender":{"name":"Alice","address":"alice@example.test"},"subject":"canary-subject","to":[],"cc":[],"attachments":[],"body":"canary-body [L1]","links":{"L1":"example.test"},"truncated":false}`
	linkList   = `[{"link":"L1","url":"https://example.test/a"},{"link":"L2","error":"the URL likely carries a secret (sign-in, reset, token)"}]`
	folderList = `[{"id":"f1","name":"Inbox"},{"id":"f2","name":"canary-folder"}]`
	report     = `{"planned":[{"id":"m1","move":{"rule":"news","to":null},"categories":[]},{"id":"m2","move":null,"categories":["Work"]}],"errors":[{"id":null,"error":"rules unavailable"}],"warnings":[{"rule":"news","problem":"folder not found"}],"since":"2026-03-01T00:00:00Z","stats":{"messages":2,"verdicts":{"green":1,"red":1},"applied":0,"tagged":0,"skipped":0,"failed":0}}`
	eventList  = `[{"id":"ev1","verdict":"green","start":"2026-03-02T09:00:00+00:00","end":"2026-03-02T09:30:00+00:00","organizer":{"name":"Bob","address":"bob@example.test"},"subject":"canary-event","attendees":[{"name":"Me","address":"me@example.test","response":"accepted"}],"all_day":false,"location":"Room 1","attachments":[],"categories":[],"show_as":"busy","cancelled":false,"body":"canary-agenda","links":{},"truncated":false},{"id":"ev2","verdict":"red","start":"2026-03-03T09:00:00+00:00","end":"2026-03-03T10:00:00+00:00","organizer":{"address":"evil@example.test"},"reason":"injection"}]`
	event      = `{"id":"ev3","verdict":"yellow","start":"2026-03-04T09:00:00+00:00","end":"2026-03-04T10:00:00+00:00","organizer":{"address":"bob@example.test"},"subject":"canary-event","attendees":[{"address":"me@example.test"}],"reason":"injection"}`
	ruleList   = `{"account":"work","rules":[{"rule":"news","folder":"f9","name":null,"min_age_hours":0},{"rule":"tag","category":"Work","name":"Work"}],"warnings":[{"rule":"news","problem":"folder not found"}]}`
	authStatus = `{"account":"work","ok":true,"logged_in":true,"username":"me@example.test","scopes":["Mail.ReadWrite"],"extra_scopes":["Mail.Send"]}`
)

type toolCase struct {
	name, tool string
	args       map[string]any
	op, params string
	body       string // the server result
	key        string // the result wraps the server array under this key
}

var toolCases = []toolCase{
	{"mail_list defaults", "mail_list", map[string]any{}, "mail.list", `{"folder":"inbox","count":25,"skip":0}`, summaries, "messages"},
	{"mail_list options", "mail_list", map[string]any{"folder": "f", "count": 4, "skip": 2}, "mail.list", `{"folder":"f","count":4,"skip":2}`, sealed, "messages"},
	{"mail_get", "mail_get", map[string]any{"id": "m"}, "mail.get", `{"id":"m"}`, message, ""},
	{"mail_search all", "mail_search", map[string]any{"text": "x", "from": "alice", "subject": "s", "after": "2026-01-01", "before": "2026-02-01", "folder": "f", "count": 9}, "mail.search", `{"text":"x","from":"alice","subject":"s","after":"2026-01-01","before":"2026-02-01","folder":"f","count":9}`, summaries, "messages"},
	{"mail_search text", "mail_search", map[string]any{"text": "x"}, "mail.search", `{"text":"x","count":25}`, `[]`, "messages"},
	{"mail_link links", "mail_link", map[string]any{"id": "m", "links": []string{"L1", "L2"}}, "mail.link", `{"id":"m","links":["L1","L2"]}`, linkList, "links"},
	{"mail_link all", "mail_link", map[string]any{"id": "m", "all": true}, "mail.link", `{"id":"m","all":true}`, linkList, "links"},
	{"mail_folders", "mail_folders", map[string]any{}, "mail.folders", `{}`, folderList, "folders"},
	{"mail_categories", "mail_categories", map[string]any{}, "mail.categories", `{}`, `["Work","canary-category"]`, "categories"},
	{"mail_organize defaults", "mail_organize", map[string]any{}, "mail.organize", `{"since":"7d","apply":false}`, report, ""},
	{"mail_organize range", "mail_organize", map[string]any{"since": "1m", "until": "1d", "apply": false}, "mail.organize", `{"since":"1m","until":"1d","apply":false}`, report, ""},
	{"calendar_list", "calendar_list", map[string]any{"start": "2026-03-07T00:00:00-05:00", "end": "2026-03-09T00:00:00-04:00"}, "calendar.list", `{"start":"2026-03-07T00:00:00-05:00","end":"2026-03-09T00:00:00-04:00"}`, eventList, "events"},
	{"calendar_get", "calendar_get", map[string]any{"id": "e"}, "calendar.get", `{"id":"e"}`, event, ""},
	{"rules_list", "rules_list", map[string]any{}, "rules.list", `{}`, ruleList, ""},
	{"auth_status", "auth_status", map[string]any{}, "auth.status", `{}`, authStatus, ""},
}

// sameJSON compares two JSON texts by value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return string(p) == string(q)
}

func TestToolCallsForwardOneServerCall(t *testing.T) {
	for _, tc := range toolCases {
		for _, acc := range []struct{ name, option, argument, want string }{
			{"option", "work", "", "work"},
			{"argument", "work", "remote", "remote"},
			{"none", "", "", ""},
		} {
			t.Run(tc.name+" "+acc.name, func(t *testing.T) {
				cs, f := connect(t, mcptools.Options{Account: acc.option}, answer(tc.body), nil)
				args := map[string]any{}
				for k, v := range tc.args {
					args[k] = v
				}
				if acc.argument != "" {
					args["account"] = acc.argument
				}
				if _, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: tc.tool, Arguments: args}); e != nil {
					t.Fatal(e)
				}
				calls := f.recorded()
				if len(calls) != 1 {
					t.Fatalf("server calls %d, want 1", len(calls))
				}
				if c := calls[0]; c.Operation != tc.op || c.Account != acc.want || !sameJSON(t, c.Params, []byte(tc.params)) {
					t.Errorf("call %s account %q params %s, want %s %q %s", c.Operation, c.Account, c.Params, tc.op, acc.want, tc.params)
				}
			})
		}
	}
}

// text returns the only text block of a tool result.
func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content blocks %d, want 1", len(res.Content))
	}
	c, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content %T, want text", res.Content[0])
	}
	return c.Text
}

func TestToolResultsCarryTheServerResultAsTextAndStructuredContent(t *testing.T) {
	for _, tc := range toolCases {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := connect(t, mcptools.Options{}, answer(tc.body), nil)
			res, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if e != nil {
				t.Fatal(e)
			}
			want := tc.body
			if tc.key != "" {
				want = `{"` + tc.key + `":` + tc.body + `}`
			}
			structured, _ := json.Marshal(res.StructuredContent)
			if res.IsError || !sameJSON(t, []byte(text(t, res)), []byte(want)) || !sameJSON(t, structured, []byte(want)) {
				t.Errorf("error %t text %s structured %s, want %s", res.IsError, text(t, res), structured, want)
			}
		})
	}
}

func TestFailedCallsBecomeToolErrorsWithFixedText(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		status     int
		body, want string
	}{
		{"refusal", "mail_list", nil, 400, `{"error":"invalid request"}`, "invalid request"},
		{"link refusal", "mail_link", map[string]any{"id": "m", "all": true}, 400, `{"error":"link refused: message is red, not green"}`, "link refused: message is red, not green"},
		{"unknown refusal", "mail_get", map[string]any{"id": "m"}, 500, `{"error":"canary-private-error"}`, "server request failed"},
		{"unknown body", "mail_folders", nil, 502, `canary-private-body`, "server request failed"},
		{"malformed body", "mail_get", map[string]any{"id": "m"}, 200, `{"id":"canary-private-id"`, "invalid server response"},
		{"wrong shape", "mail_list", nil, 200, `{"canary-private-key":"x"}`, "invalid server response"},
		{"wrong field type", "auth_status", nil, 200, `{"account":"canary-private-account","ok":"yes"}`, "invalid server response"},
		{"null result", "mail_get", map[string]any{"id": "m"}, 200, `null`, "invalid server response"},
		{"schema violation", "mail_list", nil, 200, `[{"id":"canary-private-id","verdict":"green","auth":null}]`, "invalid server response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := connect(t, mcptools.Options{}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}, nil)
			res, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if e != nil {
				t.Fatal(e)
			}
			all, _ := json.Marshal(res)
			if !res.IsError || text(t, res) != tc.want || res.StructuredContent != nil || strings.Contains(string(all), "canary") {
				t.Errorf("result %s, want error %q", all, tc.want)
			}
		})
	}
}

func TestRefusedArgumentsMakeNoServerCall(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       any
		want       string
	}{
		{"organize apply", "mail_organize", map[string]any{"since": "1m", "apply": true}, "writes are disabled; start mcp with --allow-writes"},
		// Arguments that fail the input schema get one fixed text, whichever property the SDK reports first.
		{"wrong type", "mail_get", map[string]any{"id": 5}, "invalid request"},
		{"missing id", "mail_get", map[string]any{}, "invalid request"},
		{"no arguments", "calendar_get", nil, "invalid request"},
		{"unknown property", "mail_get", map[string]any{"id": "m", "canary": 1}, "invalid request"},
		{"several wrong types", "mail_list", map[string]any{"folder": 3, "count": "x", "skip": "y"}, "invalid request"},
		{"not an object", "mail_folders", "canary", "invalid request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs, f := connect(t, mcptools.Options{Account: "work"}, answer(report), nil)
			res, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if e != nil {
				t.Fatal(e)
			}
			if !res.IsError || text(t, res) != tc.want || len(f.recorded()) != 0 {
				t.Errorf("error %t text %q server calls %d, want %q and none", res.IsError, text(t, res), len(f.recorded()), tc.want)
			}
		})
	}
}

func TestUnknownToolIsAProtocolErrorWithoutServerCall(t *testing.T) {
	cs, f := connect(t, mcptools.Options{}, answer(`{}`), nil)
	for _, name := range []string{"mail_archive", "stats", "cache_clear"} {
		if _, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: name}); e == nil {
			t.Errorf("%s: call succeeded", name)
		}
	}
	if len(f.recorded()) != 0 {
		t.Errorf("server calls %d, want none", len(f.recorded()))
	}
}

func TestCancelledToolCallCancelsTheServerCall(t *testing.T) {
	for _, version := range []string{"", "2025-11-25"} {
		t.Run("version "+version, func(t *testing.T) {
			started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			cs, _ := connect(t, mcptools.Options{}, func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-release:
				}
			}, &mcp.ClientSessionOptions{ProtocolVersion: version})
			t.Cleanup(func() { close(release) }) // runs before the fake server closes
			ctx, cancel := context.WithCancel(bounded(t))
			done := make(chan error, 1)
			go func() {
				_, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mail_get", Arguments: map[string]any{"id": "m"}})
				done <- e
			}()
			limit := time.After(5 * time.Second)
			select {
			case <-started:
			case <-limit:
				t.Fatal("server call did not start")
			}
			cancel()
			select {
			case e := <-done:
				if e == nil {
					t.Error("cancelled call succeeded")
				}
			case <-limit:
				t.Fatal("CallTool did not return after cancel")
			}
			select {
			case <-cancelled:
			case <-limit:
				t.Fatal("server call was not cancelled")
			}
		})
	}
}

func TestPanickingToolBecomesOperationFailed(t *testing.T) {
	cs := connectClient(t, nil, mcptools.Options{}, nil) // a nil client panics on its first call
	for range 2 {
		res, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: "mail_folders"})
		if e != nil {
			t.Fatal(e)
		}
		if !res.IsError || text(t, res) != "operation failed" {
			t.Fatalf("error %t text %q, want operation failed", res.IsError, text(t, res))
		}
	}
}
