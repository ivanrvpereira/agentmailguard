package gmail_test

import (
	"context"
	"net/http"
	"net/mail"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// replyOriginal serves message "original" with the given headers, labels, and body parts, and the account's
// send-as list. writes gets each draft or send request; other requests fail the test.
func replyOriginal(t *testing.T, labels []string, headers []map[string]string, body map[string]any, writes func(w http.ResponseWriter, r *http.Request)) (*[]string, http.HandlerFunc) {
	requests := []string{}
	return &requests, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Path == "/messages/original":
			body["headers"] = headers
			m := message("original", body)
			m["labelIds"] = labels
			respond(w, m)
		case r.Method == "GET" && r.URL.Path == "/settings/sendAs":
			respond(w, map[string]any{"sendAs": []map[string]any{{"sendAsEmail": "synthetic@example.test", "isPrimary": true}, {"sendAsEmail": "Alias@Example.test"}}})
		case r.Method == "POST":
			writes(w, r)
		default:
			t.Errorf("request %s %s", r.Method, r.URL)
		}
	}
}

// header gives the bare addresses of an address-list header.
func headerAddresses(t *testing.T, m *mail.Message, name string) []string {
	t.Helper()
	out := []string{}
	if m.Header.Get(name) == "" {
		return out
	}
	list, err := m.Header.AddressList(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

// Reply all goes to Reply-To and copies the original To and CC as Gmail on the web does: the account's address and
// its send-as addresses (users.settings.sendAs.list) are left out, each address appears once ignoring case, and the
// agent's CC and BCC are added. The draft stays in the thread and its result gives what was built.
func TestReplyAllDraftPicksGmailWebRecipientsAndAddsTheAgentCCAndBCC(t *testing.T) {
	headers := []map[string]string{{"name": "From", "value": "Ana <ana@example.test>"}, {"name": "Reply-To", "value": "List <list@example.test>"},
		{"name": "To", "value": "Synthetic <SYNTHETIC@example.test>, bo@example.test, alias@example.test"}, {"name": "Cc", "value": "Bo@Example.test, cy@example.test, list@example.test"},
		{"name": "Subject", "value": "Plan"}, {"name": "Message-ID", "value": "<m@example.test>"}}
	requests, h := replyOriginal(t, []string{"INBOX"}, headers, part("text/plain", "One"), func(w http.ResponseWriter, r *http.Request) {
		m, payload := draftMIME(t, r)
		if payload["threadId"] != "thread" || m.Header.Get("In-Reply-To") != "<m@example.test>" || m.Header.Get("Subject") != "Re: Plan" {
			t.Errorf("thread: %v %+v", payload, m.Header)
		}
		got := [][]string{headerAddresses(t, m, "To"), headerAddresses(t, m, "Cc"), headerAddresses(t, m, "Bcc")}
		if want := [][]string{{"list@example.test"}, {"bo@example.test", "cy@example.test", "dan@example.test"}, {"eve@example.test"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("recipients %v", got)
		}
		respond(w, map[string]any{"id": "r1", "message": map[string]any{"id": "created", "threadId": "thread"}})
	})
	c := sendClient(t, h)
	draft, err := c.Mailbox().DraftReply(context.Background(), "original", model.Reply{Body: "Thanks", All: true, CC: []string{"CY@example.test", "dan@example.test"}, BCC: []string{"eve@example.test", "list@example.test"}})
	want := model.DraftResult{ID: "created", ThreadID: "thread", To: []string{"list@example.test"}, CC: []string{"bo@example.test", "cy@example.test", "dan@example.test"}, BCC: []string{"eve@example.test"}, Subject: "Re: Plan"}
	if err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	if want := []string{"GET /messages/original", "GET /settings/sendAs", "POST /drafts"}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%v", *requests)
	}
}

// Reply all to a message that the account sent (label SENT) goes to the original To and copies the original CC,
// without the own addresses. A reply without all goes to the original To and reads no send-as list.
func TestReplyToASentMessageGoesToTheOriginalRecipients(t *testing.T) {
	headers := []map[string]string{{"name": "From", "value": "alias@example.test"}, {"name": "Reply-To", "value": "ignored@example.test"},
		{"name": "To", "value": "bo@example.test, synthetic@example.test"}, {"name": "Cc", "value": "cy@example.test, BO@example.test"}, {"name": "Subject", "value": "Re: Plan"}}
	var got [][]string
	requests, h := replyOriginal(t, []string{"SENT"}, headers, part("text/plain", "One"), func(w http.ResponseWriter, r *http.Request) {
		m, _ := sentMIME(t, r)
		got = append(got, headerAddresses(t, m, "To"), headerAddresses(t, m, "Cc"))
		respond(w, map[string]any{"id": "sent"})
	})
	c := sendClient(t, h)
	if err := c.Mailbox().SendReply(context.Background(), "original", model.Reply{Body: "Thanks", All: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Mailbox().SendReply(context.Background(), "original", model.Reply{Body: "Thanks"}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"bo@example.test"}, {"cy@example.test"}, {"bo@example.test", "synthetic@example.test"}, {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recipients %v", got)
	}
	if want := []string{"GET /messages/original", "GET /settings/sendAs", "POST /messages/send", "GET /messages/original", "POST /messages/send"}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%v", *requests)
	}
}

// ReplyRecipients reads the original and the send-as list and gives the recipients that a reply all picks, with
// no write.
func TestReplyRecipientsReadsWithoutWriting(t *testing.T) {
	headers := []map[string]string{{"name": "From", "value": "Ana <ana@example.test>"}, {"name": "To", "value": "synthetic@example.test, bo@example.test"}, {"name": "Cc", "value": "Alias@example.test"}}
	requests, h := replyOriginal(t, []string{"INBOX"}, headers, part("text/plain", "One"), func(http.ResponseWriter, *http.Request) { t.Error("write") })
	c := sendClient(t, h)
	got, err := c.Mailbox().ReplyRecipients(context.Background(), "original", true)
	if want := (model.Recipients{To: []string{"ana@example.test"}, CC: []string{"bo@example.test"}, BCC: []string{}}); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("recipients=%+v %v", got, err)
	}
	if want := []string{"GET /messages/original", "GET /settings/sendAs"}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%v", *requests)
	}
}

// An HTML reply is a text/html part: the agent's HTML above the quoted original, which is the original HTML part
// when there is one, otherwise the escaped original text.
func TestHTMLReplyQuotesTheOriginalBelowTheAgentHTML(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  map[string]any
		quote string
	}{
		{"html original", map[string]any{"mimeType": "multipart/alternative", "parts": []any{part("text/plain", "plain <one>"), part("text/html", "<p>One <b>bold</b></p>")}}, "<p>One <b>bold</b></p>"},
		{"text original", part("text/plain", "a <b> & c\nnext"), "a &lt;b&gt; &amp; c<br>next"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := []map[string]string{{"name": "From", "value": "Ana <ana@example.test>"}, {"name": "Subject", "value": "Plan"}}
			_, h := replyOriginal(t, []string{"INBOX"}, headers, tc.body, func(w http.ResponseWriter, r *http.Request) {
				m, _ := draftMIME(t, r)
				if m.Header.Get("Content-Type") != "text/html; charset=utf-8" {
					t.Errorf("type %q", m.Header.Get("Content-Type"))
				}
				body := draftText(t, m)
				if !strings.HasPrefix(body, "<p>Thanks <i>Ana</i></p>") || !strings.Contains(body, "On 2026-09-01 00:00 UTC, ana@example.test wrote:") || !strings.Contains(body, "<blockquote") || !strings.Contains(body, tc.quote+"</blockquote>") {
					t.Errorf("body %q", body)
				}
				respond(w, map[string]any{"id": "r1", "message": map[string]any{"id": "created", "threadId": "thread"}})
			})
			c := sendClient(t, h)
			if _, err := c.Mailbox().DraftReply(context.Background(), "original", model.Reply{Body: "<p>Thanks <i>Ana</i></p>", HTML: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// An address with a quoted local part that holds a comma stays one recipient in the reply headers.
func TestReplyKeepsAQuotedLocalPartAsOneAddress(t *testing.T) {
	headers := []map[string]string{{"name": "From", "value": "ana@example.test"}, {"name": "To", "value": "synthetic@example.test"}, {"name": "Cc", "value": `"bo, cy"@example.test`}}
	var got []string
	_, h := replyOriginal(t, []string{"INBOX"}, headers, part("text/plain", "One"), func(w http.ResponseWriter, r *http.Request) {
		m, _ := draftMIME(t, r)
		got = headerAddresses(t, m, "Cc")
		respond(w, map[string]any{"id": "r1", "message": map[string]any{"id": "created", "threadId": "thread"}})
	})
	c := sendClient(t, h)
	if _, err := c.Mailbox().DraftReply(context.Background(), "original", model.Reply{All: true}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"bo, cy@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cc=%q", got)
	}
}

// A To or Cc header that does not parse fails a reply that uses it, with no write; a plain reply to a received
// message uses only Reply-To and From, so it does not read them.
func TestReplyFailsOnAMalformedHeaderOnlyWhenItIsUsed(t *testing.T) {
	headers := []map[string]string{{"name": "From", "value": "ana@example.test"}, {"name": "To", "value": "synthetic@example.test"}, {"name": "Cc", "value": "bo@example.test, <broken"}}
	writes := 0
	_, h := replyOriginal(t, []string{"INBOX"}, headers, part("text/plain", "One"), func(w http.ResponseWriter, r *http.Request) {
		writes++
		respond(w, map[string]any{"id": "r1", "message": map[string]any{"id": "created", "threadId": "thread"}})
	})
	c := sendClient(t, h)
	if _, err := c.Mailbox().DraftReply(context.Background(), "original", model.Reply{Body: "Thanks", All: true}); err == nil || writes != 0 {
		t.Fatalf("reply all: err=%v writes=%d", err, writes)
	}
	if _, err := c.Mailbox().DraftReply(context.Background(), "original", model.Reply{Body: "Thanks"}); err != nil || writes != 1 {
		t.Fatalf("plain reply: err=%v writes=%d", err, writes)
	}
}
