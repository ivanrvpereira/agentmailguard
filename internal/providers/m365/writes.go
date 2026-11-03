package m365

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (c *Client) lockMutation(ctx context.Context) bool {
	select {
	case c.mutationGate <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func recipients(addresses []string) []map[string]any {
	out := []map[string]any{}
	for _, a := range addresses {
		out = append(out, map[string]any{"emailAddress": map[string]string{"address": a}})
	}
	return out
}

// post sends one POST under the mutation gate. The request helper never repeats a request.
func (b *mailbox) post(ctx context.Context, path string, body map[string]any) (object, error) {
	if !b.c.lockMutation(ctx) {
		return nil, requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	return b.c.request(ctx, http.MethodPost, b.c.endpoint(path, nil), body, false)
}

// itemBody is a Graph body of contentType text, or html with isHTML
// (https://learn.microsoft.com/en-us/graph/api/resources/itembody?view=graph-rest-1.0).
func itemBody(content string, isHTML bool) map[string]string {
	kind := "text"
	if isHTML {
		kind = "html"
	}
	return map[string]string{"contentType": kind, "content": content}
}

// newMessage is the Graph message of a new message: its recipients, subject, and body.
func newMessage(draft model.NewDraft) map[string]any {
	return map[string]any{"subject": draft.Subject, "body": itemBody(draft.Body, draft.HTML), "toRecipients": recipients(draft.To), "ccRecipients": recipients(draft.CC), "bccRecipients": recipients(draft.BCC)}
}

// DraftNew reads the result from the message that Graph creates and returns
// (https://learn.microsoft.com/en-us/graph/api/user-post-messages?view=graph-rest-1.0).
func (b *mailbox) DraftNew(ctx context.Context, draft model.NewDraft) (model.DraftResult, error) {
	d, err := b.post(ctx, "/me/messages", newMessage(draft))
	if err != nil {
		return model.DraftResult{}, err
	}
	return draftResult(d)
}

// draftResult reads a draft result from a message that Graph returned. A message without an ID fails.
func draftResult(d object) (model.DraftResult, error) {
	out := model.DraftResult{ID: text(d, "id"), ThreadID: text(d, "conversationId"), To: emails(d, "toRecipients"), CC: emails(d, "ccRecipients"), BCC: emails(d, "bccRecipients"), Subject: text(d, "subject")}
	if out.ID == "" {
		return model.DraftResult{}, invalidMetadata
	}
	return out, nil
}

// emails gives the addresses of a recipient list.
func emails(d object, k string) []string {
	out := []string{}
	for _, a := range addresses(d, k) {
		out = append(out, a.Address)
	}
	return out
}

// DraftReply gives the reply draft as Graph answered the last write.
func (b *mailbox) DraftReply(ctx context.Context, id string, reply model.Reply) (model.DraftResult, error) {
	d, err := b.reply(ctx, id, reply, false)
	if err != nil {
		return model.DraftResult{}, err
	}
	return draftResult(d)
}

// reply makes the reply draft of message id with createReply, or createReplyAll for all, with no comment and no
// body: Graph then writes the quoted original and picks the recipients, as Outlook does
// (https://learn.microsoft.com/en-us/graph/api/message-createreplyall?view=graph-rest-1.0). Graph does not document
// how a comment renders or how CC and BCC given there combine with its own, so the PATCH of respond adds the agent's
// body above the quote and the agent's CC and BCC to Microsoft's lists.
func (b *mailbox) reply(ctx context.Context, id string, reply model.Reply, send bool) (object, error) {
	action := "/createReply"
	if reply.All {
		action = "/createReplyAll"
	}
	return b.respond(ctx, "/me/messages/"+escapeID(id)+action, map[string]any{}, send, func(d object) (map[string]any, error) {
		patch, err := notePatch(d, reply.Body, reply.HTML)
		if err != nil {
			return nil, err
		}
		listed := model.Recipients{To: emails(d, "toRecipients"), CC: emails(d, "ccRecipients"), BCC: emails(d, "bccRecipients")}
		if merged := listed.With(reply.CC, reply.BCC); len(merged.CC) > len(listed.CC) || len(merged.BCC) > len(listed.BCC) {
			patch["ccRecipients"], patch["bccRecipients"] = recipients(merged.CC), recipients(merged.BCC)
		}
		return patch, nil
	})
}

// DraftForward gives the forward draft as Graph answered the last write.
func (b *mailbox) DraftForward(ctx context.Context, id string, f model.Forward) (model.DraftResult, error) {
	d, err := b.forward(ctx, id, f, false)
	if err != nil {
		return model.DraftResult{}, err
	}
	return draftResult(d)
}
func (b *mailbox) SendForward(ctx context.Context, id string, f model.Forward) error {
	_, err := b.forward(ctx, id, f, true)
	return err
}

// forward makes the forward draft of message id with createForward, with no comment and the agent's To when it gives
// one: Graph then writes the quoted original
// (https://learn.microsoft.com/en-us/graph/api/message-createforward?view=graph-rest-1.0). The PATCH of respond adds
// the note above the quote, as for a reply, and sets each of To, CC, and BCC that differs from the agent's list to it.
func (b *mailbox) forward(ctx context.Context, id string, f model.Forward, send bool) (object, error) {
	create := map[string]any{}
	if len(f.To) > 0 {
		create["message"] = map[string]any{"toRecipients": recipients(f.To)}
	}
	return b.respond(ctx, "/me/messages/"+escapeID(id)+"/createForward", create, send, func(d object) (map[string]any, error) {
		patch, err := notePatch(d, f.Body, f.HTML)
		if err != nil {
			return nil, err
		}
		for _, list := range []struct {
			key   string
			agent []string
		}{{"toRecipients", f.To}, {"ccRecipients", f.CC}, {"bccRecipients", f.BCC}} {
			if !slices.EqualFunc(emails(d, list.key), list.agent, strings.EqualFold) {
				patch[list.key] = recipients(list.agent)
			}
		}
		return patch, nil
	})
}

// respond makes a reply or forward draft with one POST of body to the create action, then sends one PATCH of the
// fields that edit gives for the created draft (https://learn.microsoft.com/en-us/graph/api/message-update?view=graph-rest-1.0),
// left out when edit gives none. With send, the draft is then sent
// (https://learn.microsoft.com/en-us/graph/api/message-send?view=graph-rest-1.0). Every write goes once under the
// mutation gate. A failed step returns its error and stops, so nothing is sent after it; the draft may stay in
// Drafts. respond gives Graph's answer of the create or the PATCH.
func (b *mailbox) respond(ctx context.Context, create string, body map[string]any, send bool, edit func(object) (map[string]any, error)) (object, error) {
	if !b.c.lockMutation(ctx) {
		return nil, requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	d, err := b.c.request(ctx, http.MethodPost, b.c.endpoint(create, nil), body, false)
	if err != nil {
		return nil, err
	}
	draft := text(d, "id")
	if draft == "" {
		return nil, invalidMetadata
	}
	patch, err := edit(d)
	if err != nil {
		return nil, err
	}
	path := b.c.endpoint("/me/messages/"+escapeID(draft), nil)
	if len(patch) > 0 {
		if d, err = b.c.request(ctx, http.MethodPatch, path, patch, false); err != nil {
			return nil, err
		}
		if text(d, "id") != draft {
			return nil, invalidMetadata
		}
	}
	if !send {
		return d, nil
	}
	if ctx.Err() != nil {
		return nil, requestFailed
	}
	_, err = b.c.request(ctx, http.MethodPost, b.c.endpoint("/me/messages/"+escapeID(draft)+"/send", nil), nil, false)
	return d, err
}

// notePatch is the PATCH of the agent's text (plain unless html) above the quoted original of draft d, by quotedBody;
// empty text changes no body.
func notePatch(d object, note string, html bool) (map[string]any, error) {
	patch := map[string]any{}
	if note == "" {
		return patch, nil
	}
	body := obj(d, "body")
	content, err := quotedBody(text(body, "contentType"), text(body, "content"), note, html)
	if err != nil {
		return nil, err
	}
	patch["body"] = content
	return patch, nil
}

// bodyTag is the opening body tag of an HTML document.
var bodyTag = regexp.MustCompile(`(?i)<body\b[^>]*>`)

// quotedBody is the body of a reply or forward draft whose quoted original Graph wrote as content of type kind, with
// the agent's note above it. In an HTML quote, the agent's HTML, or its text escaped with its line breaks, goes right
// after the opening body tag (at the start without one). Above a plain-text quote, text goes with a blank line; HTML
// makes the body HTML with the escaped quote below.
func quotedBody(kind, quote, note string, html bool) (map[string]string, error) {
	switch strings.ToLower(kind) {
	case "html":
		agent := note
		if !html {
			agent = model.HTMLBody(agent)
		}
		at := 0
		if tag := bodyTag.FindStringIndex(quote); tag != nil {
			at = tag[1]
		}
		return map[string]string{"contentType": "html", "content": quote[:at] + agent + quote[at:]}, nil
	case "text":
		if html {
			return map[string]string{"contentType": "html", "content": note + model.HTMLBody("\n\n"+quote)}, nil
		}
		return map[string]string{"contentType": "text", "content": note + "\n\n" + quote}, nil
	}
	return nil, invalidMetadata
}

// ReplyRecipients reads the original's sender, Reply-To, To, and CC strictly and applies model.ReplyRecipients with
// the account's address as the own address. A message from that address is one that the account sent.
func (b *mailbox) ReplyRecipients(ctx context.Context, id string, all bool) (model.Recipients, error) {
	d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), url.Values{"$select": {"from,replyTo,toRecipients,ccRecipients"}}), false)
	if err != nil {
		return model.Recipients{}, err
	}
	if text(d, "id") == "" {
		return model.Recipients{}, invalidMetadata
	}
	from, own := address(obj(d, "from")).Address, b.c.account.Email
	if from == "" {
		return model.Recipients{}, invalidMetadata
	}
	src := model.ReplySource{From: []string{from}, Sent: strings.EqualFold(from, own)}
	for _, list := range []struct {
		key string
		out *[]string
	}{{"replyTo", &src.ReplyTo}, {"toRecipients", &src.To}, {"ccRecipients", &src.CC}} {
		if *list.out, err = strictRecipients(d, list.key); err != nil {
			return model.Recipients{}, err
		}
	}
	return model.ReplyRecipients(src, all, []string{own}), nil
}

// send posts one Graph send action, which answers 202 Accepted without a body. A failed send is never sent again.
func (b *mailbox) send(ctx context.Context, path string, body map[string]any) error {
	_, err := b.post(ctx, path, body)
	return err
}
func (b *mailbox) SendNew(ctx context.Context, draft model.NewDraft) error {
	return b.send(ctx, "/me/sendMail", map[string]any{"message": newMessage(draft), "saveToSentItems": true})
}
func (b *mailbox) SendReply(ctx context.Context, id string, reply model.Reply) error {
	_, err := b.reply(ctx, id, reply, true)
	return err
}

// notDraft refuses a draft write of a message that is not a draft.
const notDraft = model.MailboxError("Graph error DraftError")

// draftFlag reads isDraft, which must be a JSON boolean, of message id; an answer for another message fails. Graph
// tells a draft from a sent message by isDraft (https://learn.microsoft.com/en-us/graph/api/resources/message?view=graph-rest-1.0).
func draftFlag(d object, id string) (bool, error) {
	var v *bool
	if json.Unmarshal(d["isDraft"], &v) != nil || v == nil || text(d, "id") != id {
		return false, invalidMetadata
	}
	return *v, nil
}

// ReadDraft reads isDraft, the recipients, the subject, and the body of message id. A message that is not a draft
// gives only IsDraft false. A draft body type other than text or html, or a body content that is not a string, fails.
func (b *mailbox) ReadDraft(ctx context.Context, id string) (model.DraftContent, error) {
	d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), url.Values{"$select": {"isDraft,toRecipients,ccRecipients,bccRecipients,subject,body"}}), false)
	if err != nil {
		return model.DraftContent{}, err
	}
	draft, err := draftFlag(d, id)
	if err != nil || !draft {
		return model.DraftContent{}, err
	}
	body := obj(d, "body")
	kind := strings.ToLower(text(body, "contentType"))
	var content *string
	if kind != "text" && kind != "html" || json.Unmarshal(body["content"], &content) != nil || content == nil {
		return model.DraftContent{}, invalidMetadata
	}
	out := model.DraftContent{IsDraft: true, Subject: text(d, "subject"), Body: *content, HTML: kind == "html"}
	for _, list := range []struct {
		key string
		out *[]string
	}{{"toRecipients", &out.To}, {"ccRecipients", &out.CC}, {"bccRecipients", &out.BCC}} {
		if *list.out, err = strictRecipients(d, list.key); err != nil {
			return model.DraftContent{}, err
		}
	}
	return out, nil
}

