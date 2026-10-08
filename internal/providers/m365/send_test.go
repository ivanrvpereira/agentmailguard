package m365_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
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
	if err := b.SendReply(ctx, "m/1", "thanks"); err != nil {
		t.Fatal(err)
	}
	if err := b.SendForward(ctx, "m/1", []string{"c@example.test"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"POST /v1.0/me/sendMail", "POST /v1.0/me/messages/m%2F1/reply", "POST /v1.0/me/messages/m%2F1/forward"}; !reflect.DeepEqual(paths, want) {
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
		{"message": map[string]any{"subject": "Hi", "body": map[string]any{"contentType": "text", "content": "text"}, "toRecipients": to("a@example.test", "b@example.test")}, "saveToSentItems": true},
		{"comment": "thanks"},
		{"comment": "", "toRecipients": to("c@example.test")},
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
			func() error { return b.SendReply(ctx, "m1", "b") },
			func() error { return b.SendForward(ctx, "m1", []string{"a@example.test"}) },
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
