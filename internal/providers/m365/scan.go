package m365

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (b *mailbox) ScanInbox(ctx context.Context, since time.Time, until *time.Time) (model.InboxScan, error) {
	out := model.InboxScan{Messages: []model.InboxEntry{}, Failures: []model.ScanFailure{}}
	if since.IsZero() || until != nil && !until.After(since) {
		return out, invalidMetadata
	}
	filter := "receivedDateTime ge " + since.UTC().Format(time.RFC3339Nano)
	if until != nil {
		filter += " and receivedDateTime lt " + until.UTC().Format(time.RFC3339Nano)
	}
	first := b.c.endpoint("/me/mailFolders/inbox/messages", url.Values{"$filter": {filter}, "$select": {"id,receivedDateTime,isDraft"}, "$top": {"100"}})
	fail := func(id *string, code string) {
		out.Failures = append(out.Failures, model.ScanFailure{ID: id, Error: "Graph error " + code})
	}
	seen := map[string]bool{}
	for link := first; link != ""; {
		if seen[link] || len(seen) >= 10000 {
			fail(nil, "InvalidPage")
			break
		}
		seen[link] = true
		d, err := b.c.get(ctx, link, false)
		if err != nil {
			if err == invalidPage {
				fail(nil, "InvalidPage")
			} else {
				fail(nil, "RequestFailed")
			}
			break
		}
		var items []json.RawMessage
		if len(d["value"]) == 0 || string(d["value"]) == "null" || json.Unmarshal(d["value"], &items) != nil {
			fail(nil, "InvalidPage")
			break
		}
		for _, raw := range items {
			var item object
			if json.Unmarshal(raw, &item) != nil || item == nil {
				fail(nil, "InvalidMetadata")
				continue
			}
			id := text(item, "id")
			if strings.TrimSpace(id) == "" {
				fail(nil, "InvalidMetadata")
				continue
			}
			var draft *bool
			if json.Unmarshal(item["isDraft"], &draft) != nil || draft == nil {
				fail(&id, "InvalidMetadata")
				continue
			}
			received, err := time.Parse(time.RFC3339Nano, text(item, "receivedDateTime"))
			if err != nil {
				fail(&id, "InvalidMetadata")
				continue
			}
			if !*draft && !received.Before(since) && (until == nil || received.Before(*until)) {
				out.Messages = append(out.Messages, model.InboxEntry{ID: id, ReceivedAt: received})
			}
		}
		link, err = b.c.next(d, first)
		if err != nil {
			fail(nil, "InvalidPage")
			break
		}
	}
	return out, nil
}
