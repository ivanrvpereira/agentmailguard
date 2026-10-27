package gmail_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// draftServer serves the draft "draft id" that holds message m on the second page of drafts.list, m in full with the
// payload and labels, and m in raw form as raw with the labels. upload gets each upload; other requests fail the
// test. It records each request.
func draftServer(t *testing.T, payload map[string]any, labels []string, raw string, upload func(w http.ResponseWriter, r *http.Request)) (*[]string, http.HandlerFunc) {
	requests := []string{}
	return &requests, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+" "+r.URL.Query().Get("format"))
		switch {
		case r.Method == "GET" && r.URL.Path == "/drafts" && r.URL.Query().Get("pageToken") == "":
			respond(w, map[string]any{"drafts": []any{map[string]any{"id": "other", "message": map[string]string{"id": "o"}}}, "nextPageToken": "next"})
		case r.Method == "GET" && r.URL.Path == "/drafts":
			respond(w, map[string]any{"drafts": []any{map[string]any{"id": "draft id", "message": map[string]string{"id": "m"}}}})
		case r.Method == "GET" && r.URL.Path == "/messages/m" && r.URL.Query().Get("format") == "full":
			v := message("m", payload)
			v["labelIds"] = labels
			respond(w, v)
		case r.Method == "GET" && r.URL.Path == "/messages/m" && r.URL.Query().Get("format") == "raw":
			respond(w, map[string]any{"id": "m", "threadId": "thread", "labelIds": labels, "raw": base64.URLEncoding.EncodeToString([]byte(raw))})
		case r.Method == "PUT":
			upload(w, r)
		default:
			t.Errorf("request %s %s", r.Method, r.URL)
		}
	}
}

// uploadedMedia gives the JSON metadata and the raw message/rfc822 media of a multipart upload.
func uploadedMedia(t *testing.T, r *http.Request) (map[string]any, string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || r.URL.Query().Get("uploadType") != "multipart" {
		t.Fatal("not a multipart upload")
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	var meta map[string]any
	if p, err := reader.NextPart(); err != nil || json.NewDecoder(p).Decode(&meta) != nil {
		t.Fatal("metadata", err)
	}
	p, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	media, err := io.ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	return meta, string(media)
}

// textPart is the text part of kind that a draft update writes for text: base64 of the text with CRLF line ends and
// a final line end, in lines of 76 characters.
func textPart(kind, text string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	lines := []string{}
	for len(encoded) > 76 {
		lines, encoded = append(lines, encoded[:76]), encoded[76:]
	}
	return "Content-Type: " + kind + "; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + strings.Join(append(lines, encoded), "\r\n")
}

// draftParts are the parts of a reply draft that the user wrote in Gmail, other than its body: an inline image under
// multipart/related, an unnamed text/calendar part with a Content-ID, an attached message, and a named PDF.
const (
	draftImage    = "Content-Type: image/png; name=logo.png\r\nContent-Disposition: inline; filename=logo.png\r\nContent-ID: <logo@x>\r\nContent-Transfer-Encoding: base64\r\n\r\nUE5H\r\n"
	draftCalendar = "Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\nContent-ID: <cal@x>\r\n\r\nBEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"
	draftMessage  = "Content-Type: message/rfc822\r\n\r\nFrom: b@example.test\r\nSubject: Old\r\n\r\nhi\r\n"
	draftPDF      = "Content-Type: application/pdf; name=a.pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERg==\r\n"
	draftBody     = "Content-Type: multipart/alternative; boundary=\"alt\"\r\n\r\n--alt\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOld\r\n> quote\r\n--alt\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Old <img src=\"cid:logo@x\"></p>\r\n--alt--\r\n"
	// draftHeaders are the top-level headers of a reply draft, with a folded References header and a boundary with spaces.
	draftHeaders = "From: Ana <ana@example.test>\r\nTo: bo@example.test\r\nCc: cy@example.test\r\nBcc: di@example.test\r\nSubject: Re: Plan\r\nIn-Reply-To: <m0@example.test>\r\n" +
		"References: <r1@example.test> <r2@example.test>\r\n <r3@example.test>\r\n\t<m0@example.test>\r\nDate: Tue, 1 Sep 2026 10:00:00 +0000\r\nMessage-ID: <old@example.test>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed;\r\n boundary=\"mix  sp\"\r\n"
)

// draftContent is the multipart/mixed content of a reply draft whose related part starts with main.
func draftContent(main string) string {
	return "--mix  sp\r\nContent-Type: multipart/related; boundary=\"rel\"\r\n\r\n--rel\r\n" + main + "--rel\r\n" + draftImage + "--rel--\r\n" +
		"--mix  sp\r\n" + draftCalendar + "--mix  sp\r\n" + draftMessage + "--mix  sp\r\n" + draftPDF + "--mix  sp--\r\n"
}

// A draft update with a body rebuilds the draft from its raw MIME through drafts.update. The top-level headers stay
// byte for byte (a folded References and a boundary with spaces included) except Date and Message-ID and the changed
// headers, which are written new. The body (here the multipart/alternative of the text and HTML, which hold only
// text) becomes one text part in base64 at its place, and every other part stays byte for byte where it was. The
// update names the draft's thread, and the result is the new message ID from Gmail's answer.
func TestDraftUpdateWithABodyKeepsTheThreadReplyHeadersAndEveryOtherPart(t *testing.T) {
	raw := draftHeaders + "\r\n" + draftContent(draftBody)
	requests, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, r *http.Request) {
		meta, media := uploadedMedia(t, r)
		if !reflect.DeepEqual(meta, map[string]any{"id": "draft id", "message": map[string]any{"threadId": "thread"}}) {
			t.Errorf("metadata %v", meta)
		}
		want := "From: Ana <ana@example.test>\r\nCc: cy@example.test\r\nBcc: di@example.test\r\nSubject: Re: Plan\r\nIn-Reply-To: <m0@example.test>\r\n" +
			"References: <r1@example.test> <r2@example.test>\r\n <r3@example.test>\r\n\t<m0@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed;\r\n boundary=\"mix  sp\"\r\n" +
			"To: <ed@example.test>\r\n\r\n" + draftContent(textPart("text/html", "<p>New</p>\r\n")+"\r\n")
		if media != want {
			t.Errorf("message %q\nwant    %q", media, want)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed", "threadId": "thread"}})
	})
	id, thread, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{To: new([]string{"ed@example.test"}), Body: new("<p>New</p>"), HTML: true})
	if err != nil || id != "changed" || thread != "thread" {
		t.Fatalf("update=%q %q %v", id, thread, err)
	}
	if want := []string{"GET /drafts ", "GET /drafts ", "GET /messages/m raw", "PUT /upload/drafts/draft id "}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%q", *requests)
	}
}

