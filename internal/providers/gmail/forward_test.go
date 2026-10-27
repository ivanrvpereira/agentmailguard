package gmail_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// forwardOriginal serves message o1 with the payload, and each attachment of attachments by its attachment ID through
// messages.attachments.get. writes gets each upload; other requests fail the test. It records each request.
func forwardOriginal(t *testing.T, payload map[string]any, attachments map[string][]byte, writes func(w http.ResponseWriter, r *http.Request)) (*[]string, http.HandlerFunc) {
	requests := []string{}
	return &requests, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		id, attachment := strings.CutPrefix(r.URL.Path, "/messages/o1/attachments/")
		switch {
		case r.Method == "GET" && r.URL.Path == "/messages/o1":
			if r.URL.Query().Get("format") != "full" {
				t.Errorf("format %q", r.URL.Query().Get("format"))
			}
			m := message("o1", payload)
			m["labelIds"] = []string{"INBOX"}
			respond(w, m)
		case r.Method == "GET" && attachment && attachments[id] != nil:
			respond(w, map[string]any{"attachmentId": id, "size": len(attachments[id]), "data": base64.URLEncoding.EncodeToString(attachments[id])})
		case r.Method == "POST":
			writes(w, r)
		default:
			t.Errorf("request %s %s", r.Method, r.URL)
		}
	}
}

// uploadedMIME decodes a multipart upload: its JSON metadata and the message/rfc822 media.
func uploadedMIME(t *testing.T, r *http.Request) (*mail.Message, map[string]any) {
	t.Helper()
	if r.URL.Query().Get("uploadType") != "multipart" {
		t.Errorf("upload type %q", r.URL.Query().Get("uploadType"))
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
	if json.NewDecoder(metadata).Decode(&meta) != nil {
		t.Fatal("metadata")
	}
	media, err := reader.NextPart()
	if err != nil || media.Header.Get("Content-Type") != "message/rfc822" {
		t.Fatal("media", err)
	}
	m, err := mail.ReadMessage(media)
	if err != nil {
		t.Fatal(err)
	}
	return m, meta
}

// mimePart is one decoded leaf part of a message: the multipart types that hold it (multipart/related with its type
// parameter), its Content-Type, Content-Disposition, and Content-ID, and its body.
type mimePart struct {
	container, kind, disposition, cid, body string
}

// messageParts decodes each leaf part of a message, its body by its transfer encoding.
func messageParts(t *testing.T, m *mail.Message) []mimePart {
	t.Helper()
	return leafParts(t, "", textproto.MIMEHeader(m.Header), m.Body)
}

func leafParts(t *testing.T, container string, h textproto.MIMEHeader, r io.Reader) []mimePart {
	t.Helper()
	kind, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type %q", h.Get("Content-Type"))
	}
	if strings.HasPrefix(kind, "multipart/") {
		if params["type"] != "" {
			kind += "(" + params["type"] + ")"
		}
		out := []mimePart{}
		reader := multipart.NewReader(r, params["boundary"])
		for {
			p, err := reader.NextRawPart()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, leafParts(t, container+kind+">", p.Header, p)...)
		}
	}
	switch h.Get("Content-Transfer-Encoding") {
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	default:
		t.Fatalf("transfer encoding %q", h.Get("Content-Transfer-Encoding"))
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return []mimePart{{container, h.Get("Content-Type"), h.Get("Content-Disposition"), h.Get("Content-ID"), strings.ReplaceAll(string(data), "\r\n", "\n")}}
}

// headers is the Gmail header list of name, value pairs.
func headers(pairs ...string) []map[string]string {
	out := []map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, map[string]string{"name": pairs[i], "value": pairs[i+1]})
	}
	return out
}

// attachmentPart is a Gmail attachment part whose data Gmail gives through messages.attachments.get.
func attachmentPart(kind, filename, id string, size int, extra ...string) map[string]any {
	return map[string]any{"mimeType": kind, "filename": filename, "headers": headers(append([]string{"Content-Disposition", "attachment; filename=\"" + filename + "\""}, extra...)...), "body": map[string]any{"attachmentId": id, "size": size}}
}

