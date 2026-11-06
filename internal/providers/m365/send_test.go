package m365_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestSendsPostTheGraphSendActionsAndAccept202(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusAccepted)
	})
	b := c.Mailbox()
	ctx := context.Background()
	if err := b.SendNew(ctx, model.NewDraft{To: []string{"a@example.test", "b@example.test"}, Subject: "Hi", Body: "text"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"POST /v1.0/me/sendMail"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("endpoints %v", paths)
	}
	to := func(addresses ...string) []any {
		out := []any{}
		for _, a := range addresses {
			out = append(out, map[string]any{"emailAddress": map[string]any{"address": a}})
		}
		return out
	}
	want := []map[string]any{
		{"message": map[string]any{"subject": "Hi", "body": map[string]any{"contentType": "text", "content": "text"}, "toRecipients": to("a@example.test", "b@example.test"), "ccRecipients": to(), "bccRecipients": to()}, "saveToSentItems": true},
	}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("send bodies %v", bodies)
	}
}

// A send that may have reached Graph is never sent again: a duplicate send is worse than a failure.
func TestFailedSendIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		requests := 0
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
		})
		b := c.Mailbox()
		ctx := context.Background()
		for _, send := range []func() error{
			func() error {
				return b.SendNew(ctx, model.NewDraft{To: []string{"a@example.test"}, Subject: "s", Body: "b"})
			},
			func() error { return b.SendReply(ctx, "m1", model.Reply{Body: "b"}) },
			func() error { return b.SendForward(ctx, "m1", model.Forward{To: []string{"a@example.test"}}) },
		} {
			before := requests
			if err := send(); err == nil {
				t.Fatalf("status %d: send succeeded", status)
			}
			if requests-before != 1 {
				t.Fatalf("status %d: %d requests for one send", status, requests-before)
			}
		}
	}
}

// A new draft and a new send carry To, Cc, and Bcc recipients and the HTML body type. The draft result is read
// from the message that Graph returns.
func TestNewMessagesCarryCcBccAndTheHTMLBodyType(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	recipient := func(name, address string) map[string]any {
		return map[string]any{"emailAddress": map[string]any{"name": name, "address": address}}
	}
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if r.URL.Path == "/v1.0/me/sendMail" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		jsonResponse(w, map[string]any{"id": "d1", "conversationId": "conv", "subject": "Hi", "isDraft": true,
			"toRecipients": []any{recipient("a@example.test", "a@example.test")}, "ccRecipients": []any{recipient("C", "c@example.test")}, "bccRecipients": []any{recipient("d@example.test", "d@example.test"), recipient("", "e@example.test")}})
	})
	b := c.Mailbox()
	d := model.NewDraft{To: []string{"a@example.test"}, CC: []string{"c@example.test"}, BCC: []string{"d@example.test", "e@example.test"}, Subject: "Hi", Body: "<p>Hi</p>", HTML: true}
	draft, err := b.DraftNew(t.Context(), d)
	if want := (model.DraftResult{ID: "d1", ThreadID: "conv", To: d.To, CC: d.CC, BCC: d.BCC, Subject: "Hi"}); err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	if err := b.SendNew(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if want := []string{"POST /v1.0/me/messages", "POST /v1.0/me/sendMail"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("endpoints %v", paths)
	}
	to := func(addresses ...string) []any {
		out := []any{}
		for _, a := range addresses {
			out = append(out, map[string]any{"emailAddress": map[string]any{"address": a}})
		}
		return out
	}
	message := map[string]any{"subject": "Hi", "body": map[string]any{"contentType": "html", "content": "<p>Hi</p>"}, "toRecipients": to("a@example.test"), "ccRecipients": to("c@example.test"), "bccRecipients": to("d@example.test", "e@example.test")}
	if want := []map[string]any{message, {"message": message, "saveToSentItems": true}}; !reflect.DeepEqual(bodies, want) {
		t.Fatalf("bodies %v", bodies)
	}
}

// A draft, or a message from the account's address, which is one that the account sent, carries its bccRecipients.
// A received message gives no BCC.
func TestGetReadsTheBccRecipientsOfDraftsAndSentMail(t *testing.T) {
	bcc := []model.Address{{Name: "Ed", Address: "ed@example.test"}}
	for _, tc := range []struct {
		draft bool
		from  string
		want  []model.Address
	}{{true, "ana@example.test", bcc}, {false, "WORK@example.test", bcc}, {false, "ana@example.test", []model.Address{}}} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Query().Get("$select"), ",bccRecipients,") {
				t.Errorf("select=%s", r.URL.Query().Get("$select"))
			}
			jsonResponse(w, map[string]any{"id": "m", "parentFolderId": "inbox-id", "isDraft": tc.draft, "from": graphRecipients(tc.from)[0], "bccRecipients": []any{map[string]any{"emailAddress": map[string]any{"name": "Ed", "address": "ed@example.test"}}}})
		})
		got, err := c.Mailbox().Get(t.Context(), []string{"m"}, false)
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].BCC, tc.want) {
			t.Fatalf("draft=%v from=%s: got=%+v %v", tc.draft, tc.from, got, err)
		}
	}
}
