package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
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

// textMessage is a one-part message: a text/plain body, or with html a text/html body of the HTML source as given.
func textMessage(headers []header, body string, html bool, ids []string) ([]byte, error) {
	head, err := mimeHeaders(headers, ids)
	if err != nil {
		return nil, err
	}
	kind := "text/plain"
	if html {
		kind = "text/html"
	}
	var b bytes.Buffer
	b.Write(head)
	b.WriteString("Content-Type: " + kind + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
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
	id, _, err := draftMessage(v)
	return id, err
}

// draftMessage gives the message ID and thread ID of a Draft resource, which drafts.create and drafts.update
// return (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.drafts#Draft).
func draftMessage(v map[string]json.RawMessage) (id, thread string, err error) {
	obj, err := object(v["message"], "Gmail")
	id = stringField(obj, "id")
	if err != nil || strings.TrimSpace(id) == "" {
		return "", "", failure("Gmail", "InvalidMetadata")
	}
	return id, stringField(obj, "threadId"), nil
}

// rawMessage is the Gmail Message resource of raw MIME, in the thread when thread is not nil.
func rawMessage(raw []byte, thread *string) map[string]any {
	message := map[string]any{"raw": base64.URLEncoding.EncodeToString(raw)}
	if thread != nil {
		message["threadId"] = *thread
	}
	return message
}

// createDraft gives the message ID and thread ID of the new draft.
func (m *mailbox) createDraft(ctx context.Context, raw []byte, thread *string) (string, string, error) {
	v, err := m.c.write(ctx, "POST", m.c.options.GmailURL+"/drafts", map[string]any{"message": rawMessage(raw, thread)})
	if err != nil {
		return "", "", err
	}
	return draftMessage(v)
}

// sendRaw sends raw MIME at once. The request helper never repeats a POST, so a failed send is never sent again.
func (m *mailbox) sendRaw(ctx context.Context, raw []byte, thread *string) error {
	_, err := m.c.write(ctx, "POST", m.c.options.GmailURL+"/messages/send", rawMessage(raw, thread))
	return err
}

// newMessage builds the MIME of a new message. Gmail sends to the To, Cc, and Bcc headers
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send).
func newMessage(draft model.NewDraft) ([]byte, error) {
	headers := []header{{"To", strings.Join(draft.To, ", ")}, {"Cc", strings.Join(draft.CC, ", ")}, {"Bcc", strings.Join(draft.BCC, ", ")}, {"Subject", draft.Subject}}
	return textMessage(headers, draft.Body, draft.HTML, nil)
}

// DraftNew gives the draft's message and thread IDs from Gmail's answer, and the recipients and subject that it built.
func (m *mailbox) DraftNew(ctx context.Context, draft model.NewDraft) (model.DraftResult, error) {
	raw, err := newMessage(draft)
	if err != nil {
		return model.DraftResult{}, err
	}
	id, thread, err := m.createDraft(ctx, raw, nil)
	if err != nil {
		return model.DraftResult{}, err
	}
	return model.DraftResult{ID: id, ThreadID: thread, To: draft.To, CC: draft.CC, BCC: draft.BCC, Subject: draft.Subject}, nil
}
func (m *mailbox) SendNew(ctx context.Context, draft model.NewDraft) error {
	raw, err := newMessage(draft)
	if err != nil {
		return err
	}
	return m.sendRaw(ctx, raw, nil)
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

// DraftReply gives the draft's message and thread IDs from Gmail's answer, and the recipients and subject that it built.
func (m *mailbox) DraftReply(ctx context.Context, id string, reply model.Reply) (model.DraftResult, error) {
	b, err := m.reply(ctx, id, reply)
	if err != nil {
		return model.DraftResult{}, err
	}
	draft, thread, err := m.createDraft(ctx, b.raw, &b.thread)
	if err != nil {
		return model.DraftResult{}, err
	}
	return model.DraftResult{ID: draft, ThreadID: thread, To: b.to.To, CC: b.to.CC, BCC: b.to.BCC, Subject: b.subject}, nil
}
func (m *mailbox) SendReply(ctx context.Context, id string, reply model.Reply) error {
	b, err := m.reply(ctx, id, reply)
	if err != nil {
		return err
	}
	return m.sendRaw(ctx, b.raw, &b.thread)
}

// ReplyRecipients reads the headers of message id, and with all also the send-as addresses, and gives the
// recipients that its reply picks.
func (m *mailbox) ReplyRecipients(ctx context.Context, id string, all bool) (model.Recipients, error) {
	v, _, err := m.original(ctx, id, "metadata")
	if err != nil {
		return model.Recipients{}, err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return model.Recipients{}, failure("Gmail", "DraftError")
	}
	return m.replyRecipients(ctx, v, all)
}

// replyRecipients applies model.ReplyRecipients to the original v. A message with the SENT label is one that
// the account sent. The own addresses are the account's address and, for reply all, its send-as addresses
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.settings.sendAs/list, allowed by
// gmail.modify).
func (m *mailbox) replyRecipients(ctx context.Context, v wireMessage, all bool) (model.Recipients, error) {
	src := model.ReplySource{Sent: contains(v.LabelIDs, "SENT")}
	type used struct {
		name string
		list *[]string
	}
	// The rule reads To and Cc only for reply all or a sent message, so only then must they parse.
	headers := []used{{"from", &src.From}, {"reply-to", &src.ReplyTo}}
	if all || src.Sent {
		headers = append(headers, used{"to", &src.To}, used{"cc", &src.CC})
	}
	for _, h := range headers {
		list, err := headerList(v.Payload.Headers, h.name)
		if err != nil {
			return model.Recipients{}, err
		}
		*h.list = list
	}
	own := []string{m.c.account.Email}
	if all {
		aliases, err := m.sendAs(ctx)
		if err != nil {
			return model.Recipients{}, err
		}
		own = append(own, aliases...)
	}
	return model.ReplyRecipients(src, all, own), nil
}

// headerList gives the bare addresses of an address-list header. A header that does not parse fails, so that a
// reply never silently drops its recipients.
func headerList(headers []header, name string) ([]string, error) {
	value := headerValue(headers, name)
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	parser := mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	list, err := parser.ParseList(value)
	if err != nil {
		return nil, failure("Gmail", "InvalidMetadata")
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out, nil
}

// addressList writes bare addresses as one header value. Each address is written in its RFC 5322 form, so a quoted
// local part such as "a, b"@example.test stays one address.
func addressList(list []string) string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, (&mail.Address{Address: a}).String())
	}
	return strings.Join(out, ", ")
}

