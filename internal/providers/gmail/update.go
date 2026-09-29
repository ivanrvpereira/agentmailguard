package gmail

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
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
func (m *mailbox) DraftUpdate(ctx context.Context, id, body string) (string, error) {
	draft, err := m.resolveDraft(ctx, id)
	if err != nil {
		return "", err
	}
	v, _, err := m.original(ctx, id, "full")
	if err != nil {
		return "", err
	}
	if !contains(v.LabelIDs, "DRAFT") {
		return "", failure("Gmail", "DraftError")
	}
	for _, h := range v.Payload.Headers {
		if strings.EqualFold(h.Name, "In-Reply-To") || strings.EqualFold(h.Name, "References") {
			return "", failure("Gmail", "DraftError")
		}
	}
	err = walk(&v.Payload, func(p *mimePart, attached bool) error {
		kind := strings.ToLower(p.MimeType)
		if attached || (kind != "text/html" && kind != "text/plain" && !strings.HasPrefix(kind, "multipart/")) {
			return failure("Gmail", "DraftError")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	kept := []header{}
	for _, name := range []string{"To", "Cc", "Bcc", "Subject"} {
		kept = append(kept, header{name, headerValue(v.Payload.Headers, name)})
	}
	raw, err := plainMessage(kept, body, nil)
	if err != nil {
		return "", err
	}
	data, err := m.c.write(ctx, "PUT", m.c.options.GmailURL+"/drafts/"+segment(draft), map[string]any{"id": draft, "message": map[string]string{"raw": base64.URLEncoding.EncodeToString(raw)}})
	if err != nil {
		return "", err
	}
	return draftID(data)
}