// strictRecipients reads the recipient list k of a message strictly: a JSON array of recipients that each have an
// address. Any other value fails, so that no recipient is silently dropped or blank.
func strictRecipients(d object, k string) ([]string, error) {
	var list []struct {
		EmailAddress struct{ Address string } `json:"emailAddress"`
	}
	if json.Unmarshal(d[k], &list) != nil || list == nil {
		return nil, invalidMetadata
	}
	out := make([]string, 0, len(list))
	for _, r := range list {
		if r.EmailAddress.Address == "" {
			return nil, invalidMetadata
		}
		out = append(out, r.EmailAddress.Address)
	}
	return out, nil
}

// DraftUpdate reads isDraft of message id and refuses a message that is not a draft; then it sends one PATCH of only
// the fields that change gives. Graph updates the recipients, subject, and body only of a draft and keeps the message
// ID (https://learn.microsoft.com/en-us/graph/api/message-update?view=graph-rest-1.0). The conversation comes from
// Graph's answer to the PATCH.
func (b *mailbox) DraftUpdate(ctx context.Context, id string, change model.DraftChange) (string, string, error) {
	if !b.c.lockMutation(ctx) {
		return "", "", requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	if err := b.onlyDraft(ctx, id); err != nil {
		return "", "", err
	}
	patch := map[string]any{}
	for _, list := range []struct {
		key   string
		value *[]string
	}{{"toRecipients", change.To}, {"ccRecipients", change.CC}, {"bccRecipients", change.BCC}} {
		if list.value != nil {
			patch[list.key] = recipients(*list.value)
		}
	}
	if change.Subject != nil {
		patch["subject"] = *change.Subject
	}
	if change.Body != nil {
		patch["body"] = itemBody(*change.Body, change.HTML)
	}
	d, err := b.c.request(ctx, http.MethodPatch, b.c.endpoint("/me/messages/"+escapeID(id), nil), patch, false)
	if err != nil {
		return "", "", err
	}
	if text(d, "id") != id {
		return "", "", invalidMetadata
	}
	return id, text(d, "conversationId"), nil
}

// onlyDraft reads isDraft of message id and refuses a message that is not a draft. The caller holds the mutation
// gate, so the write that follows acts on the message that was checked.
func (b *mailbox) onlyDraft(ctx context.Context, id string) error {
	d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), url.Values{"$select": {"isDraft"}}), false)
	if err != nil {
		return err
	}
	draft, err := draftFlag(d, id)
	if err != nil {
		return err
	}
	if !draft {
		return notDraft
	}
	if ctx.Err() != nil {
		return requestFailed
	}
	return nil
}

