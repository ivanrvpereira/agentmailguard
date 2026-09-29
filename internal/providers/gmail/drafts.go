package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"mime/quotedprintable"
	"net/url"
	"regexp"
	"strings"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"golang.org/x/net/html"
)

var messageIDRE = regexp.MustCompile(`<[!-;=?-~]+>`)

func threadIDs(messageID, references string) []string {
	last := messageIDRE.FindString(messageID)
	if last == "" {
		return nil
	}
	return append(messageIDRE.FindAllString(references, -1), last)
}
func mimeHeaders(headers []header, ids []string) ([]byte, error) {
	var b bytes.Buffer
	for _, h := range headers {
		value := strings.Join(strings.Fields(h.Value), " ")
		if value == "" {
			continue
		}
		if strings.ContainsAny(decoded(value), "\r\n") {
			return nil, failure("Gmail", "HeaderError")
		}
		if h.Name == "Subject" {
			value = mime.QEncoding.Encode("utf-8", value)
		}
		b.WriteString(h.Name + ": " + value + "\r\n")
	}
	if len(ids) > 0 {
		b.WriteString("In-Reply-To: " + ids[len(ids)-1] + "\r\nReferences: " + strings.Join(ids, "\r\n ") + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	return b.Bytes(), nil
}
func plainMessage(headers []header, body string, ids []string) ([]byte, error) {
	head, err := mimeHeaders(headers, ids)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(head)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	w := quotedprintable.NewWriter(&b)
	if _, err = w.Write([]byte(body)); err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	if err = w.Close(); err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	return b.Bytes(), nil
}
func draftID(v map[string]json.RawMessage) (string, error) {
	obj, err := object(v["message"], "Gmail")
	id := stringField(obj, "id")
	if err != nil || strings.TrimSpace(id) == "" {
		return "", failure("Gmail", "InvalidMetadata")
	}
	return id, nil
}
func (m *mailbox) createDraft(ctx context.Context, raw []byte, thread *string) (string, error) {
	message := map[string]any{"raw": base64.URLEncoding.EncodeToString(raw)}
	if thread != nil {
		message["threadId"] = *thread
	}
	v, err := m.c.write(ctx, "POST", m.c.options.GmailURL+"/drafts", map[string]any{"message": message})
	if err != nil {
		return "", err
	}
	return draftID(v)
}
func (m *mailbox) DraftNew(ctx context.Context, draft model.NewDraft) (string, error) {
	raw, err := plainMessage([]header{{"To", strings.Join(draft.To, ", ")}, {"Subject", draft.Subject}}, draft.Body, nil)
	if err != nil {
		return "", err
	}
	return m.createDraft(ctx, raw, nil)
}
func htmlText(text string) (string, error) {
	root, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return "", failure("Gmail", "ValueError")
	}
	parts := []string{}
	type entry struct {
		node  *html.Node
		depth int
	}
	todo := []entry{{root, 0}}
	count := 0
	for len(todo) > 0 {
		e := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		count++
		if e.depth > 200 || count > 100000 {
			return "", failure("Gmail", "ValueError")
		}
		n := e.node
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
			continue
		}
		if n.Type == html.TextNode {
			parts = append(parts, n.Data)
		}
		for child := n.LastChild; child != nil; child = child.PrevSibling {
			todo = append(todo, entry{child, e.depth + 1})
		}
	}
	return strings.Join(parts, "\n"), nil
}
func (m *mailbox) original(ctx context.Context, id, format string) (wireMessage, map[string]json.RawMessage, error) {
	data, err := m.c.get(ctx, "Gmail", m.path(id), url.Values{"format": {format}})
	if err != nil {
		return wireMessage{}, nil, err
	}
	raw, _ := json.Marshal(data)
	var v wireMessage
	if json.Unmarshal(raw, &v) != nil || v.ID != id {
		return wireMessage{}, nil, failure("Gmail", "InvalidMetadata")
	}
	return v, data, nil
}
func (m *mailbox) DraftReply(ctx context.Context, id, comment string) (string, error) {
	v, data, err := m.original(ctx, id, "full")
	if err != nil {
		return "", err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return "", failure("Gmail", "DraftError")
	}
	original, err := parseMessage(data, nil, false)
	if err != nil {
		return "", err
	}
	headers := v.Payload.Headers
	to := headerValue(headers, "reply-to")
	if to == "" {
		to = headerValue(headers, "from")
	}
	if contains(v.LabelIDs, "SENT") {
		to = headerValue(headers, "to")
	}
	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	ids := threadIDs(headerValue(headers, "message-id"), headerValue(headers, "references"))
	text, kind, err := bodyText(&v.Payload, true)
	if err != nil {
		return "", err
	}
	if kind == model.HTML {
		text, err = htmlText(text)
		if err != nil {
			return "", err
		}
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := []string{}
	if text != "" {
		lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	for i := range lines {
		lines[i] = "> " + lines[i]
	}
	body := comment + "\n\nOn " + original.ReceivedAt.Format("2006-01-02 15:04") + " UTC, " + original.Sender.Address + " wrote:\n" + strings.Join(lines, "\n") + "\n"
	raw, err := plainMessage([]header{{"To", to}, {"Subject", subject}}, body, ids)
	if err != nil {
		return "", err
	}
	return m.createDraft(ctx, raw, &v.ThreadID)
}