// A forward is inline, as Gmail on the web and Google's CLI forward: the note, then the "Forwarded message" block
// with the original's From, Date, Subject, To, and Cc, then the original text. The original's regular attachments
// are attached again, fetched with messages.attachments.get when Gmail gives no data inline; an inline image and the
// original itself as .eml are not. The draft stays in the thread, and its result gives what was built.
func TestForwardDraftIsInlineWithTheOriginalAttachments(t *testing.T) {
	inline := part("text/plain", "hi")
	inline["filename"], inline["headers"] = "notes.txt", headers("Content-Disposition", "attachment; filename=notes.txt")
	image := part("image/png", "PNG")
	image["filename"], image["headers"] = "logo.png", headers("Content-Disposition", "inline; filename=logo.png", "Content-ID", "<logo>")
	payload := map[string]any{"mimeType": "multipart/mixed", "headers": headers("From", "Ana <ana@example.test>", "To", "synthetic@example.test, Bo <bo@example.test>", "Cc", "cy@example.test",
		"Date", "Tue, 1 Sep 2026 10:00:00 +0000", "Subject", "=?utf-8?q?Plan_caf=C3=A9?=", "Message-ID", "<m@example.test>", "References", "<r@example.test>"),
		"parts": []any{map[string]any{"mimeType": "multipart/alternative", "parts": []any{part("text/plain", "One\r\nTwo"), part("text/html", "<p>One</p>")}},
			attachmentPart("application/pdf", "a.pdf", "att1", 4), inline, image}}
	var built []mimePart
	requests, h := forwardOriginal(t, payload, map[string][]byte{"att1": []byte("%PDF")}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload/drafts" {
			t.Errorf("upload %s", r.URL.Path)
		}
		m, meta := uploadedMIME(t, r)
		if !reflect.DeepEqual(meta, map[string]any{"message": map[string]any{"threadId": "thread"}}) {
			t.Errorf("metadata %v", meta)
		}
		subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
		got := [][]string{headerAddresses(t, m, "To"), headerAddresses(t, m, "Cc"), headerAddresses(t, m, "Bcc"), {subject, m.Header.Get("In-Reply-To"), strings.Join(strings.Fields(m.Header.Get("References")), " ")}}
		if want := [][]string{{"a@example.test"}, {"c@example.test"}, {"d@example.test"}, {"Fwd: Plan café", "<m@example.test>", "<r@example.test> <m@example.test>"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("headers %v", got)
		}
		built = messageParts(t, m)
		respond(w, map[string]any{"id": "f1", "message": map[string]any{"id": "created", "threadId": "thread"}})
	})
	c := sendClient(t, h)
	draft, err := c.Mailbox().DraftForward(context.Background(), "o1", model.Forward{Body: "FYI", To: []string{"a@example.test"}, CC: []string{"c@example.test"}, BCC: []string{"d@example.test"}})
	if want := (model.DraftResult{ID: "created", ThreadID: "thread", To: []string{"a@example.test"}, CC: []string{"c@example.test"}, BCC: []string{"d@example.test"}, Subject: "Fwd: Plan café"}); err != nil || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	text := "FYI\n\n---------- Forwarded message ---------\nFrom: Ana <ana@example.test>\nDate: Tue, 1 Sep 2026 10:00:00 +0000\nSubject: Plan café\nTo: synthetic@example.test, Bo <bo@example.test>\nCc: cy@example.test\n\nOne\nTwo\n"
	want := []mimePart{{"multipart/mixed>", "text/plain; charset=utf-8", "", "", text}, {"multipart/mixed>", "application/pdf; name=a.pdf", "attachment; filename=a.pdf", "", "%PDF"}, {"multipart/mixed>", "text/plain; name=notes.txt", "attachment; filename=notes.txt", "", "hi"}}
	if !reflect.DeepEqual(built, want) {
		t.Fatalf("parts %q", built)
	}
	if want := []string{"GET /messages/o1", "GET /messages/o1/attachments/att1", "POST /upload/drafts"}; !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%v", *requests)
	}
}

