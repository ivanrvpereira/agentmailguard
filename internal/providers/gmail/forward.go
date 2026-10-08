package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
)

func (m *mailbox) DraftForward(ctx context.Context, id string, to []string) (string, error) {
	media, thread, err := m.forward(ctx, id, to)
	if err != nil {
		return "", err
	}
	// The drafts upload takes a Draft resource as metadata.
	data, err := m.upload(ctx, m.c.options.UploadURL, map[string]any{"message": map[string]string{"threadId": thread}}, media)
	if err != nil {
		return "", err
	}
	return draftID(data)
}

// SendForward sends the forward that DraftForward would save. The send upload takes a Message resource
// as metadata. The request helper never repeats a POST, so a failed send is never sent again.
func (m *mailbox) SendForward(ctx context.Context, id string, to []string) error {
	media, thread, err := m.forward(ctx, id, to)
	if err != nil {
		return err
	}
	_, err = m.upload(ctx, m.c.options.SendUploadURL, map[string]string{"threadId": thread}, media)
	return err
}

// forward builds the MIME of a forward of message id to the recipients, with the original attached
// unchanged, and gives its thread ID. A draft source is refused.
func (m *mailbox) forward(ctx context.Context, id string, to []string) ([]byte, string, error) {
	v, _, err := m.original(ctx, id, "raw")
	if err != nil {
		return nil, "", err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return nil, "", failure("Gmail", "DraftError")
	}
	if v.Raw == "" {
		return nil, "", failure("Gmail", "ValueError")
	}
	original, err := decode64(v.Raw)
	if err != nil {
		return nil, "", err
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(original))
	if err != nil {
		return nil, "", failure("Gmail", "ValueError")
	}
	subject := strings.Join(strings.Fields(decoded(parsed.Header.Get("Subject"))), " ")
	lower := strings.ToLower(subject)
	if !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
		subject = "Fwd: " + subject
	}
	ids := threadIDs(parsed.Header.Get("Message-ID"), parsed.Header.Get("References"))
	head, err := mimeHeaders([]header{{"To", strings.Join(to, ", ")}, {"Subject", subject}}, ids)
	if err != nil {
		return nil, "", err
	}
	var mixed bytes.Buffer
	writer := multipart.NewWriter(&mixed)
	p, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"7bit"}})
	if err != nil {
		return nil, "", failure("Gmail", "ValueError")
	}
	_, _ = p.Write([]byte("\r\n"))
	p, err = writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"message/rfc822"}, "Content-Disposition": {`attachment; filename="forwarded.eml"`}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return nil, "", failure("Gmail", "ValueError")
	}
	// Write the original untouched, rather than parsing/re-serializing its body or attachments.
	_, _ = p.Write(original)
	_ = writer.Close()
	media := append(head, []byte("Content-Type: multipart/mixed; boundary="+writer.Boundary()+"\r\n\r\n")...)
	media = append(media, mixed.Bytes()...)
	return media, v.ThreadID, nil
}

// upload posts media with its JSON metadata as one multipart/related upload.
func (m *mailbox) upload(ctx context.Context, endpoint string, metadata any, media []byte) (map[string]json.RawMessage, error) {
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
	return m.c.request(ctx, "Gmail", "POST", endpoint, url.Values{"uploadType": {"multipart"}}, upload.Bytes(), "multipart/related; boundary="+related.Boundary())
}
