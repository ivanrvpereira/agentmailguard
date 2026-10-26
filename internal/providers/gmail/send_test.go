package gmail_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

// sentMIME decodes a messages.send body: the Message resource itself, not a draft wrapper.
func sentMIME(t *testing.T, r *http.Request) (*mail.Message, map[string]any) {
	t.Helper()
	var payload map[string]any
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		t.Fatal("bad send JSON")
	}
	raw, err := base64.URLEncoding.DecodeString(payload["raw"].(string))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg, payload
}

func sendClient(t *testing.T, h http.HandlerFunc) *gmail.Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, UploadURL: server.URL + "/upload/drafts", SendUploadURL: server.URL + "/upload/send", Sleep: func(context.Context, time.Duration) error { return nil }})
}

func TestSendNewPostsThePlainMessage(t *testing.T) {
	sends := 0
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		sends++
		if r.Method != "POST" || r.URL.Path != "/messages/send" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		m, payload := sentMIME(t, r)
		if len(payload) != 1 {
			t.Errorf("new send has extra fields: %v", payload)
		}
		subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
		if m.Header.Get("To") != "a@example.test, b@example.test" || subject != "Olá" || m.Header.Get("In-Reply-To") != "" || m.Header.Get("Content-Type") != "text/plain; charset=utf-8" || draftText(t, m) != "Body\n" {
			t.Errorf("send MIME: %+v", m.Header)
		}
		respond(w, map[string]string{"id": "sent", "threadId": "t"})
	})
	if err := c.Mailbox().SendNew(context.Background(), model.NewDraft{To: []string{"a@example.test", "b@example.test"}, Subject: "Olá", Body: "Body"}); err != nil || sends != 1 {
		t.Fatalf("send=%v requests=%d", err, sends)
	}
}

func TestSendReplyPostsTheQuotedReplyInTheThread(t *testing.T) {
	sends := 0
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			p := part("text/plain", "One\nTwo")
			p["headers"] = []map[string]string{{"name": "From", "value": "Alice <alice@example.test>"}, {"name": "Subject", "value": "Plan"}, {"name": "Message-ID", "value": "<m@example.test>"}, {"name": "References", "value": "<r@example.test>"}}
			respond(w, message("original", p))
			return
		}
		sends++
		if r.Method != "POST" || r.URL.Path != "/messages/send" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		m, payload := sentMIME(t, r)
		if payload["threadId"] != "thread" || len(payload) != 2 {
			t.Errorf("thread: %v", payload)
		}
		if m.Header.Get("Subject") != "Re: Plan" || m.Header.Get("To") != "Alice <alice@example.test>" || m.Header.Get("In-Reply-To") != "<m@example.test>" || strings.Join(strings.Fields(m.Header.Get("References")), " ") != "<r@example.test> <m@example.test>" {
			t.Errorf("reply headers: %+v", m.Header)
		}
		if got := draftText(t, m); got != "Thanks\n\nOn 2026-09-01 00:00 UTC, alice@example.test wrote:\n> One\n> Two\n" {
			t.Errorf("quote: %q", got)
		}
		respond(w, map[string]string{"id": "sent"})
	})
	if err := c.Mailbox().SendReply(context.Background(), "original", "Thanks"); err != nil || sends != 1 {
		t.Fatalf("send=%v requests=%d", err, sends)
	}
}

func TestSendForwardUploadsTheOriginalToTheSendEndpoint(t *testing.T) {
	sends := 0
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			respond(w, map[string]any{"id": "o1", "threadId": "thread", "raw": base64.RawURLEncoding.EncodeToString([]byte(originalMIME))})
			return
		}
		sends++
		if r.Method != "POST" || r.URL.Path != "/upload/send" || r.URL.Query().Get("uploadType") != "multipart" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "multipart/related" {
			t.Fatal("not related")
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		metadata, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		if json.NewDecoder(metadata).Decode(&meta) != nil || !reflect.DeepEqual(meta, map[string]any{"threadId": "thread"}) {
			t.Errorf("metadata: %v", meta)
		}
		media, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(media)
		sent, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(originalMIME)) || sent.Header.Get("Subject") != "Fwd: Plan" || sent.Header.Get("To") != "a@example.test" || sent.Header.Get("In-Reply-To") != "<m@example.test>" {
			t.Errorf("forward MIME: %+v", sent.Header)
		}
		respond(w, map[string]string{"id": "sent"})
	})
	if err := c.Mailbox().SendForward(context.Background(), "o1", []string{"a@example.test"}); err != nil || sends != 1 {
		t.Fatalf("send=%v requests=%d", err, sends)
	}
}