// An HTML forward puts the agent's HTML above the header block, whose values from the original are escaped, and the
// original HTML part below it, or the escaped original text when there is no HTML part. A subject that starts with
// fwd: or fw: in any case gets no second prefix, and an original without Date or Cc gets no such line. Without
// attachments, the forward is one text/html part; mail forward sends it to the send upload in the thread.
func TestSendForwardHTMLPutsTheOriginalBelowTheEscapedHeaderBlock(t *testing.T) {
	for _, tc := range []struct {
		name, kind, original, quoted, subject string
	}{
		{"html original", "text/html", "<p>One &amp; two</p>", "<p>One &amp; two</p>", "FWD: Plan"},
		{"text original", "text/plain", "a < b\nc", "a &lt; b<br>c", "fw: Plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := part(tc.kind, tc.original)
			p["headers"] = headers("From", "Ana <ana@example.test>", "To", "x&y <synthetic@example.test>", "Subject", tc.subject)
			sends := 0
			requests, h := forwardOriginal(t, p, nil, func(w http.ResponseWriter, r *http.Request) {
				sends++
				if r.URL.Path != "/upload/send" {
					t.Errorf("upload %s", r.URL.Path)
				}
				m, meta := uploadedMIME(t, r)
				if !reflect.DeepEqual(meta, map[string]any{"threadId": "thread"}) || m.Header.Get("Subject") != tc.subject || m.Header.Get("Content-Type") != "text/html; charset=utf-8" {
					t.Errorf("metadata %v headers %v", meta, m.Header)
				}
				want := `<p>FYI</p><br><br><div class="gmail_quote gmail_quote_container"><div dir="ltr" class="gmail_attr">---------- Forwarded message ---------<br>From: Ana &lt;ana@example.test&gt;<br>Subject: ` + tc.subject + `<br>To: x&amp;y &lt;synthetic@example.test&gt;<br></div><br><br>` + tc.quoted + "</div>\n"
				if got := draftText(t, m); got != want {
					t.Errorf("body %q\nwant %q", got, want)
				}
				respond(w, map[string]any{"id": "sent"})
			})
			c := sendClient(t, h)
			if err := c.Mailbox().SendForward(context.Background(), "o1", model.Forward{Body: "<p>FYI</p>", HTML: true, BCC: []string{"d@example.test"}}); err != nil || sends != 1 {
				t.Fatalf("send=%v sends=%d", err, sends)
			}
			if want := []string{"GET /messages/o1", "POST /upload/send"}; !reflect.DeepEqual(*requests, want) {
				t.Fatalf("requests=%v", *requests)
			}
		})
	}
}

// An attached message (message/rfc822 or any message/*) goes as application/octet-stream in base64, because RFC 2046
// section 5.2.1 allows no base64 for message/rfc822. It keeps its file name, or is named forwarded.eml without one.
func TestForwardAttachesMessagesAsOctetStream(t *testing.T) {
	named := attachmentPart("message/rfc822", "old.eml", "eml1", 14)
	named["parts"] = []any{part("text/plain", "hi")}
	unnamed := part("message/rfc822", "From: b\r\n\r\nyo")
	payload := map[string]any{"mimeType": "multipart/mixed", "headers": headers("From", "ana@example.test", "To", "synthetic@example.test", "Subject", "Plan"),
		"parts": []any{part("text/plain", "One"), named, unnamed}}
	var built []mimePart
	_, h := forwardOriginal(t, payload, map[string][]byte{"eml1": []byte("From: a\r\n\r\nhi")}, func(w http.ResponseWriter, r *http.Request) {
		m, _ := uploadedMIME(t, r)
		built = messageParts(t, m)
		respond(w, map[string]any{"id": "sent"})
	})
	if err := sendClient(t, h).Mailbox().SendForward(context.Background(), "o1", model.Forward{To: []string{"a@example.test"}}); err != nil || len(built) != 3 {
		t.Fatalf("send=%v parts=%q", err, built)
	}
	want := []mimePart{{"multipart/mixed>", "application/octet-stream; name=old.eml", "attachment; filename=old.eml", "", "From: a\n\nhi"}, {"multipart/mixed>", "application/octet-stream; name=forwarded.eml", "attachment; filename=forwarded.eml", "", "From: b\n\nyo"}}
	if !reflect.DeepEqual(built[1:], want) {
		t.Fatalf("parts %q", built[1:])
	}
}

// inlineOriginal is an HTML original with a cid: image in a multipart/related, an inline image without a file name,
// an inline text part with a Content-ID, and an image attachment with a Content-ID.
func inlineOriginal() map[string]any {
	html := part("text/html", `<p>Hi <img src="cid:logo@x"></p>`)
	alternative := map[string]any{"mimeType": "multipart/alternative", "parts": []any{part("text/plain", "Hi"), html}}
	logo := map[string]any{"mimeType": "image/png", "filename": "logo.png", "headers": headers("Content-Disposition", "inline; filename=logo.png", "Content-ID", "<logo@x>"), "body": map[string]any{"attachmentId": "img1", "size": 3}}
	dot := part("image/gif", "GIF")
	dot["headers"] = headers("Content-ID", "<dot>")
	notes := part("text/plain", "hi")
	notes["filename"], notes["headers"] = "notes.txt", headers("Content-Disposition", "inline; filename=notes.txt", "Content-ID", "<n>")
	photo := part("image/jpeg", "JPG")
	photo["filename"], photo["headers"] = "photo.jpg", headers("Content-Disposition", "attachment; filename=photo.jpg", "Content-ID", "<p>")
	return map[string]any{"mimeType": "multipart/mixed", "headers": headers("From", "Ana <ana@example.test>", "To", "synthetic@example.test", "Subject", "Plan"),
		"parts": []any{map[string]any{"mimeType": "multipart/related", "parts": []any{alternative, logo, dot}}, notes, photo}}
}

