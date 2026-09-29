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
	v, _, err := m.original(ctx, id, "raw")
	if err != nil {
		return "", err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return "", failure("Gmail", "DraftError")
	}
	if v.Raw == "" {
		return "", failure("Gmail", "ValueError")
	}
	original, err := decode64(v.Raw)
	if err != nil {
		return "", err
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(original))
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	subject := strings.Join(strings.Fields(decoded(parsed.Header.Get("Subject"))), " ")
	lower := strings.ToLower(subject)
	if !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
		subject = "Fwd: " + subject
	}
	ids := threadIDs(parsed.Header.Get("Message-ID"), parsed.Header.Get("References"))
	head, err := mimeHeaders([]header{{"To", strings.Join(to, ", ")}, {"Subject", subject}}, ids)
	if err != nil {
		return "", err
	}
	var mixed bytes.Buffer
	writer := multipart.NewWriter(&mixed)
	p, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"7bit"}})
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	_, _ = p.Write([]byte("\r\n"))
	p, err = writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"message/rfc822"}, "Content-Disposition": {`attachment; filename="forwarded.eml"`}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	// Write the original untouched, rather than parsing/re-serializing its body or attachments.
	_, _ = p.Write(original)
	_ = writer.Close()
	media := append(head, []byte("Content-Type: multipart/mixed; boundary="+writer.Boundary()+"\r\n\r\n")...)
	media = append(media, mixed.Bytes()...)
	var upload bytes.Buffer
	related := multipart.NewWriter(&upload)
	p, err = related.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	_ = json.NewEncoder(p).Encode(map[string]any{"message": map[string]string{"threadId": v.ThreadID}})
	p, err = related.CreatePart(textproto.MIMEHeader{"Content-Type": {"message/rfc822"}})
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	_, _ = p.Write(media)
	_ = related.Close()
	data, err := m.c.request(ctx, "Gmail", "POST", m.c.options.UploadURL, url.Values{"uploadType": {"multipart"}}, upload.Bytes(), "multipart/related; boundary="+related.Boundary())
	if err != nil {
		return "", err
	}
	return draftID(data)
}
