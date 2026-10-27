package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// maxForwardBytes bounds the encoded size (encodedSize) of the parts that a forward carries: Gmail takes a raw message
// of at most 36700160 bytes (35 MB) through the upload endpoints of drafts.create and messages.send
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.drafts/create,
// https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send), so a forward whose
// carried parts take more there can never be saved or sent.
const maxForwardBytes = 36700160

// maxForwardAttachments bounds the attachments and inline images that a forward carries, and so its attachment
// reads. Gmail documents no count limit; this bound is the server's.
const maxForwardAttachments = 100

// DraftForward gives the draft's message and thread IDs from Gmail's answer, and the recipients and subject that it
// built. The drafts upload takes a Draft resource as metadata.
func (m *mailbox) DraftForward(ctx context.Context, id string, f model.Forward) (model.DraftResult, error) {
	b, err := m.forward(ctx, id, f)
	if err != nil {
		return model.DraftResult{}, err
	}
	data, err := m.upload(ctx, "POST", m.c.options.UploadURL, map[string]any{"message": map[string]string{"threadId": b.thread}}, b.raw)
	if err != nil {
		return model.DraftResult{}, err
	}
	draft, thread, err := draftMessage(data)
	if err != nil {
		return model.DraftResult{}, err
	}
	return model.DraftResult{ID: draft, ThreadID: thread, To: f.To, CC: f.CC, BCC: f.BCC, Subject: b.subject}, nil
}

// SendForward sends the forward that DraftForward would save. The send upload takes a Message resource
// as metadata. The request helper never repeats a POST, so a failed send is never sent again.
func (m *mailbox) SendForward(ctx context.Context, id string, f model.Forward) error {
	b, err := m.forward(ctx, id, f)
	if err != nil {
		return err
	}
	_, err = m.upload(ctx, "POST", m.c.options.SendUploadURL, map[string]string{"threadId": b.thread}, b.raw)
	return err
}

// builtForward is a forward's raw MIME, its thread, and its subject.
type builtForward struct {
	raw             []byte
	thread, subject string
}

// forwardDivider opens the header block of a forwarded message, as Gmail on the web writes it.
const forwardDivider = "---------- Forwarded message ---------"

// forward builds the inline forward of message id, as Gmail on the web and Google's CLI forward
// (https://github.com/googleworkspace/cli/blob/a3768d0e82ad83cca2da97724e46bea4ff0e6dbd/crates/google-workspace-cli/src/helpers/gmail/forward.rs):
// the note, then a block with the original's From, Date, Subject, To, and Cc, then the original text; with HTML, the
// note's HTML above the block and the original HTML part, or the escaped original text. The original's attachments
// and, with HTML, its inline images are carried (forwardParts); its subject gets "Fwd: " unless it starts with "fwd:"
// or "fw:" in any case. The forward keeps the thread and the reply headers of the original. A draft source is refused.
func (m *mailbox) forward(ctx context.Context, id string, f model.Forward) (builtForward, error) {
	v, _, err := m.original(ctx, id, "full")
	if err != nil {
		return builtForward{}, err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return builtForward{}, failure("Gmail", "DraftError")
	}
	regular, inline, err := forwardParts(&v.Payload, f.HTML)
	if err != nil {
		return builtForward{}, err
	}
	original := v.Payload.Headers
	value := func(name string) string {
		return strings.Join(strings.Fields(decoded(headerValue(original, name))), " ")
	}
	subject := value("subject")
	if lower := strings.ToLower(subject); !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
		subject = "Fwd: " + subject
	}
	lines := []string{"From: " + value("from")}
	if date := value("date"); date != "" {
		lines = append(lines, "Date: "+date)
	}
	lines = append(lines, "Subject: "+value("subject"), "To: "+value("to"))
	if cc := value("cc"); cc != "" {
		lines = append(lines, "Cc: "+cc)
	}
	text, kind, err := bodyText(&v.Payload, !f.HTML)
	if err != nil {
		return builtForward{}, err
	}
	var body string
	if f.HTML {
		if kind != model.HTML {
			text = model.HTMLBody(text)
		}
		for i := range lines {
			lines[i] = html.EscapeString(lines[i])
		}
		body = f.Body + `<br><br><div class="gmail_quote gmail_quote_container"><div dir="ltr" class="gmail_attr">` + forwardDivider + "<br>" + strings.Join(lines, "<br>") + "<br></div><br><br>" + text + "</div>"
	} else {
		if kind == model.HTML {
			if text, err = htmlText(text); err != nil {
				return builtForward{}, err
			}
		}
		body = f.Body + "\n\n" + forwardDivider + "\n" + strings.Join(lines, "\n") + "\n\n" + text
	}
	headers := []header{{"To", addressList(f.To)}, {"Cc", addressList(f.CC)}, {"Bcc", addressList(f.BCC)}, {"Subject", subject}}
	ids := threadIDs(headerValue(original, "message-id"), headerValue(original, "references"))
	data, err := m.attachmentData(ctx, id, append(append([]*mimePart{}, regular...), inline...))
	if err != nil {
		return builtForward{}, err
	}
	raw, err := forwardMessage(headers, ids, body, f.HTML, regular, inline, data)
	return builtForward{raw, v.ThreadID, subject}, err
}