// An HTML forward carries the original's inline images (image/* with a Content-ID, not marked attachment) with their
// Content-ID in a multipart/related beside the HTML part, so the quoted cid: images show; the regular attachments
// stay beside it in the multipart/mixed. A plain-text forward drops the inline images, as Google's CLI does, and never
// reads them. Every other part with a file name, an inline text part with a Content-ID included, is a regular
// attachment.
func TestForwardCarriesInlineImagesOnlyBesideHTML(t *testing.T) {
	regular := []mimePart{{"multipart/mixed>", "text/plain; name=notes.txt", "attachment; filename=notes.txt", "", "hi"}, {"multipart/mixed>", "image/jpeg; name=photo.jpg", "attachment; filename=photo.jpg", "", "JPG"}}
	quote := `<br><br><div class="gmail_quote gmail_quote_container"><div dir="ltr" class="gmail_attr">---------- Forwarded message ---------<br>From: Ana &lt;ana@example.test&gt;<br>Subject: Plan<br>To: synthetic@example.test<br></div><br><br>`
	for _, tc := range []struct {
		name     string
		forward  model.Forward
		want     []mimePart
		requests []string
	}{
		{"html", model.Forward{Body: "<p>FYI</p>", HTML: true}, append([]mimePart{
			{"multipart/mixed>multipart/related(text/html)>", "text/html; charset=utf-8", "", "", "<p>FYI</p>" + quote + `<p>Hi <img src="cid:logo@x"></p></div>` + "\n"},
			{"multipart/mixed>multipart/related(text/html)>", "image/png; name=logo.png", "inline; filename=logo.png", "<logo@x>", "PNG"},
			{"multipart/mixed>multipart/related(text/html)>", "image/gif", "inline", "<dot>", "GIF"}}, regular...),
			[]string{"GET /messages/o1", "GET /messages/o1/attachments/img1", "POST /upload/drafts"}},
		{"plain", model.Forward{Body: "FYI"}, append([]mimePart{
			{"multipart/mixed>", "text/plain; charset=utf-8", "", "", "FYI\n\n---------- Forwarded message ---------\nFrom: Ana <ana@example.test>\nSubject: Plan\nTo: synthetic@example.test\n\nHi\n"}}, regular...),
			[]string{"GET /messages/o1", "POST /upload/drafts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var built []mimePart
			requests, h := forwardOriginal(t, inlineOriginal(), map[string][]byte{"img1": []byte("PNG")}, func(w http.ResponseWriter, r *http.Request) {
				m, _ := uploadedMIME(t, r)
				built = messageParts(t, m)
				respond(w, map[string]any{"id": "f1", "message": map[string]any{"id": "created", "threadId": "thread"}})
			})
			if _, err := sendClient(t, h).Mailbox().DraftForward(context.Background(), "o1", tc.forward); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(built, tc.want) {
				t.Fatalf("parts %q\nwant  %q", built, tc.want)
			}
			if !reflect.DeepEqual(*requests, tc.requests) {
				t.Fatalf("requests=%v", *requests)
			}
		})
	}
}

