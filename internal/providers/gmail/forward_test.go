package gmail_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

const originalMIME = "From: Alice <alice@example.test>\r\nTo: bob@example.test\r\nSubject: Plan\r\nMessage-ID: <m@example.test>\r\nReferences: <r@example.test> <日本@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOlá BODY\r\n--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERg==\r\n--b1--\r\n"

func TestForwardDraftPreservesOriginalBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if r.URL.RawPath != "/messages/o%2F..%2Fx%3Fy" || r.URL.Query().Get("format") != "raw" {
				t.Error("forward raw read path")
			}
			respond(w, map[string]any{"id": "o/../x?y", "threadId": "thread", "raw": base64.RawURLEncoding.EncodeToString([]byte(originalMIME))})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/upload/drafts" || r.URL.Query().Get("uploadType") != "multipart" {
			t.Error("not draft multipart upload")
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
		var meta map[string]map[string]string
		if json.NewDecoder(metadata).Decode(&meta) != nil || meta["message"]["threadId"] != "thread" {
			t.Fatal("thread metadata")
		}
		media, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(media)
		if err != nil || !bytes.Contains(raw, []byte(originalMIME)) {
			t.Fatal("original was changed")
		}
		draft, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if draft.Header.Get("Subject") != "Fwd: Plan" || draft.Header.Get("To") != "a@example.test, b@example.test" || draft.Header.Get("From") != "" || strings.Join(strings.Fields(draft.Header.Get("References")), " ") != "<r@example.test> <m@example.test>" {
			t.Fatalf("forward headers: %+v", draft.Header)
		}
		_, params, err = mime.ParseMediaType(draft.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		parts := multipart.NewReader(draft.Body, params["boundary"])
		if _, err = parts.NextPart(); err != nil {
			t.Fatal(err)
		}
		attachment, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if attachment.Header.Get("Content-Type") != "message/rfc822" || attachment.Header.Get("Content-Transfer-Encoding") != "8bit" || attachment.FileName() != "forwarded.eml" {
			t.Fatal("forward attachment MIME")
		}
		attached, _ := io.ReadAll(attachment)
		if string(attached) != originalMIME {
			t.Fatalf("original changed, length %d", len(attached))
		}
		if _, err = parts.NextPart(); err != io.EOF {
			t.Fatal("unexpected attachment")
		}
		respond(w, map[string]any{"message": map[string]string{"id": "forwarded"}})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, UploadURL: server.URL + "/upload/drafts"})
	id, err := c.Mailbox().DraftForward(context.Background(), "o/../x?y", []string{"a@example.test", "b@example.test"})
	if err != nil || id != "forwarded" {
		t.Fatal(id, err)
	}
}
