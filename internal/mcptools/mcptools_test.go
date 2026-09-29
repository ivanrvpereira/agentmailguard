package mcptools_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
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

var writeTools = []string{"draft_forward", "draft_new", "draft_reply", "draft_update", "mail_archive", "mail_categorize", "mail_flag", "mail_move", "mail_read", "mail_trash", "mail_unread"}

func TestWritesModeListsTheReadAndWriteTools(t *testing.T) {
	cs, _ := connect(t, mcptools.Options{AllowWrites: true}, answer(`{}`), nil)
	res, e := cs.ListTools(bounded(t), nil)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Sorted(slices.Values(append(slices.Clone(readTools), writeTools...)))
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
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

func TestEveryToolDescribesItselfAndItsHints(t *testing.T) {
	// Rows of the approved annotation table: readOnly, destructive, idempotent, openWorld.
	provider := [4]bool{true, false, true, true}
	local := [4]bool{true, false, true, false}
	change := [4]bool{false, false, true, false}
	draft := [4]bool{false, false, false, false}
	readOnly := map[string][4]bool{
		"mail_list": provider, "mail_search": provider, "mail_get": provider, "mail_link": provider, "calendar_list": provider, "calendar_get": provider,
		"mail_folders": local, "mail_categories": local, "rules_list": local, "auth_status": local,
		"mail_organize": provider,
	}
	// Writes mode serves the read tools unchanged except mail_organize, plus the write tools.
	writes := maps.Clone(readOnly)
	maps.Copy(writes, map[string][4]bool{
		"mail_organize": {false, true, true, true},
		"mail_read":     change, "mail_unread": change, "mail_flag": change, "mail_categorize": change, "mail_archive": change, "mail_move": change,
		"mail_trash": {false, true, true, false},
		"draft_new":  draft, "draft_reply": draft, "draft_forward": draft,
		"draft_update": {false, true, true, false},
	})
	for _, mode := range []struct {
		name     string
		writes   bool
		want     map[string][4]bool
		confirms []string // tools that ask the client for user confirmation
	}{
		{"read-only", false, readOnly, nil},
		{"writes", true, writes, []string{"mail_organize", "mail_trash"}},
	} {
		t.Run(mode.name, func(t *testing.T) { describesItself(t, mode.writes, mode.want, mode.confirms) })
	}
}

func describesItself(t *testing.T, writes bool, want map[string][4]bool, confirms []string) {
	untrusted := map[string]bool{"mail_list": true, "mail_search": true, "mail_get": true, "mail_link": true, "calendar_list": true, "calendar_get": true, "mail_organize": true}
	mutation := map[string]bool{"mail_read": true, "mail_unread": true, "mail_flag": true, "mail_categorize": true, "mail_archive": true, "mail_move": true, "mail_trash": true}
	cs, _ := connect(t, mcptools.Options{AllowWrites: writes}, answer(`{}`), nil)
	res, e := cs.ListTools(bounded(t), nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(res.Tools) != len(want) {
		t.Errorf("tools %d, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		wire, _ := json.Marshal(tool)
		if slices.Contains(confirms, tool.Name) {
			if !strings.Contains(string(wire), `"_meta":{"anthropic/requiresUserInteraction":true}`) {
				t.Errorf("%s: no user interaction meta: %s", tool.Name, wire)
			}
		} else if tool.Meta != nil || strings.Contains(string(wire), "_meta") {
			t.Errorf("%s: unexpected meta: %s", tool.Name, wire)
		}
		if mutation[tool.Name] && !(strings.Contains(tool.Description, "per ID") && strings.Contains(tool.Description, "`ok`") && strings.Contains(tool.Description, "`error`") && strings.Contains(tool.Description, "never deleted permanently")) {
			t.Errorf("%s: description does not explain the results: %q", tool.Name, tool.Description)
		}
		if strings.HasPrefix(tool.Name, "draft_") && !strings.Contains(tool.Description, "never sent") {
			t.Errorf("%s: description does not say drafts are never sent: %q", tool.Name, tool.Description)
		}
		if tool.Name == "mail_organize" && writes != (strings.Contains(tool.Description, "apply: true performs the planned moves") && strings.Contains(tool.Description, "trash") && strings.Contains(tool.Description, "apply: false only plans")) {
			t.Errorf("%s: description does not match the mode: %q", tool.Name, tool.Description)
		}
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
		if tool.Name == "mail_flag" && !strings.Contains(string(in.Properties["state"]), "flagged, complete, or none") {
			t.Errorf("%s: state does not list its values: %s", tool.Name, in.Properties["state"])
		}
		if tool.Name == "mail_move" && !(strings.Contains(string(in.Properties["folder"]), "folder ID from mail_folders") && strings.Contains(tool.Description, "folder ID from mail_folders")) {
			t.Errorf("%s: folder does not require a folder ID: %s %q", tool.Name, in.Properties["folder"], tool.Description)
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

// Mutation results carry null for a success and a safe code for a failure.
const (
	results = `[{"id":"a","ok":true,"error":null},{"id":"b","ok":false,"error":"Error 404"}]`
	draftID = `{"id":"d1"}`
	updated = `{"id":"d1","ok":true}`
)

// writeCases run with AllowWrites.
var writeCases = []toolCase{
	{"mail_archive", "mail_archive", map[string]any{"ids": []string{"a", "b"}}, "mail.archive", `{"ids":["a","b"]}`, results, "results"},
	{"mail_trash", "mail_trash", map[string]any{"ids": []string{"a", "b"}}, "mail.trash", `{"ids":["a","b"]}`, results, "results"},
	{"mail_read", "mail_read", map[string]any{"ids": []string{"a"}}, "mail.read", `{"ids":["a"]}`, results, "results"},
	{"mail_unread", "mail_unread", map[string]any{"ids": []string{"a"}}, "mail.unread", `{"ids":["a"]}`, results, "results"},
	{"mail_move", "mail_move", map[string]any{"ids": []string{"a", "b"}, "folder": "f2"}, "mail.move", `{"ids":["a","b"],"folder":"f2"}`, results, "results"},
	{"mail_flag", "mail_flag", map[string]any{"ids": []string{"a", "b"}, "state": "complete"}, "mail.flag", `{"ids":["a","b"],"state":"complete"}`, results, "results"},
	{"mail_categorize both", "mail_categorize", map[string]any{"ids": []string{"a"}, "add": []string{"One", "Two"}, "remove": []string{"Old"}}, "mail.categorize", `{"ids":["a"],"add":["One","Two"],"remove":["Old"]}`, results, "results"},
	{"mail_categorize add", "mail_categorize", map[string]any{"ids": []string{"a"}, "add": []string{"One"}}, "mail.categorize", `{"ids":["a"],"add":["One"]}`, results, "results"},
	{"mail_categorize remove", "mail_categorize", map[string]any{"ids": []string{"a"}, "add": []string{}, "remove": []string{"Old"}}, "mail.categorize", `{"ids":["a"],"remove":["Old"]}`, results, "results"},
	{"draft_new", "draft_new", map[string]any{"to": []string{"a@test", "b@test"}, "subject": "s", "body": "b"}, "draft.new", `{"to":["a@test","b@test"],"subject":"s","body":"b"}`, draftID, ""},
	{"draft_new empty", "draft_new", map[string]any{"to": []string{"a@test"}, "subject": "", "body": ""}, "draft.new", `{"to":["a@test"],"subject":"","body":""}`, draftID, ""},
	{"draft_reply", "draft_reply", map[string]any{"id": "m", "body": "reply"}, "draft.reply", `{"id":"m","body":"reply"}`, draftID, ""},
	{"draft_reply empty", "draft_reply", map[string]any{"id": "m", "body": ""}, "draft.reply", `{"id":"m","body":""}`, draftID, ""},
	{"draft_forward", "draft_forward", map[string]any{"id": "m", "to": []string{"a@test", "b@test"}}, "draft.forward", `{"id":"m","to":["a@test","b@test"]}`, draftID, ""},
	{"draft_update", "draft_update", map[string]any{"id": "d", "body": ""}, "draft.update", `{"id":"d","body":""}`, updated, ""},
	{"mail_organize apply", "mail_organize", map[string]any{"since": "1m", "apply": true}, "mail.organize", `{"since":"1m","apply":true}`, report, ""},
	{"mail_organize plan", "mail_organize", map[string]any{}, "mail.organize", `{"since":"7d","apply":false}`, report, ""},
}

// modes pairs each case table with the AllowWrites option it runs under.
var modes = []struct {
	writes bool
	cases  []toolCase
}{{false, toolCases}, {true, writeCases}}

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
	for _, mode := range modes {
		for _, tc := range mode.cases {
			forwards(t, mode.writes, tc)
		}
	}
}

func forwards(t *testing.T, writes bool, tc toolCase) {
	t.Helper()
	for _, acc := range []struct{ name, option, argument, want string }{
		{"option", "work", "", "work"},
		{"argument", "work", "remote", "remote"},
		{"none", "", "", ""},
	} {
		t.Run(tc.name+" "+acc.name, func(t *testing.T) {
			cs, f := connect(t, mcptools.Options{Account: acc.option, AllowWrites: writes}, answer(tc.body), nil)
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
	for _, mode := range modes {
		for _, tc := range mode.cases {
			t.Run(tc.name, func(t *testing.T) { carriesResult(t, mode.writes, tc) })
		}
	}
}

func carriesResult(t *testing.T, writes bool, tc toolCase) {
	cs, _ := connect(t, mcptools.Options{AllowWrites: writes}, answer(tc.body), nil)
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
}

func TestFailedCallsBecomeToolErrorsWithFixedText(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		status     int
		body, want string
	}{
		{"flag refusal", "mail_flag", map[string]any{"ids": []string{"a"}, "state": "canary"}, 400, `{"error":"flag state must be one of flagged, complete, none"}`, "flag state must be one of flagged, complete, none"},
		{"move refusal", "mail_move", map[string]any{"ids": []string{"a"}, "folder": "Archive"}, 400, "{\"error\":\"move refused: the destination must be a folder ID from `folders`\"}", "move refused: the destination must be a folder ID from `folders`"},
		{"categorize refusal", "mail_categorize", map[string]any{"ids": []string{"a"}}, 400, `{"error":"categorize needs at least one category to add or remove"}`, "categorize needs at least one category to add or remove"},
		{"trash unknown refusal", "mail_trash", map[string]any{"ids": []string{"a"}}, 500, `{"error":"canary-private-error"}`, "server request failed"},
		{"trash wrong shape", "mail_trash", map[string]any{"ids": []string{"a"}}, 200, `{"canary-private-key":"x"}`, "invalid server response"},
		{"mutation wrong field type", "mail_archive", map[string]any{"ids": []string{"a"}}, 200, `[{"id":"canary-private-id","ok":"yes","error":null}]`, "invalid server response"},
		{"draft wrong shape", "draft_new", map[string]any{"to": []string{"a@test"}, "subject": "", "body": ""}, 200, `["canary-private-id"]`, "invalid server response"},
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
			cs, _ := connect(t, mcptools.Options{AllowWrites: true}, func(w http.ResponseWriter, _ *http.Request) {
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
			refused(t, false, tc.tool, tc.args, tc.want)
		})
	}
	for _, tc := range []struct {
		name, tool string
		args       any
	}{
		{"ids as a string", "mail_trash", map[string]any{"ids": "a"}},
		{"missing ids", "mail_archive", map[string]any{}},
		{"missing folder", "mail_move", map[string]any{"ids": []string{"a"}}},
		{"missing state", "mail_flag", map[string]any{"ids": []string{"a"}}},
		{"add as a string", "mail_categorize", map[string]any{"ids": []string{"a"}, "add": "One"}},
		{"missing subject", "draft_new", map[string]any{"to": []string{"a@test"}, "body": ""}},
		{"missing body", "draft_new", map[string]any{"to": []string{"a@test"}, "subject": ""}},
		{"reply without body", "draft_reply", map[string]any{"id": "m"}},
		{"update without body", "draft_update", map[string]any{"id": "d"}},
		{"forward without to", "draft_forward", map[string]any{"id": "m"}},
		{"unknown property", "mail_read", map[string]any{"ids": []string{"a"}, "canary": 1}},
	} {
		t.Run("writes "+tc.name, func(t *testing.T) {
			refused(t, true, tc.tool, tc.args, "invalid request")
		})
	}
}

// refused calls a tool with arguments that must fail with want before any server call.
func refused(t *testing.T, writes bool, tool string, args any, want string) {
	t.Helper()
	cs, f := connect(t, mcptools.Options{Account: "work", AllowWrites: writes}, answer(report), nil)
	res, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: tool, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	if !res.IsError || text(t, res) != want || len(f.recorded()) != 0 {
		t.Errorf("error %t text %q server calls %d, want %q and none", res.IsError, text(t, res), len(f.recorded()), want)
	}
}

func TestUnknownToolIsAProtocolErrorWithoutServerCall(t *testing.T) {
	cs, f := connect(t, mcptools.Options{}, answer(`{}`), nil)
	// Without AllowWrites the write tools are unknown, like the tools that are never exposed.
	for _, name := range append([]string{"stats", "cache_clear"}, writeTools...) {
		if _, e := cs.CallTool(bounded(t), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"ids": []string{"a"}}}); e == nil || !strings.Contains(e.Error(), "unknown tool") {
			t.Errorf("%s: error %v, want unknown tool", name, e)
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