// Gmail takes a message of at most 35 MB (36700160 bytes) through the upload endpoints of drafts.create and
// messages.send. A forward whose carried parts take more in base64 fails before anything is written: two attachments
// of 14,000,000 bytes take 28,000,000 bytes raw but more than 37,000,000 encoded, as does one with more
// attachments and carried inline images than the forward carries, or an attached message whose data Gmail does not
// give. An attachment file name or an inline image Content-ID with a line break never reaches a MIME header.
func TestForwardRefusesOversizedAttachmentsAndHeaderBreaks(t *testing.T) {
	many := []any{part("text/plain", "One")}
	for range 101 {
		many = append(many, attachmentPart("application/pdf", "a.pdf", "att1", 1))
	}
	withInline := append([]any{}, many[:101]...)
	image := part("image/png", "PNG")
	image["headers"] = headers("Content-ID", "<logo>")
	withInline = append(withInline, image)
	unread := map[string]any{"mimeType": "message/rfc822", "filename": "old.eml", "body": map[string]any{"size": 0}, "parts": []any{part("text/plain", "hi")}}
	for _, tc := range []struct {
		name        string
		parts       []any
		html        bool
		attachments map[string][]byte
		code        string
		requests    []string
	}{
		{"too large encoded together", []any{part("text/plain", "One"), attachmentPart("application/pdf", "a.pdf", "half", 1), attachmentPart("application/pdf", "b.pdf", "half", 1)}, false, map[string][]byte{"half": make([]byte, 14000000)}, "Gmail error SizeError",
			[]string{"GET /messages/o1", "GET /messages/o1/attachments/half", "GET /messages/o1/attachments/half"}},
		{"too many", many, false, nil, "Gmail error SizeError", []string{"GET /messages/o1"}},
		{"inline images count in HTML", withInline, true, nil, "Gmail error SizeError", []string{"GET /messages/o1"}},
		{"line break in a Content-ID", []any{part("text/html", "<p>One</p>"), map[string]any{"mimeType": "image/png", "headers": headers("Content-ID", "<logo>\r\nBcc: evil@example.test"), "body": map[string]any{"attachmentId": "img1", "size": 3}}}, true,
			map[string][]byte{"img1": []byte("PNG")}, "Gmail error HeaderError", []string{"GET /messages/o1"}},
		{"attached message without data", []any{part("text/plain", "One"), unread}, false, nil, "Gmail error ValueError", []string{"GET /messages/o1"}},
		{"line break in a file name", []any{part("text/plain", "One"), attachmentPart("application/pdf", "a.pdf\r\nBcc: evil@example.test", "att1", 1)}, false, map[string][]byte{"att1": []byte("x")}, "Gmail error HeaderError",
			[]string{"GET /messages/o1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"mimeType": "multipart/mixed", "headers": headers("From", "ana@example.test", "Subject", "Plan"), "parts": tc.parts}
			requests, h := forwardOriginal(t, payload, tc.attachments, func(http.ResponseWriter, *http.Request) { t.Error("write") })
			c := sendClient(t, h)
			_, err := c.Mailbox().DraftForward(context.Background(), "o1", model.Forward{To: []string{"a@example.test"}, HTML: tc.html})
			if err == nil || err.Error() != tc.code || !reflect.DeepEqual(*requests, tc.requests) {
				t.Fatalf("err=%v requests=%v", err, *requests)
			}
		})
	}
}

// An attachments.get answer without data, with null data, or with empty data for an attachment that is not empty
// fails before anything is written. An empty attachment needs no data.
func TestForwardRefusesAnAttachmentAnswerWithoutData(t *testing.T) {
	forward := func(size int, answer map[string]any, writes func(http.ResponseWriter, *http.Request)) (*[]string, error) {
		payload := map[string]any{"mimeType": "multipart/mixed", "headers": headers("From", "ana@example.test", "Subject", "Plan"), "parts": []any{part("text/plain", "One"), attachmentPart("application/pdf", "a.pdf", "att1", size)}}
		requests, h := forwardOriginal(t, payload, nil, writes)
		c := sendClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/messages/o1/attachments/att1" {
				respond(w, answer)
				return
			}
			h(w, r)
		})
		_, err := c.Mailbox().DraftForward(context.Background(), "o1", model.Forward{To: []string{"a@example.test"}})
		return requests, err
	}
	for _, answer := range []map[string]any{{"attachmentId": "att1", "size": 4}, {"attachmentId": "att1", "size": 4, "data": nil}, {"attachmentId": "att1", "size": 4, "data": ""}} {
		requests, err := forward(4, answer, func(http.ResponseWriter, *http.Request) { t.Error("write") })
		if err == nil || err.Error() != "Gmail error ValueError" || !reflect.DeepEqual(*requests, []string{"GET /messages/o1"}) {
			t.Fatalf("answer %v: err=%v requests=%v", answer, err, *requests)
		}
	}
	requests, err := forward(0, map[string]any{"attachmentId": "att1", "size": 0}, func(w http.ResponseWriter, _ *http.Request) {
		respond(w, map[string]any{"id": "f1", "message": map[string]any{"id": "created", "threadId": "thread"}})
	})
	if err != nil || !reflect.DeepEqual(*requests, []string{"GET /messages/o1", "POST /upload/drafts"}) {
		t.Fatalf("empty attachment: err=%v requests=%v", err, *requests)
	}
}