// forwardParts gives the parts of a message that its forward carries. An inline image is an image/* part with a
// Content-ID whose Content-Disposition is not attachment: the original's HTML shows it in place through a cid: link.
// With html, inline images are carried as inline; without, they are dropped, as Google's CLI does. Every other part
// that starts an attachment (walk: a file name, Content-Disposition attachment, or message/rfc822), without the parts
// inside it, is a regular attachment; partHeader sends an attached message as application/octet-stream. A part that
// is none of these is not carried, such as an unnamed text/calendar part or a non-image part with only a Content-ID.
// Regular attachments and carried inline images count
// toward maxForwardAttachments together, and a file name, type, or Content-ID with a line break fails before it
// reaches a header.
func forwardParts(payload *mimePart, html bool) (regular, inline []*mimePart, err error) {
	regular, inline = []*mimePart{}, []*mimePart{}
	err = walk(payload, func(p *mimePart, attached bool) error {
		disposition, _, _ := mime.ParseMediaType(headerValue(p.Headers, "content-disposition"))
		kind, _, _ := mime.ParseMediaType(p.MimeType)
		image := strings.HasPrefix(kind, "image/") && headerValue(p.Headers, "content-id") != "" && disposition != "attachment"
		switch {
		case image && !html:
			return errSkipParts
		case !image && !attached:
			return nil
		}
		if strings.ContainsAny(p.Filename+p.MimeType+headerValue(p.Headers, "content-id"), "\r\n") {
			return failure("Gmail", "HeaderError")
		}
		if image {
			inline = append(inline, p)
		} else {
			regular = append(regular, p)
		}
		if len(regular)+len(inline) > maxForwardAttachments {
			return failure("Gmail", "SizeError")
		}
		return errSkipParts
	})
	return regular, inline, err
}

// attachmentData gives the data of each part of message id: the inline data, or else the data that
// messages.attachments.get gives for its attachment ID
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages.attachments/get). A part with
// neither fails, unless it is empty; an attached message (message/*) is never taken as empty. An encodedSize of more
// than maxForwardBytes in total fails, and stops the reads.
func (m *mailbox) attachmentData(ctx context.Context, id string, parts []*mimePart) ([][]byte, error) {
	out := make([][]byte, 0, len(parts))
	total := 0
	for _, p := range parts {
		encoded := p.Body.Data
		switch {
		case encoded != "":
		case p.Body.AttachmentID != "":
			v, err := m.c.get(ctx, "Gmail", m.path(id)+"/attachments/"+segment(p.Body.AttachmentID), nil)
			if err != nil {
				return nil, err
			}
			var body struct{ Data string }
			raw, _ := json.Marshal(v)
			if json.Unmarshal(raw, &body) != nil {
				return nil, failure("Gmail", "ValueError")
			}
			encoded = body.Data
		case p.Body.Size != nil && *p.Body.Size == 0 && !strings.HasPrefix(strings.ToLower(p.MimeType), "message/"):
		default:
			return nil, failure("Gmail", "ValueError")
		}
		data, err := decode64(encoded)
		if err != nil {
			return nil, err
		}
		if total += encodedSize(len(data)); total > maxForwardBytes {
			return nil, failure("Gmail", "SizeError")
		}
		out = append(out, data)
	}
	return out, nil
}

// encodedSize is the size of n bytes of data in a carried part, as multipartOf writes it: base64 in lines of at most
// 76 characters, each ending in CRLF.
func encodedSize(n int) int {
	b := (n + 2) / 3 * 4
	return b + 2*max(1, (b+75)/76)
}