// In alternative{text, related{html, image}}, the update replaces the HTML that the agent reads, drops the old text
// alternative, and keeps the inline image in its multipart/related.
func TestDraftUpdateWithABodyKeepsTheInlineImagesOfTheHTMLAlternative(t *testing.T) {
	related := "Content-Type: multipart/related; boundary=\"rel\"\r\n\r\n--rel\r\n%s--rel\r\n" + draftImage + "--rel--\r\n"
	raw := "Subject: Plan\r\nContent-Type: multipart/alternative; boundary=\"alt\"\r\n\r\n--alt\r\nContent-Type: text/plain\r\n\r\nOld\r\n--alt\r\n" +
		fmt.Sprintf(related, "Content-Type: text/html\r\n\r\n<p>Old</p>\r\n") + "--alt--\r\n"
	want := "Subject: Plan\r\nContent-Type: multipart/alternative; boundary=\"alt\"\r\nMIME-Version: 1.0\r\n\r\n--alt\r\n" + fmt.Sprintf(related, textPart("text/html", "<p>New</p>\r\n")+"\r\n") + "--alt--\r\n"
	_, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, r *http.Request) {
		if _, media := uploadedMedia(t, r); media != want {
			t.Errorf("message %q\nwant    %q", media, want)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed"}})
	})
	if _, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Body: new("<p>New</p>"), HTML: true}); err != nil {
		t.Fatal(err)
	}
}