// maxSendAs bounds the send-as list that Gmail returns.
const maxSendAs = 1000

// sendAs gives the send-as addresses of the account, from one page: users.settings.sendAs.list has no paging.
func (m *mailbox) sendAs(ctx context.Context) ([]string, error) {
	data, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/settings/sendAs", nil)
	if err != nil {
		return nil, err
	}
	list, err := items(data, "sendAs", "Gmail")
	if err != nil || len(list) > maxSendAs {
		return nil, failure("Gmail", "InvalidMetadata")
	}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		var alias struct {
			SendAsEmail string `json:"sendAsEmail"`
		}
		if json.Unmarshal(raw, &alias) != nil {
			return nil, failure("Gmail", "InvalidMetadata")
		}
		out = append(out, alias.SendAsEmail)
	}
	return out, nil
}

// builtReply is a reply's raw MIME, its thread, and the recipients and subject that it carries.
type builtReply struct {
	raw             []byte
	thread, subject string
	to              model.Recipients
}

// quoteStyle is the left border that Gmail on the web gives a quoted original.
const quoteStyle = `margin:0 0 0 .8ex;border-left:1px #ccc solid;padding-left:1ex`

// reply builds the reply to message id: the agent's text above the quoted original text, or with HTML the agent's
// HTML above the original HTML part (or the escaped original text) in a blockquote. It goes to the
// model.ReplyRecipients of the original plus the agent's CC and BCC. A draft source is refused.
func (m *mailbox) reply(ctx context.Context, id string, reply model.Reply) (builtReply, error) {
	v, data, err := m.original(ctx, id, "full")
	if err != nil {
		return builtReply{}, err
	}
	if contains(v.LabelIDs, "DRAFT") {
		return builtReply{}, failure("Gmail", "DraftError")
	}
	original, err := parseMessage(data, nil, false)
	if err != nil {
		return builtReply{}, err
	}
	picked, err := m.replyRecipients(ctx, v, reply.All)
	if err != nil {
		return builtReply{}, err
	}
	to := picked.With(reply.CC, reply.BCC)
	headers := v.Payload.Headers
	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	ids := threadIDs(headerValue(headers, "message-id"), headerValue(headers, "references"))
	text, kind, err := bodyText(&v.Payload, !reply.HTML)
	if err != nil {
		return builtReply{}, err
	}
	attribution := "On " + original.ReceivedAt.Format("2006-01-02 15:04") + " UTC, " + original.Sender.Address + " wrote:"
	var body string
	if reply.HTML {
		quoted := text
		if kind != model.HTML {
			quoted = model.HTMLBody(text)
		}
		body = reply.Body + "<br><br><div class=\"gmail_quote\">" + model.HTMLBody(attribution) + "<br><blockquote class=\"gmail_quote\" style=\"" + quoteStyle + "\">" + quoted + "</blockquote></div>"
	} else {
		if kind == model.HTML {
			text, err = htmlText(text)
			if err != nil {
				return builtReply{}, err
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
		body = reply.Body + "\n\n" + attribution + "\n" + strings.Join(lines, "\n") + "\n"
	}
	raw, err := textMessage([]header{{"To", addressList(to.To)}, {"Cc", addressList(to.CC)}, {"Bcc", addressList(to.BCC)}, {"Subject", subject}}, body, reply.HTML, ids)
	if err != nil {
		return builtReply{}, err
	}
	return builtReply{raw: raw, thread: v.ThreadID, subject: subject, to: to}, nil
}