// forwardMessage is the MIME of a forward: the text part of body; with inline images, a multipart/related of the
// HTML part and the images; with regular attachments, a multipart/mixed of that and the attachments. data holds the
// data of the regular attachments, then of the inline images.
func forwardMessage(headers []header, ids []string, body string, isHTML bool, regular, inline []*mimePart, data [][]byte) ([]byte, error) {
	head, err := mimeHeaders(headers, ids)
	if err != nil {
		return nil, err
	}
	kind, text, err := textBody(body, isHTML)
	if err != nil {
		return nil, err
	}
	content := textproto.MIMEHeader{"Content-Type": {kind}, "Content-Transfer-Encoding": {"quoted-printable"}}
	if len(inline) > 0 {
		// RFC 2387 names the type of the root part of a multipart/related.
		if content, text, err = multipartOf("related", map[string]string{"type": "text/html"}, content, text, inline, data[len(regular):], true); err != nil {
			return nil, err
		}
	}
	if len(regular) > 0 {
		if content, text, err = multipartOf("mixed", map[string]string{}, content, text, regular, data[:len(regular)], false); err != nil {
			return nil, err
		}
	}
	for _, k := range []string{"Content-Type", "Content-Transfer-Encoding"} {
		if v := content.Get(k); v != "" {
			head = append(head, k+": "+v+"\r\n"...)
		}
	}
	head = append(head, "\r\n"...)
	return append(head, text...), nil
}

// multipartOf is a multipart/sub with params: the first part of header and body, then each part of parts with its
// data (partHeader). It gives the header and body of the multipart.
func multipartOf(sub string, params map[string]string, header textproto.MIMEHeader, body []byte, parts []*mimePart, data [][]byte, inline bool) (textproto.MIMEHeader, []byte, error) {
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	part, err := w.CreatePart(header)
	if err != nil {
		return nil, nil, failure("Gmail", "ValueError")
	}
	_, _ = part.Write(body)
	for i, p := range parts {
		h, err := partHeader(p, inline)
		if err != nil {
			return nil, nil, err
		}
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, nil, failure("Gmail", "ValueError")
		}
		_, _ = part.Write(append(base64Lines(data[i]), "\r\n"...))
	}
	if w.Close() != nil {
		return nil, nil, failure("Gmail", "ValueError")
	}
	params["boundary"] = w.Boundary()
	kind := mime.FormatMediaType("multipart/"+sub, params)
	if kind == "" {
		return nil, nil, failure("Gmail", "ValueError")
	}
	return textproto.MIMEHeader{"Content-Type": {kind}}, out.Bytes(), nil
}

// partHeader is the header of a carried part in base64, with the original's type and file name: an attachment, or
// with inline an inline part with the original's Content-ID. RFC 2046 section 5.2.1 allows no base64 for
// message/rfc822, so an attached message (message/*) goes as application/octet-stream, named forwarded.eml when it
// has no file name.
func partHeader(p *mimePart, inline bool) (textproto.MIMEHeader, error) {
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	kind, _, err := mime.ParseMediaType(p.MimeType)
	name := p.Filename
	switch {
	case err == nil && strings.HasPrefix(kind, "message/"):
		kind = "application/octet-stream"
		if name == "" {
			name = "forwarded.eml"
		}
	case err != nil || !strings.Contains(kind, "/") || strings.HasPrefix(kind, "multipart/"):
		kind = "application/octet-stream"
	}
	if name != "" {
		kind = mime.FormatMediaType(kind, map[string]string{"name": name})
		disposition = mime.FormatMediaType(disposition, map[string]string{"filename": name})
	}
	if kind == "" || disposition == "" {
		return nil, failure("Gmail", "ValueError")
	}
	h := textproto.MIMEHeader{"Content-Type": {kind}, "Content-Disposition": {disposition}, "Content-Transfer-Encoding": {"base64"}}
	if inline {
		h["Content-ID"] = []string{strings.TrimSpace(headerValue(p.Headers, "content-id"))}
	}
	return h, nil
}

// upload sends media with its JSON metadata as one multipart/related upload, with method POST or PUT.
func (m *mailbox) upload(ctx context.Context, method, endpoint string, metadata any, media []byte) (map[string]json.RawMessage, error) {
	var upload bytes.Buffer
	related := multipart.NewWriter(&upload)
	p, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	_ = json.NewEncoder(p).Encode(metadata)
	p, err = related.CreatePart(textproto.MIMEHeader{"Content-Type": {"message/rfc822"}})
	if err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	_, _ = p.Write(media)
	_ = related.Close()
	return m.c.request(ctx, "Gmail", method, endpoint, url.Values{"uploadType": {"multipart"}}, upload.Bytes(), "multipart/related; boundary="+related.Boundary())
}