// A body line that is a delimiter of the draft's boundaries cannot change the MIME structure: the new body goes in
// base64, so the draft keeps its parts and the body decodes to the agent's text.
func TestDraftUpdateBodyCannotFormAMIMEDelimiter(t *testing.T) {
	text := "Hi\n--mix  sp\nContent-Type: text/html\n\n<b>planted</b>\n--rel--\n--mix  sp--"
	_, h := draftServer(t, nil, []string{"DRAFT"}, draftHeaders+"\r\n"+draftContent(draftBody), func(w http.ResponseWriter, r *http.Request) {
		_, media := uploadedMedia(t, r)
		m, err := mail.ReadMessage(strings.NewReader(media))
		if err != nil {
			t.Fatal(err)
		}
		mixed := multipart.NewReader(m.Body, "mix  sp")
		var parts []string
		var decoded []byte
		for {
			p, err := mixed.NextRawPart()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, p.Header.Get("Content-Type"))
			if len(parts) == 1 {
				first, err := multipart.NewReader(p, "rel").NextRawPart()
				if err != nil {
					t.Fatal(err)
				}
				decoded, _ = io.ReadAll(base64.NewDecoder(base64.StdEncoding, first))
			}
		}
		if want := []string{"multipart/related; boundary=\"rel\"", "text/calendar; method=REQUEST; charset=utf-8", "message/rfc822", "application/pdf; name=a.pdf"}; !reflect.DeepEqual(parts, want) || string(decoded) != strings.ReplaceAll(text, "\n", "\r\n")+"\r\n" {
			t.Errorf("parts %q body %q", parts, decoded)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed"}})
	})
	if _, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Body: new(text)}); err != nil {
		t.Fatal(err)
	}
}

// A draft that is one text part becomes one new text part, plain text here, with the draft's other headers.
func TestDraftUpdateWithABodyReplacesASinglePartDraft(t *testing.T) {
	raw := "To: bo@example.test\r\nSubject: Plan\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Old</p>\r\n"
	_, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, r *http.Request) {
		_, media := uploadedMedia(t, r)
		if want := "To: bo@example.test\r\nSubject: Plan\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nNew\r\n"; media != want {
			t.Errorf("message %q\nwant    %q", media, want)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed", "threadId": "thread"}})
	})
	if id, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Body: new("New")}); err != nil || id != "changed" {
		t.Fatalf("update=%q %v", id, err)
	}
}

// A draft with only an attachment gets the new body as the first part of its multipart/mixed.
func TestDraftUpdateWithABodyAddsTheBodyToAnAttachmentOnlyDraft(t *testing.T) {
	raw := "Subject: Plan\r\nContent-Type: multipart/mixed; boundary=\"mix\"\r\n\r\n--mix\r\n" + draftPDF + "--mix--\r\n"
	want := "Subject: Plan\r\nContent-Type: multipart/mixed; boundary=\"mix\"\r\nMIME-Version: 1.0\r\n\r\n--mix\r\n" + textPart("text/plain", "New\r\n") + "\r\n--mix\r\n" + draftPDF + "--mix--\r\n"
	_, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, r *http.Request) {
		if _, media := uploadedMedia(t, r); media != want {
			t.Errorf("message %q\nwant    %q", media, want)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed"}})
	})
	if _, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Body: new("New")}); err != nil {
		t.Fatal(err)
	}
}

// A body update fails before any write when the draft cannot take a body (one attachment and nothing else) or its MIME
// does not parse: a multipart without a boundary or a close delimiter, or nested deeper than the daemon walks.
func TestDraftUpdateWithABodyRefusesADraftThatCannotTakeOne(t *testing.T) {
	deep := "Content-Type: text/plain\r\n\r\nhi\r\n"
	for i := range 102 {
		b := fmt.Sprint("b", i)
		deep = "Content-Type: multipart/mixed; boundary=" + b + "\r\n\r\n--" + b + "\r\n" + deep + "--" + b + "--\r\n"
	}
	for name, raw := range map[string]string{
		"attachment only":    "Content-Type: application/pdf\r\n\r\nJVBERg==\r\n",
		"no boundary":        "Content-Type: multipart/mixed\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nhi\r\n--x--\r\n",
		"no close delimiter": "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nhi\r\n",
		"too deep":           "Subject: Deep\r\n" + deep,
		"no end of header":   "Subject: Plan",
		// The text to drop is the only part of its multipart/related, which would be left empty.
		"text alone in its part": "Content-Type: multipart/mixed; boundary=mix\r\n\r\n--mix\r\nContent-Type: multipart/related; boundary=r1\r\n\r\n--r1\r\nContent-Type: text/plain\r\n\r\nOld\r\n--r1--\r\n" +
			"--mix\r\nContent-Type: multipart/related; boundary=r2\r\n\r\n--r2\r\nContent-Type: text/html\r\n\r\n<p>Old</p>\r\n--r2\r\n" + draftImage + "--r2--\r\n--mix--\r\n",
		"too many parts": "Content-Type: multipart/mixed; boundary=x\r\n\r\n" + strings.Repeat("--x\r\nContent-Type: application/pdf\r\n\r\nA\r\n", 100001) + "--x--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("write")
				respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed"}})
			})
			if _, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Body: new("New")}); err == nil {
				t.Fatal("update accepted")
			}
		})
	}
}

