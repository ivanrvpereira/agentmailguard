package m365_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// graphRecipients is a Graph recipient list of bare addresses.
func graphRecipients(addresses ...string) []any {
	out := []any{}
	for _, a := range addresses {
		out = append(out, map[string]any{"emailAddress": map[string]any{"address": a}})
	}
	return out
}

// replyGraph is a fake Graph API for replies. The created reply draft d1 has the given body and Microsoft's
// recipients; a PATCH answers with the draft as patched, and send answers 202. fail names a request path that
// answers 503. ids, when set, gives the draft ID of the create answer (key "create") or the PATCH answer (key "patch").
// It records each request and its JSON body.
type replyGraph struct {
	paths  []string
	bodies []map[string]any
	ids    map[string]any
}

func (g *replyGraph) handler(t *testing.T, kind, content, fail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.paths = append(g.paths, r.Method+" "+r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.bodies = append(g.bodies, body)
		if r.URL.EscapedPath() == fail {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		draft := map[string]any{"id": "d1", "conversationId": "conv", "subject": "RE: Plan", "isDraft": true, "body": map[string]any{"contentType": kind, "content": content},
			"toRecipients": graphRecipients("ana@example.test"), "ccRecipients": graphRecipients("bo@example.test"), "bccRecipients": graphRecipients()}
		switch r.Method + " " + r.URL.EscapedPath() {
		case "POST /v1.0/me/messages/d1/send":
			w.WriteHeader(http.StatusAccepted)
		case "PATCH /v1.0/me/messages/d1":
			for k, v := range body {
				draft[k] = v
			}
			if id, ok := g.ids["patch"]; ok {
				draft["id"] = id
			}
			jsonResponse(w, draft)
		default:
			if id, ok := g.ids["create"]; ok {
				draft["id"] = id
			}
			jsonResponse(w, draft)
		}
	}
}

const outlookReply = `<html><head><meta charset="utf-8"></head><BODY dir="ltr"><hr><div id="divRplyFwdMsg"><b>From:</b> Ana</div><div>One</div></BODY></html>`

// A reply all draft is Graph's createReplyAll with no comment and no body, then one PATCH: the agent's text, escaped
// with its line breaks, goes right after the opening body tag of the quoted reply that Graph made, and the agent's CC
// and BCC are added to Microsoft's recipients without duplicates ignoring case. The result is Graph's answer.
func TestReplyAllDraftPatchesTheAgentTextAboveTheQuoteAndAddsRecipients(t *testing.T) {
	g := &replyGraph{}
	b := graphClient(t, g.handler(t, "html", outlookReply, "")).Mailbox()
	draft, err := b.DraftReply(t.Context(), "m/1", model.Reply{Body: "Thanks <3\nAna", All: true, CC: []string{"BO@example.test", "cy@example.test"}, BCC: []string{"eve@example.test", "Ana@example.test"}})
	if want := (model.DraftResult{ID: "d1", ThreadID: "conv", To: []string{"ana@example.test"}, CC: []string{"bo@example.test", "cy@example.test"}, BCC: []string{"eve@example.test"}, Subject: "RE: Plan"}); err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	if want := []string{"POST /v1.0/me/messages/m%2F1/createReplyAll", "PATCH /v1.0/me/messages/d1"}; !reflect.DeepEqual(g.paths, want) {
		t.Fatalf("requests %v", g.paths)
	}
	patch := map[string]any{"body": map[string]any{"contentType": "html", "content": `<html><head><meta charset="utf-8"></head><BODY dir="ltr">Thanks &lt;3<br>Ana<hr><div id="divRplyFwdMsg"><b>From:</b> Ana</div><div>One</div></BODY></html>`},
		"ccRecipients": graphRecipients("bo@example.test", "cy@example.test"), "bccRecipients": graphRecipients("eve@example.test")}
	if want := []map[string]any{{}, patch}; !reflect.DeepEqual(g.bodies, want) {
		t.Fatalf("bodies %v", g.bodies)
	}
}

// The agent's HTML goes in unchanged. When Graph made a plain-text reply, plain text goes above it after a blank
// line, and HTML makes the body HTML with the escaped original text below. A reply without text or recipients to
// add is only createReply.
func TestReplyDraftBodyFollowsTheTypeOfGraphsQuote(t *testing.T) {
	for _, tc := range []struct {
		name, kind, quote string
		reply             model.Reply
		patch             map[string]any
	}{
		{"html into html", "html", "<html><body><div>One</div></body></html>", model.Reply{Body: "<p>Hi</p>", HTML: true},
			map[string]any{"contentType": "html", "content": "<html><body><p>Hi</p><div>One</div></body></html>"}},
		{"text into text", "text", "> One", model.Reply{Body: "Hi\nthere"},
			map[string]any{"contentType": "text", "content": "Hi\nthere\n\n> One"}},
		{"html into text", "text", "> One & <two>", model.Reply{Body: "<p>Hi</p>", HTML: true},
			map[string]any{"contentType": "html", "content": "<p>Hi</p><br><br>&gt; One &amp; &lt;two&gt;"}},
		{"nothing to change", "html", "<html><body>One</body></html>", model.Reply{CC: []string{"BO@example.test"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &replyGraph{}
			b := graphClient(t, g.handler(t, tc.kind, tc.quote, "")).Mailbox()
			if _, err := b.DraftReply(t.Context(), "m1", tc.reply); err != nil {
				t.Fatal(err)
			}
			if tc.patch == nil {
				if want := []string{"POST /v1.0/me/messages/m1/createReply"}; !reflect.DeepEqual(g.paths, want) {
					t.Fatalf("requests %v", g.paths)
				}
				return
			}
			if len(g.bodies) != 2 || !reflect.DeepEqual(g.bodies[1]["body"], tc.patch) {
				t.Fatalf("requests %v bodies %v", g.paths, g.bodies)
			}
		})
	}
}

// mail reply makes the reply draft, patches it, and only then sends it with /send. A failed step stops the reply:
// nothing is retried, and nothing is sent after a failed create or patch.
func TestSendReplySendsThePatchedDraftOnlyAfterEachStepSucceeds(t *testing.T) {
	steps := []string{"POST /v1.0/me/messages/m1/createReply", "PATCH /v1.0/me/messages/d1", "POST /v1.0/me/messages/d1/send"}
	for _, tc := range []struct {
		fail string
		want []string
	}{
		{"", steps},
		{"/v1.0/me/messages/m1/createReply", steps[:1]},
		{"/v1.0/me/messages/d1", steps[:2]},
		{"/v1.0/me/messages/d1/send", steps},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			g := &replyGraph{}
			b := graphClient(t, g.handler(t, "html", "<html><body>One</body></html>", tc.fail)).Mailbox()
			err := b.SendReply(t.Context(), "m1", model.Reply{Body: "Thanks"})
			if (err == nil) != (tc.fail == "") || !reflect.DeepEqual(g.paths, tc.want) {
				t.Fatalf("err=%v requests=%v", err, g.paths)
			}
		})
	}
}

