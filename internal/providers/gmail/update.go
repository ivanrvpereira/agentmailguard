package gmail

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"mime"
	"net/textproto"
	"net/url"
	"slices"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (m *mailbox) resolveDraft(ctx context.Context, id string) (string, error) {
	q := url.Values{"maxResults": {"500"}}
	seen := map[string]bool{}
	for {
		v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/drafts", q)
		if err != nil {
			return "", err
		}
		a, err := items(v, "drafts", "Gmail")
		if err != nil {
			return "", err
		}
		for _, raw := range a {
			draft, err := object(raw, "Gmail")
			if err != nil {
				return "", err
			}
			message, err := object(draft["message"], "Gmail")
			if err != nil {
				return "", err
			}
			draftID := stringField(draft, "id")
			messageID := stringField(message, "id")
			if draftID == "" || messageID == "" {
				return "", failure("Gmail", "InvalidMetadata")
			}
			if messageID == id {
				return draftID, nil
			}
		}
		token, err := pageToken(v, seen, "Gmail")
		if err != nil {
			return "", err
		}
		if token == "" {
			return "", failure("Gmail", "DraftError")
		}
		q.Set("pageToken", token)
	}
}

// ReadDraft reads message id in full. The DRAFT label marks a draft; a message without it gives only IsDraft false.
// To, Cc, and Bcc must parse, and the body is the HTML part, or else the text part (bodyText).
func (m *mailbox) ReadDraft(ctx context.Context, id string) (model.DraftContent, error) {
	v, _, err := m.original(ctx, id, "full")
	if err != nil || !contains(v.LabelIDs, "DRAFT") {
		return model.DraftContent{}, err
	}
	out := model.DraftContent{IsDraft: true, Subject: decoded(headerValue(v.Payload.Headers, "subject"))}
	for _, h := range []struct {
		name string
		list *[]string
	}{{"to", &out.To}, {"cc", &out.CC}, {"bcc", &out.BCC}} {
		if *h.list, err = headerList(v.Payload.Headers, h.name); err != nil {
			return model.DraftContent{}, err
		}
	}
	body, kind, err := bodyText(&v.Payload, false)
	if err != nil {
		return model.DraftContent{}, err
	}
	out.Body, out.HTML = body, kind == model.HTML
	return out, nil
}

// DraftUpdate replaces the message of the draft that holds message id with drafts.update through its upload endpoint
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.drafts/update), as a forward is made.
// Gmail keeps the draft ID and gives the draft a new message ID
// (https://developers.google.com/workspace/gmail/api/guides/drafts). The new message is the draft's raw MIME: its
// top-level header fields stay byte for byte, In-Reply-To and References included, except Date and Message-ID,
// which name the replaced message, and the fields that are written new: the To, Cc, Bcc, and Subject that change
// gives, and the Content-Type of a body that replaces the whole message. A body in change replaces the body that
// textLeaves picks (bodyEdits); every other part stays byte for byte at its place. The update names the draft's
// thread, because Gmail keeps a message in a thread only when the request names it
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages).
func (m *mailbox) DraftUpdate(ctx context.Context, id string, change model.DraftChange) (string, string, error) {
	draft, err := m.resolveDraft(ctx, id)
	if err != nil {
		return "", "", err
	}
	// The raw form is one read, bounded by responseLimit like every Gmail response.
	v, _, err := m.original(ctx, id, "raw")
	if err != nil {
		return "", "", err
	}
	if !contains(v.LabelIDs, "DRAFT") {
		return "", "", failure("Gmail", "DraftError")
	}
	raw, err := decode64(v.Raw)
	if err != nil {
		return "", "", err
	}
	changed := []header{}
	for _, list := range []struct {
		name  string
		value *[]string
	}{{"To", change.To}, {"Cc", change.CC}, {"Bcc", change.BCC}} {
		if list.value != nil {
			changed = append(changed, header{list.name, addressList(*list.value)})
		}
	}
	if change.Subject != nil {
		changed = append(changed, header{"Subject", *change.Subject})
	}
	var message []byte
	if change.Body == nil {
		_, content, err := splitEntity(raw)
		if err != nil {
			return "", "", err
		}
		if message, err = topHeaders(raw[:content], changed, false); err != nil {
			return "", "", err
		}
		message = append(append(message, "\r\n"...), raw[content:]...)
	} else if message, err = bodyMessage(raw, changed, *change.Body, change.HTML); err != nil {
		return "", "", err
	}
	metadata := map[string]string{}
	if v.ThreadID != "" {
		metadata["threadId"] = v.ThreadID
	}
	data, err := m.upload(ctx, "PUT", m.c.options.UploadURL+"/"+segment(draft), map[string]any{"id": draft, "message": metadata}, message)
	if err != nil {
		return "", "", err
	}
	return draftMessage(data)
}