// Without a body, a draft update keeps the MIME body exactly as Gmail stores it and the other headers byte for byte,
// and changes only the given headers: a new subject, and an empty CC list that removes the Cc header.
func TestDraftUpdateWithoutABodyKeepsTheMIMEBodyAsItIs(t *testing.T) {
	body := "--b1\r\nContent-Type: text/plain\r\n\r\nOld\r\n--b1\r\nContent-Type: text/html\r\n\r\n<p>Old</p>\r\n--b1--\r\n"
	raw := "From: Ana <ana@example.test>\r\nTo: bo@example.test\r\nCc: cy@example.test\r\nSubject: Re: Plan\r\nIn-Reply-To: <m0@example.test>\r\nContent-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" + body
	requests, h := draftServer(t, nil, []string{"DRAFT"}, raw, func(w http.ResponseWriter, r *http.Request) {
		want := "From: Ana <ana@example.test>\r\nTo: bo@example.test\r\nIn-Reply-To: <m0@example.test>\r\nContent-Type: multipart/alternative; boundary=\"b1\"\r\nSubject: New plan\r\nMIME-Version: 1.0\r\n\r\n" + body
		if _, media := uploadedMedia(t, r); media != want {
			t.Errorf("message %q\nwant    %q", media, want)
		}
		respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed", "threadId": "thread"}})
	})
	id, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{CC: new([]string{}), Subject: new("New plan")})
	if err != nil || id != "changed" {
		t.Fatalf("update=%q %v", id, err)
	}
	if want := []string{"GET /drafts ", "GET /drafts ", "GET /messages/m raw", "PUT /upload/drafts/draft id "}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%q", *requests)
	}
}

// A message that no draft holds, or that has no DRAFT label, is refused before any write.
func TestDraftUpdateRefusesAMessageThatIsNotADraftBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
		id     string
	}{{"no draft holds it", []string{"DRAFT"}, "o2"}, {"no DRAFT label", []string{"INBOX"}, "m"}} {
		t.Run(tc.name, func(t *testing.T) {
			requests, h := draftServer(t, nil, tc.labels, "Subject: Old\r\n\r\nOld\r\n", func(w http.ResponseWriter, _ *http.Request) {
				t.Error("write")
				respond(w, map[string]any{"id": "draft id", "message": map[string]any{"id": "changed"}})
			})
			if _, _, err := sendClient(t, h).Mailbox().DraftUpdate(context.Background(), tc.id, model.DraftChange{Subject: new("New")}); err == nil {
				t.Fatalf("update accepted after %q", *requests)
			}
		})
	}
}

// ReadDraft reads the recipients, the decoded subject, and the HTML body of a draft. A message without the DRAFT label
// gives only IsDraft false, and a recipient header that does not parse fails.
func TestReadDraftMapsTheDraftFields(t *testing.T) {
	payload := map[string]any{"mimeType": "multipart/alternative", "headers": headers("To", "Bo <bo@example.test>, cy@example.test", "Bcc", "di@example.test", "Subject", "=?utf-8?q?Caf=C3=A9?="),
		"parts": []any{part("text/plain", "Old"), part("text/html", "<p>Old</p>")}}
	_, h := draftServer(t, payload, []string{"DRAFT"}, "", nil)
	got, err := sendClient(t, h).Mailbox().ReadDraft(context.Background(), "m")
	want := model.DraftContent{IsDraft: true, To: []string{"bo@example.test", "cy@example.test"}, CC: []string{}, BCC: []string{"di@example.test"}, Subject: "Café", Body: "<p>Old</p>", HTML: true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("draft=%+v %v", got, err)
	}
	_, h = draftServer(t, payload, []string{"INBOX"}, "", nil)
	if got, err := sendClient(t, h).Mailbox().ReadDraft(context.Background(), "m"); err != nil || !reflect.DeepEqual(got, model.DraftContent{}) {
		t.Fatalf("received message=%+v %v", got, err)
	}
	payload["headers"] = headers("To", "<<broken")
	_, h = draftServer(t, payload, []string{"DRAFT"}, "", nil)
	if _, err := sendClient(t, h).Mailbox().ReadDraft(context.Background(), "m"); err == nil || strings.Contains(err.Error(), "broken") {
		t.Fatalf("broken To accepted: %v", err)
	}
}