func TestSendRefusesADraftSource(t *testing.T) {
	sends := 0
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			sends++
			return
		}
		m := message("d1", part("text/plain", "x"))
		m["labelIds"] = []string{"DRAFT"}
		m["raw"] = base64.RawURLEncoding.EncodeToString([]byte(originalMIME))
		respond(w, m)
	})
	b := c.Mailbox()
	if err := b.SendReply(context.Background(), "d1", "x"); err == nil {
		t.Fatal("reply to a draft sent")
	}
	if err := b.SendForward(context.Background(), "d1", []string{"a@example.test"}); err == nil {
		t.Fatal("forward of a draft sent")
	}
	if sends != 0 {
		t.Fatalf("sends=%d", sends)
	}
}

// A send that may have reached Gmail is never sent again: a duplicate send is worse than a failure.
func TestFailedSendIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		sends := 0
		c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				if r.URL.Query().Get("format") == "raw" {
					respond(w, map[string]any{"id": "o1", "threadId": "thread", "raw": base64.RawURLEncoding.EncodeToString([]byte(originalMIME))})
				} else {
					respond(w, message("o1", part("text/plain", "x")))
				}
				return
			}
			sends++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`)
		})
		b := c.Mailbox()
		ctx := context.Background()
		for _, send := range []func() error{
			func() error {
				return b.SendNew(ctx, model.NewDraft{To: []string{"a@example.test"}, Subject: "s", Body: "b"})
			},
			func() error { return b.SendReply(ctx, "o1", "b") },
			func() error { return b.SendForward(ctx, "o1", []string{"a@example.test"}) },
		} {
			before := sends
			if err := send(); err == nil {
				t.Fatalf("status %d: send succeeded", status)
			}
			if sends-before != 1 {
				t.Fatalf("status %d: %d requests for one send", status, sends-before)
			}
		}
	}
}

// A new draft and a new send carry To, Cc, and Bcc headers and a text/html part when the body is HTML. The draft
// result gives the message ID and thread ID from drafts.create, and the recipients and subject that were built.
func TestNewMessagesCarryCcBccAndTheHTMLPart(t *testing.T) {
	requests := []string{}
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		var m *mail.Message
		if r.URL.Path == "/drafts" {
			m, _ = draftMIME(t, r)
		} else {
			m, _ = sentMIME(t, r)
		}
		if m.Header.Get("To") != "a@example.test, b@example.test" || m.Header.Get("Cc") != "c@example.test" || m.Header.Get("Bcc") != "d@example.test, e@example.test" || m.Header.Get("Subject") != "Hi" {
			t.Errorf("recipient headers: %+v", m.Header)
		}
		if m.Header.Get("Content-Type") != "text/html; charset=utf-8" || draftText(t, m) != "<p>Hi <b>there</b></p>\n" {
			t.Errorf("html part: %+v", m.Header)
		}
		if r.URL.Path == "/drafts" {
			respond(w, map[string]any{"id": "r1", "message": map[string]any{"id": "created", "threadId": "t1", "labelIds": []string{"DRAFT"}}})
			return
		}
		respond(w, map[string]string{"id": "sent", "threadId": "t"})
	})
	d := model.NewDraft{To: []string{"a@example.test", "b@example.test"}, CC: []string{"c@example.test"}, BCC: []string{"d@example.test", "e@example.test"}, Subject: "Hi", Body: "<p>Hi <b>there</b></p>", HTML: true}
	draft, err := c.Mailbox().DraftNew(context.Background(), d)
	want := model.DraftResult{ID: "created", ThreadID: "t1", To: d.To, CC: d.CC, BCC: d.BCC, Subject: "Hi"}
	if err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	if err := c.Mailbox().SendNew(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requests, []string{"POST /drafts", "POST /messages/send"}) {
		t.Fatalf("requests=%v", requests)
	}
}

// A message read from Gmail carries its Bcc header, which Gmail keeps on drafts and sent mail.
func TestGetReadsTheBccHeader(t *testing.T) {
	c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/labels" {
			respond(w, map[string]any{})
			return
		}
		p := part("text/plain", "Body")
		p["headers"] = []map[string]string{{"name": "To", "value": "a@example.test"}, {"name": "Bcc", "value": "Ed <ed@example.test>, flo@example.test"}}
		respond(w, message("m", p))
	})
	got, err := c.Mailbox().Get(context.Background(), []string{"m"}, false)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].BCC, []model.Address{{Name: "Ed", Address: "ed@example.test"}, {Address: "flo@example.test"}}) {
		t.Fatalf("got=%+v %v", got, err)
	}
}