// topHeaders gives the header fields of block, the top-level header block of a draft, for its update. Each field
// stays byte for byte, folding included, except Date, Message-ID, each field of changed, and with whole every
// Content- field. Then come the changed fields with a value (headerLines), and MIME-Version when block has none.
func topHeaders(block []byte, changed []header, whole bool) ([]byte, error) {
	var out []byte
	keep, version := false, false
	for rest := block; len(rest) > 0; {
		line, after, _ := bytes.Cut(rest, []byte("\n"))
		field := rest[:len(rest)-len(after)]
		rest = after
		if len(bytes.TrimSuffix(line, []byte("\r"))) == 0 {
			break
		}
		if line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			lower := strings.ToLower(strings.TrimSpace(string(name)))
			replaced := slices.ContainsFunc(changed, func(c header) bool { return strings.EqualFold(c.Name, lower) })
			keep = !replaced && lower != "date" && lower != "message-id" && !(whole && strings.HasPrefix(lower, "content-"))
			version = version || lower == "mime-version"
		}
		if keep {
			out = append(out, field...)
		}
	}
	lines, err := headerLines(changed)
	if err != nil {
		return nil, err
	}
	out = append(out, lines...)
	if !version {
		out = append(out, "MIME-Version: 1.0\r\n"...)
	}
	return out, nil
}

// bodyMessage is the raw draft with body in place of its old body (bodyEdits) and the changed header fields.
func bodyMessage(raw []byte, changed []header, body string, html bool) ([]byte, error) {
	top, err := parseRaw(raw)
	if err != nil {
		return nil, err
	}
	edits, whole, err := bodyEdits(top, body, html)
	if err != nil {
		return nil, err
	}
	message, err := topHeaders(raw[:top.content], changed, whole)
	if err != nil {
		return nil, err
	}
	if whole {
		kind, data, err := textBody(body, html)
		if err != nil {
			return nil, err
		}
		return append(append(message, "Content-Type: "+kind+"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"...), data...), nil
	}
	message = append(message, "\r\n"...)
	at := top.content
	for _, e := range edits {
		message = append(append(message, raw[at:e.start]...), e.text...)
		at = e.end
	}
	return append(message, raw[at:]...), nil
}

// rawEdit replaces the bytes of a raw message from start to end with text.
type rawEdit struct {
	start, end int
	text       []byte
}

// bodyEdits gives the edits that put body in place of the old body of the draft top: the leaves that textLeaves
// picks, the same body that ReadDraft and mail get show. Their smallest common entity is replaced when it holds only
// text and HTML leaves (a single text part, or a multipart/alternative of text and HTML); otherwise the shown leaf
// (HTML, else text) is replaced and the other picked leaf removed, so that the parts beside them, such as the inline
// images of a multipart/related, stay. whole reports that the body is the whole message. A draft without a body gets
// it as the first part of its top multipart/mixed; any other draft without one fails. The new part is base64, whose
// alphabet has no "-", so no line of it is a delimiter of the draft's boundaries.
func bodyEdits(top *rawPart, body string, html bool) ([]rawEdit, bool, error) {
	kind, text := bodyLines(body, html)
	part := append([]byte("Content-Type: "+kind+"\r\nContent-Transfer-Encoding: base64\r\n\r\n"), base64Lines([]byte(strings.ReplaceAll(text, "\n", "\r\n")))...)
	plain, rich, err := textLeaves(&top.part)
	if err != nil {
		return nil, false, err
	}
	picked := []*rawPart{}
	for _, p := range []*mimePart{rich, plain} {
		if p != nil {
			picked = append(picked, top.parts[p])
		}
	}
	if len(picked) == 0 {
		if top.part.MimeType != "multipart/mixed" {
			return nil, false, failure("Gmail", "ValueError")
		}
		return []rawEdit{{top.insert, top.insert, append(append([]byte("--"+top.boundary+"\r\n"), part...), "\r\n"...)}}, false, nil
	}
	target := picked[0]
	for len(picked) == 2 && !target.holds(picked[1]) {
		target = target.parent
	}
	if !target.onlyText() {
		target = picked[0]
	}
	if target == top {
		return nil, true, nil
	}
	edits := []rawEdit{{target.start, target.end, part}}
	if len(picked) == 2 && !target.holds(picked[1]) {
		other := picked[1]
		if len(other.parent.children) < 2 {
			return nil, false, failure("Gmail", "ValueError")
		}
		edits = append(edits, rawEdit{other.cut, other.next, nil})
	}
	slices.SortFunc(edits, func(a, b rawEdit) int { return a.start - b.start })
	return edits, false, nil
}

// rawPart is one entity of a raw message: part, as walk and textLeaves read it, and its place in the raw bytes. The
// entity, header included, runs from start to end, and its content from content. Inside a multipart, its delimiter
// line starts at cut, and the next delimiter line at next. A multipart has its boundary, its children, and insert,
// the start of its first delimiter line. The top entity has parts, which maps each part of the parsed tree to its
// entity.
type rawPart struct {
	part                                   mimePart
	start, content, end, cut, next, insert int
	boundary                               string
	parent                                 *rawPart
	children                               []*rawPart
	parts                                  map[*mimePart]*rawPart
}

// holds reports whether e is r or inside r.
func (r *rawPart) holds(e *rawPart) bool {
	for ; e != nil; e = e.parent {
		if e == r {
			return true
		}
	}
	return false
}