// ReplyRecipients reads the original's sender, Reply-To, To, and CC and applies the reply rule with the account's
// address as its own; a message from the account's address is one that it sent.
func TestReplyRecipientsReadTheOriginalWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		from string
		want model.Recipients
	}{
		{"received", "ana@example.test", model.Recipients{To: []string{"list@example.test"}, CC: []string{"bo@example.test", "cy@example.test"}, BCC: []string{}}},
		{"sent", "WORK@example.test", model.Recipients{To: []string{"bo@example.test"}, CC: []string{"cy@example.test"}, BCC: []string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			b := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.Query().Get("$select"))
				jsonResponse(w, map[string]any{"id": "m1", "from": graphRecipients(tc.from)[0], "replyTo": graphRecipients("list@example.test"),
					"toRecipients": graphRecipients("work@example.test", "bo@example.test"), "ccRecipients": graphRecipients("cy@example.test", "Work@Example.test")})
			}).Mailbox()
			got, err := b.ReplyRecipients(t.Context(), "m1", true)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("recipients=%+v %v", got, err)
			}
			if want := []string{"GET /v1.0/me/messages/m1?from,replyTo,toRecipients,ccRecipients"}; !reflect.DeepEqual(paths, want) {
				t.Fatalf("requests %v", paths)
			}
		})
	}
}

// A create answer without a draft ID, or a PATCH answer for another draft, fails the reply: nothing is patched after
// the bad create, and nothing is sent.
func TestSendReplyStopsOnAnAnswerForTheWrongDraft(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  map[string]any
		want []string
	}{
		{"create without id", map[string]any{"create": nil}, []string{"POST /v1.0/me/messages/m1/createReply"}},
		{"patch of another draft", map[string]any{"patch": "d2"}, []string{"POST /v1.0/me/messages/m1/createReply", "PATCH /v1.0/me/messages/d1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &replyGraph{ids: tc.ids}
			b := graphClient(t, g.handler(t, "html", "<html><body>One</body></html>", "")).Mailbox()
			if err := b.SendReply(t.Context(), "m1", model.Reply{Body: "Thanks"}); err == nil || !reflect.DeepEqual(g.paths, tc.want) {
				t.Fatalf("err=%v requests=%v", err, g.paths)
			}
		})
	}
}
