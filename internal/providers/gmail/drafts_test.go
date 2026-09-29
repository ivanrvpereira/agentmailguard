package gmail_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

func draftMIME(t *testing.T, r *http.Request) (*mail.Message, map[string]any) {
	t.Helper()
	var payload struct{ Message map[string]any }
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		t.Fatal("bad draft JSON")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload.Message["raw"].(string), "="))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return msg, payload.Message
}
func draftText(t *testing.T, m *mail.Message) string {
	t.Helper()
	r := m.Body
	if m.Header.Get("Content-Transfer-Encoding") == "quoted-printable" {
		r = quotedprintable.NewReader(r)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
func TestNewAndReplyDraftsSafeHeadersAndThreading(t *testing.T) {
	mode := "new"
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			p := part("text/html", "<div>One</div><p>Two <b>x</b></p><script>y</script>")
			p["headers"] = []map[string]string{{"name": "From", "value": "Alice <alice@example.test>"}, {"name": "Reply-To", "value": "list@example.test"}, {"name": "Subject", "value": "=?utf-8?q?caf=C3=A9?="}, {"name": "Message-ID", "value": "<m@example.test>\r\nBcc: evil@example.test"}, {"name": "References", "value": "<r@example.test> <bad id>"}}
			respond(w, message("original", p))
			return
		}
		writes++
		if r.Method != "POST" || r.URL.Path != "/drafts" {
			t.Error("not draft create")
		}
		m, payload := draftMIME(t, r)
		if m.Header.Get("From") != "" || m.Header.Get("Bcc") != "" {
			t.Error("injected header")
		}
		if mode == "new" {
			if m.Header.Get("In-Reply-To") != "" || m.Header.Get("Content-Type") != "text/plain; charset=utf-8" || draftText(t, m) != "Body\n" {
				t.Error("new draft MIME")
			}
			if len(payload) != 1 {
				t.Error("new draft has threading")
			}
		} else {
			subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
			if subject != "Re: café" || m.Header.Get("To") != "list@example.test" || m.Header.Get("In-Reply-To") != "<m@example.test>" || strings.Join(strings.Fields(m.Header.Get("References")), " ") != "<r@example.test> <m@example.test>" || payload["threadId"] != "thread" {
				t.Errorf("reply headers: %+v", m.Header)
			}
			if got := draftText(t, m); got != "Thanks\n\nOn 2026-09-01 00:00 UTC, alice@example.test wrote:\n> One\n> Two \n> x\n" {
				t.Errorf("quote: %q", got)
			}
		}
		respond(w, map[string]any{"message": map[string]string{"id": "created"}})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	id, err := c.Mailbox().DraftNew(context.Background(), model.NewDraft{To: []string{"a@example.test\r\nBcc: evil@example.test"}, Subject: "Hi\nBcc: evil@example.test", Body: "Body"})
	if err != nil || id != "created" {
		t.Fatal(id, err)
	}
	mode = "reply"
	id, err = c.Mailbox().DraftReply(context.Background(), "original", "Thanks")
	if err != nil || id != "created" {
		t.Fatal(id, err)
	}
	_, err = c.Mailbox().DraftNew(context.Background(), model.NewDraft{To: []string{"=?utf-8?q?x=0D=0ABcc:_evil@example.test?= <a@example.test>"}})
	if err == nil || writes != 2 {
		t.Fatal("encoded newline accepted")
	}
}
