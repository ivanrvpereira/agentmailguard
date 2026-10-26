package m365_test

import (
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const outlookForward = `<html><head></head><body><hr><div id="divRplyFwdMsg"><b>From:</b> Ana</div><div>One</div></body></html>`

// A forward draft is Graph's createForward with no comment, and with the agent's To when it gives one, then one
// PATCH: the note goes above the quoted original as in a reply, and each recipient list that differs from the
// agent's is set to it. Without a note and with nothing to change, only createForward is sent. The result is Graph's
// answer to the last write.
func TestForwardDraftPatchesTheNoteAboveTheQuoteAndSetsTheRecipients(t *testing.T) {
	for _, tc := range []struct {
		name    string
		forward model.Forward
		fields  map[string]any
		create  map[string]any
		patch   map[string]any
	}{
		{"note and every list", model.Forward{Body: "FYI <3\nAna", To: []string{"a@example.test"}, CC: []string{"c@example.test"}, BCC: []string{"d@example.test"}},
			map[string]any{"toRecipients": graphRecipients("a@example.test"), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()},
			map[string]any{"message": map[string]any{"toRecipients": graphRecipients("a@example.test")}},
			map[string]any{"body": map[string]any{"contentType": "html", "content": `<html><head></head><body>FYI &lt;3<br>Ana<hr><div id="divRplyFwdMsg"><b>From:</b> Ana</div><div>One</div></body></html>`},
				"ccRecipients": graphRecipients("c@example.test"), "bccRecipients": graphRecipients("d@example.test")}},
		{"html note and a To that Graph did not keep", model.Forward{Body: "<p>FYI</p>", HTML: true, To: []string{"A@example.test"}},
			map[string]any{"toRecipients": graphRecipients(), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()},
			map[string]any{"message": map[string]any{"toRecipients": graphRecipients("A@example.test")}},
			map[string]any{"body": map[string]any{"contentType": "html", "content": `<html><head></head><body><p>FYI</p><hr><div id="divRplyFwdMsg"><b>From:</b> Ana</div><div>One</div></body></html>`},
				"toRecipients": graphRecipients("A@example.test")}},
		{"recipients only, To ignoring case", model.Forward{To: []string{"A@example.test"}, BCC: []string{"d@example.test"}},
			map[string]any{"toRecipients": graphRecipients("a@example.test"), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()},
			map[string]any{"message": map[string]any{"toRecipients": graphRecipients("A@example.test")}},
			map[string]any{"bccRecipients": graphRecipients("d@example.test")}},
		{"nothing to change", model.Forward{},
			map[string]any{"toRecipients": graphRecipients(), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()},
			map[string]any{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &replyGraph{fields: tc.fields}
			b := graphClient(t, g.handler(t, "html", outlookForward, "")).Mailbox()
			draft, err := b.DraftForward(t.Context(), "m/1", tc.forward)
			if err != nil || draft.ID != "d1" || draft.ThreadID != "conv" {
				t.Fatalf("draft=%+v %v", draft, err)
			}
			paths, bodies := []string{"POST /v1.0/me/messages/m%2F1/createForward"}, []map[string]any{tc.create}
			if tc.patch != nil {
				paths, bodies = append(paths, "PATCH /v1.0/me/messages/d1"), append(bodies, tc.patch)
			}
			if !reflect.DeepEqual(g.paths, paths) || !reflect.DeepEqual(g.bodies, bodies) {
				t.Fatalf("requests %v bodies %v", g.paths, g.bodies)
			}
		})
	}
}

// The draft result of a forward is Graph's answer to the PATCH: the recipients that the draft now has.
func TestForwardDraftResultIsGraphsAnswer(t *testing.T) {
	g := &replyGraph{fields: map[string]any{"subject": "FW: Plan", "toRecipients": graphRecipients(), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()}}
	b := graphClient(t, g.handler(t, "text", "> One", "")).Mailbox()
	draft, err := b.DraftForward(t.Context(), "m1", model.Forward{Body: "FYI", CC: []string{"c@example.test"}})
	want := model.DraftResult{ID: "d1", ThreadID: "conv", To: []string{}, CC: []string{"c@example.test"}, BCC: []string{}, Subject: "FW: Plan"}
	if err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	if patch := g.bodies[1]["body"]; !reflect.DeepEqual(patch, map[string]any{"contentType": "text", "content": "FYI\n\n> One"}) {
		t.Fatalf("patch body %v", patch)
	}
}

// mail forward makes the forward draft, patches it, and only then sends it with /send. A failed step stops the
// forward: nothing is retried, and nothing is sent after a failed create or patch, or after an answer for another
// draft.
func TestSendForwardSendsThePatchedDraftOnlyAfterEachStepSucceeds(t *testing.T) {
	steps := []string{"POST /v1.0/me/messages/m1/createForward", "PATCH /v1.0/me/messages/d1", "POST /v1.0/me/messages/d1/send"}
	for _, tc := range []struct {
		name, fail string
		ids        map[string]any
		want       []string
	}{
		{"sent", "", nil, steps},
		{"create fails", "/v1.0/me/messages/m1/createForward", nil, steps[:1]},
		{"patch fails", "/v1.0/me/messages/d1", nil, steps[:2]},
		{"send fails", "/v1.0/me/messages/d1/send", nil, steps},
		{"create without id", "", map[string]any{"create": nil}, steps[:1]},
		{"patch of another draft", "", map[string]any{"patch": "d2"}, steps[:2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &replyGraph{ids: tc.ids, fields: map[string]any{"toRecipients": graphRecipients("a@example.test"), "ccRecipients": graphRecipients(), "bccRecipients": graphRecipients()}}
			b := graphClient(t, g.handler(t, "html", outlookForward, tc.fail)).Mailbox()
			err := b.SendForward(t.Context(), "m1", model.Forward{Body: "FYI", To: []string{"a@example.test"}, CC: []string{"c@example.test"}})
			if (err == nil) != (tc.name == "sent") || !reflect.DeepEqual(g.paths, tc.want) {
				t.Fatalf("err=%v requests=%v", err, g.paths)
			}
		})
	}
}