// draftWrite sends one request of method to path under the mutation gate after the draft check of message id. The
// request helper never repeats it.
func (b *mailbox) draftWrite(ctx context.Context, id, method, path string) error {
	if !b.c.lockMutation(ctx) {
		return requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	if err := b.onlyDraft(ctx, id); err != nil {
		return err
	}
	_, err := b.c.request(ctx, method, b.c.endpoint(path, nil), nil, false)
	return err
}

// DraftDelete deletes draft id with DELETE (https://learn.microsoft.com/en-us/graph/api/message-delete?view=graph-rest-1.0).
// Graph has a separate permanentDelete for a permanent delete, so this one leaves the item restorable.
func (b *mailbox) DraftDelete(ctx context.Context, id string) error {
	return b.draftWrite(ctx, id, http.MethodDelete, "/me/messages/"+escapeID(id))
}

// DraftSend sends draft id as it is with its send action and no request body, which Graph answers with 202 Accepted
// and saves in Sent Items (https://learn.microsoft.com/en-us/graph/api/message-send?view=graph-rest-1.0).
func (b *mailbox) DraftSend(ctx context.Context, id string) error {
	return b.draftWrite(ctx, id, http.MethodPost, "/me/messages/"+escapeID(id)+"/send")
}
func (b *mailbox) each(ctx context.Context, ids []string, call func(string) error) ([]model.MutationResult, error) {
	// Serialize mutation operations for this account, including category read-modify-write.
	out := make([]model.MutationResult, len(ids))
	if !b.c.lockMutation(ctx) {
		for i, id := range ids {
			code := string(requestFailed)
			out[i] = model.MutationResult{ID: id, Error: &code}
		}
		return out, nil
	}
	defer func() { <-b.c.mutationGate }()
	var wg sync.WaitGroup
	// Duplicate IDs must not race their own read-modify-write, but retain one result per input.
	unique := map[string][]int{}
	for i, id := range ids {
		unique[id] = append(unique[id], i)
	}
	for id, positions := range unique {
		wg.Go(func() {
			for _, i := range positions {
				err := call(id)
				out[i] = model.MutationResult{ID: id, OK: err == nil}
				if err != nil {
					s := err.Error()
					out[i].Error = &s
				}
			}
		})
	}
	wg.Wait()
	return out, nil
}
func (b *mailbox) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "inbox", "archive")
}
func (b *mailbox) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "inbox", "deleteditems")
}
func (b *mailbox) Move(ctx context.Context, ids []string, _, dest string) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error {
		_, err := b.c.request(ctx, http.MethodPost, b.c.endpoint("/me/messages/"+escapeID(id)+"/move", nil), map[string]any{"destinationId": folderName(dest)}, false)
		return err
	})
}
func (b *mailbox) patch(ctx context.Context, id string, patch map[string]any) error {
	_, err := b.c.request(ctx, http.MethodPatch, b.c.endpoint("/me/messages/"+escapeID(id), nil), patch, false)
	return err
}
func (b *mailbox) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error { return b.patch(ctx, id, map[string]any{"isRead": read}) })
}
func (b *mailbox) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	var status string
	switch state {
	case model.Flagged:
		status = "flagged"
	case model.Complete:
		status = "complete"
	case model.NoFlag:
		status = "notFlagged"
	default:
		return nil, invalidMetadata
	}
	return b.each(ctx, ids, func(id string) error {
		return b.patch(ctx, id, map[string]any{"flag": map[string]string{"flagStatus": status}})
	})
}
func (b *mailbox) Categorize(ctx context.Context, ids []string, add, remove []string) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error {
		d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), url.Values{"$select": {"categories"}}), false)
		if err != nil {
			return err
		}
		var current []*string
		if json.Unmarshal(d["categories"], &current) != nil || current == nil {
			return invalidMetadata
		}
		out := []string{}
		for _, name := range current {
			if name == nil {
				return invalidMetadata
			}
			if !slices.Contains(remove, *name) {
				out = append(out, *name)
			}
		}
		for _, name := range add {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		return b.patch(ctx, id, map[string]any{"categories": out})
	})
}