// onlyText reports an entity whose leaves are all text/plain or text/html parts that are not attachments.
func (r *rawPart) onlyText() bool {
	only := true
	err := walk(&r.part, func(p *mimePart, attached bool) error {
		if kind := p.MimeType; len(p.Parts) == 0 && (attached || kind != "text/plain" && kind != "text/html") {
			only = false
		}
		return nil
	})
	return only && err == nil
}

// parseRaw parses raw MIME into its entities. Only multipart entities are opened: an attached message (message/*)
// is one leaf. A multipart without a boundary or a close delimiter, a header block without its end, and more than
// maxMIMEDepth levels or maxMIMEParts entities fail.
func parseRaw(raw []byte) (*rawPart, error) {
	count := 0
	top, err := parseEntity(raw, 0, len(raw), 0, &count)
	if err != nil {
		return nil, err
	}
	top.parts = map[*mimePart]*rawPart{}
	var index func(r *rawPart, p *mimePart)
	index = func(r *rawPart, p *mimePart) {
		top.parts[p] = r
		for i := range p.Parts {
			index(r.children[i], &p.Parts[i])
		}
	}
	index(top, &top.part)
	return top, nil
}

func parseEntity(raw []byte, start, end, depth int, count *int) (*rawPart, error) {
	if *count++; depth > maxMIMEDepth || *count > maxMIMEParts {
		return nil, failure("Gmail", "ValueError")
	}
	h, at, err := splitEntity(raw[start:end])
	if err != nil {
		return nil, err
	}
	r := &rawPart{start: start, content: start + at, end: end, cut: -1}
	for name, values := range h {
		for _, v := range values {
			r.part.Headers = append(r.part.Headers, header{name, v})
		}
	}
	r.part.MimeType = "text/plain"
	params := map[string]string{}
	if value := h.Get("Content-Type"); value != "" {
		// A type that does not parse is never a body.
		r.part.MimeType, params, _ = mime.ParseMediaType(value)
	}
	_, named, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	r.part.Filename = cmp.Or(named["filename"], params["name"])
	if !strings.HasPrefix(r.part.MimeType, "multipart/") {
		return r, nil
	}
	r.boundary = params["boundary"]
	spans, closing, err := multipartSpans(raw[r.content:end], r.boundary)
	if err != nil {
		return nil, err
	}
	r.insert = r.content + closing
	if len(spans) > 0 {
		r.insert = r.content + spans[0].cut
	}
	for _, s := range spans {
		child, err := parseEntity(raw, r.content+s.start, r.content+s.end, depth+1, count)
		if err != nil {
			return nil, err
		}
		child.cut, child.next, child.parent = r.content+s.cut, r.content+s.next, r
		r.part.Parts = append(r.part.Parts, child.part)
		r.children = append(r.children, child)
	}
	return r, nil
}

// splitEntity parses the header block of the MIME entity b, which ends at its first empty line, and gives the
// offset of the content after it. A header block may be empty; an entity without the empty line fails.
func splitEntity(b []byte) (textproto.MIMEHeader, int, error) {
	at := 0
	for {
		line, _, found := bytes.Cut(b[at:], []byte("\n"))
		if !found {
			return nil, 0, failure("Gmail", "ValueError")
		}
		at += len(line) + 1
		if len(bytes.TrimSuffix(line, []byte("\r"))) == 0 {
			break
		}
	}
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(b[:at]))).ReadMIMEHeader()
	if err != nil {
		return nil, 0, failure("Gmail", "ValueError")
	}
	return h, at, nil
}

// partSpan is one part of a multipart content: its delimiter line starts at cut, the part runs from start to end,
// and the next delimiter line starts at next.
type partSpan struct{ cut, start, end, next int }

// multipartSpans gives the parts of the multipart content b and the start of its close delimiter line. A part runs
// from after its delimiter line up to the line break before the next delimiter line, which belongs to that delimiter
// (RFC 2046 section 5.1.1). The preamble and the epilogue are not parts. Content without a close delimiter fails.
func multipartSpans(b []byte, boundary string) ([]partSpan, int, error) {
	if boundary == "" {
		return nil, 0, failure("Gmail", "ValueError")
	}
	dash := []byte("--" + boundary)
	spans := []partSpan{}
	open := false
	for at := 0; at < len(b); {
		next := len(b)
		if i := bytes.IndexByte(b[at:], '\n'); i >= 0 {
			next = at + i + 1
		}
		if rest, ok := bytes.CutPrefix(bytes.TrimRight(b[at:next], " \t\r\n"), dash); ok && (len(rest) == 0 || string(rest) == "--") {
			if open {
				s := &spans[len(spans)-1]
				s.end, s.next = at, at
				if s.end > s.start && b[s.end-1] == '\n' {
					s.end--
					if s.end > s.start && b[s.end-1] == '\r' {
						s.end--
					}
				}
			}
			if len(rest) > 0 {
				return spans, at, nil
			}
			spans, open = append(spans, partSpan{cut: at, start: next}), true
		}
		at = next
	}
	return nil, 0, failure("Gmail", "ValueError")
}
