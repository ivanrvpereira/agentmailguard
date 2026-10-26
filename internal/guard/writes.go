package guard

import (
	"context"
	"slices"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// checkIDs needs at least one ID. There is no maximum of ours: the providers change one message per call.
func checkIDs(ids []string) error {
	if len(ids) < 1 {
		return GuardError("give at least one message ID")
	}
	return nil
}
func (g *Guard) acquire(ctx context.Context) error { return acquire(ctx, g.mutation) }
func acquire(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *Guard) release() { <-g.mutation }

// Normalize at the external boundary: unsolicited IDs never enter the audit, and duplicates cannot prove success.
func normalize(ids []string, results []model.MutationResult, organizer bool) []model.MutationResult {
	counts := map[string]int{}
	byID := map[string]model.MutationResult{}
	for _, r := range results {
		counts[r.ID]++
		byID[r.ID] = r
	}
	out := make([]model.MutationResult, 0, len(ids))
	for _, id := range ids {
		r := byID[id]
		r.ID = id
		r.ApprovalID = nil
		if counts[id] != 1 {
			r.OK = false
			r.Error = new("failed")
		}
		if r.OK {
			r.Error = nil
		} else if organizer || r.Error == nil {
			r.Error = new("failed")
		} else {
			r.Error = safeText(r.Error)
		}
		out = append(out, r)
	}
	return out
}

// audited runs call for the IDs that the policy did not hold and writes one record with a result for every ID.
// held has the results of the IDs that the policy kept from the provider. allowed lists the distinct IDs with
// an allow decision; their policy records are written with the operation record. A manual call without held
// IDs is not audited when call fails; otherwise a failure becomes the result of each sent ID.
func (g *Guard) audited(ctx context.Context, op audit.Operation, ids []string, held map[string]model.MutationResult, allowed []string, opts audit.Options, call func([]string) ([]model.MutationResult, error)) ([]model.MutationResult, error) {
	send := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { _, ok := held[id]; return ok })
	var results []model.MutationResult
	var err error
	if len(send) > 0 {
		results, err = call(send)
	}
	if err != nil && opts.Rule == nil && len(held) == 0 {
		// Only these fixed refusals are public prose; external GuardError text is not trusted.
		if ctx.Err() == nil {
			switch err {
			case GuardError("move refused: the destination must be a folder ID from `folders`"),
				GuardError("move refused: use mail.trash"),
				GuardError("flag state must be one of flagged, complete, none"),
				GuardError("categorize needs at least one category to add or remove"),
				GuardError("categorize refused: not in the category list"):
				return nil, err
			}
		}
		return nil, safeErr(ctx, err)
	}
	if err != nil {
		results = make([]model.MutationResult, 0, len(send))
		for _, id := range send {
			results = append(results, model.MutationResult{ID: id, Error: new(safeCode(err))})
		}
	} else {
		results = normalize(send, results, opts.Rule != nil)
	}
	// results has one entry per sent ID, in input order.
	merged := make([]model.MutationResult, 0, len(ids))
	for _, id := range ids {
		r, ok := held[id]
		if !ok {
			r, results = results[0], results[1:]
		}
		merged = append(merged, r)
	}
	// Once a provider has returned a write result, cancellation must not erase its audit record.
	for _, id := range allowed {
		if e := g.recordDecision(ctx, op, keyTrash, decision{value: config.Allow}, &id, nil, opts.Rule); e != nil {
			return nil, e
		}
	}
	if e := g.log.Record(context.WithoutCancel(ctx), g.account, op, merged, opts); e != nil {
		return nil, GuardError("AuditError")
	}
	if err != nil && len(held) == 0 {
		return nil, safeErr(ctx, err)
	}
	return merged, nil
}
func (g *Guard) mutationCall(ctx context.Context, op audit.Operation, ids []string, opts audit.Options) ([]model.MutationResult, error) {
	if err := checkIDs(ids); err != nil {
		return nil, err
	}
	var held map[string]model.MutationResult
	var allowed []string
	if op == audit.Trash {
		var err error
		if held, allowed, err = g.trashPolicy(ctx, ids, opts.Rule); err != nil {
			return nil, err
		}
	}
	return g.audited(ctx, op, ids, held, allowed, opts, func(ids []string) ([]model.MutationResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var call func([]string) ([]model.MutationResult, error)
		switch op {
		case audit.Archive:
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Archive(ctx, ids) }
		case audit.Trash:
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Trash(ctx, ids) }
		case audit.Move:
			folders, err := g.mailbox.Folders(ctx)
			if err != nil {
				return nil, err
			}
			i := slices.IndexFunc(folders, func(f model.Folder) bool { return f.ID == *opts.Folder })
			if i < 0 {
				return nil, GuardError("move refused: the destination must be a folder ID from `folders`")
			}
			// A move into deleted items would bypass the trash operation.
			if folders[i].Trash {
				return nil, GuardError("move refused: use mail.trash")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Move(ctx, ids, *opts.Folder) }
		case audit.Read, audit.Unread:
			call = func(ids []string) ([]model.MutationResult, error) {
				return g.mailbox.MarkRead(ctx, ids, op == audit.Read)
			}
		case audit.Flag:
			if opts.Flag == nil || (*opts.Flag != model.Flagged && *opts.Flag != model.Complete && *opts.Flag != model.NoFlag) {
				return nil, GuardError("flag state must be one of flagged, complete, none")
			}
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Flag(ctx, ids, *opts.Flag) }
		case audit.Categorize:
			if len(opts.Add)+len(opts.Remove) == 0 {
				return nil, GuardError("categorize needs at least one category to add or remove")
			}
			categories, err := g.mailbox.Categories(ctx)
			if err != nil {
				return nil, err
			}
			for _, names := range [][]string{opts.Add, opts.Remove} {
				for _, name := range names {
					if !slices.Contains(categories, name) {
						return nil, GuardError("categorize refused: not in the category list")
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			call = func(ids []string) ([]model.MutationResult, error) {
				return g.mailbox.Categorize(ctx, ids, opts.Add, opts.Remove)
			}
		default:
			return nil, GuardError("invalid operation")
		}
		if opts.Rule == nil {
			return call(ids)
		}
		return g.authorizedWrite(ctx, ids, *opts.Rule, call)
	})
}

// Validation above may wait on provider/auth permits. Only the final verdict check and write hold the security gate.
// The caller already owns mutation; security writers never acquire mutation in the reverse order.
func (g *Guard) authorizedWrite(ctx context.Context, ids []string, rule string, call func([]string) ([]model.MutationResult, error)) ([]model.MutationResult, error) {
	if err := acquire(ctx, g.security); err != nil {
		return nil, err
	}
	defer func() { <-g.security }()
	rows, err := g.cached(ctx, ids)
	if err != nil {
		return nil, err
	}
	verdict := model.Green
	switch rule {
	case "security:yellow":
		verdict = model.Yellow
	case "security:red":
		verdict = model.Red
	case "security:sealed":
		verdict = model.Sealed
	}
	allowed := []string{}
	results := []model.MutationResult{}
	for _, id := range ids {
		if row, ok := rows[id]; ok && row.Verdict == verdict {
			allowed = append(allowed, id)
		} else {
			results = append(results, model.MutationResult{ID: id, Error: new("failed")})
		}
	}
	if len(allowed) != 0 {
		written, err := call(allowed)
		if err != nil {
			return nil, err
		}
		results = append(results, normalize(allowed, written, true)...)
	}
	return results, nil
}
func (g *Guard) manual(ctx context.Context, op audit.Operation, ids []string, opts audit.Options) ([]model.MutationResult, error) {
	if err := checkIDs(ids); err != nil {
		return nil, err
	}
	if err := g.acquire(ctx); err != nil {
		return nil, err
	}
	defer g.release()
	return g.mutationCall(ctx, op, ids, opts)
}
func (g *Guard) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Archive, ids, audit.Options{})
}
func (g *Guard) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Trash, ids, audit.Options{})
}
func (g *Guard) Move(ctx context.Context, ids []string, dest string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Move, ids, audit.Options{Folder: &dest})
}
func (g *Guard) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	op := audit.Unread
	if read {
		op = audit.Read
	}
	return g.manual(ctx, op, ids, audit.Options{})
}
func (g *Guard) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Flag, ids, audit.Options{Flag: &state})
}
func (g *Guard) Categorize(ctx context.Context, ids, add, remove []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Categorize, ids, audit.Options{Add: append([]string{}, add...), Remove: append([]string{}, remove...)})
}

// compose applies the mail_draft decision to a draft operation, or the mail_send decision to a send, and the
// Jev action check, then runs call. source is the message that the action references; a new message has
// none. Reply and forward reference a received message, so the sealed decision applies to their source;
// update acts on a draft and has no sealed check. Jev sees the agent fields, and for a forward also the text
// of the forwarded message. call gives the draft ID; a send gives none, so its operation record has only the
// source ID, or no ID for a new message.
func (g *Guard) compose(ctx context.Context, op audit.Operation, source string, args any, call func() (string, error)) (string, error) {
	if err := g.acquire(ctx); err != nil {
		return "", err
	}
	defer g.release()
	send := op == audit.MailSend || op == audit.MailReply || op == audit.MailForward
	key := keyDraft
	if send {
		key = keySend
	}
	var ref *string
	if op != audit.DraftNew && op != audit.MailSend {
		ref = new(source)
	}
	d := decision{value: g.base(ctx, key)}
	if ref != nil && op != audit.DraftUpdate {
		decisions, err := g.decide(ctx, key, []string{source})
		if err != nil {
			return "", err
		}
		d = decisions[source]
	}
	d, err := g.checkAction(ctx, op, key, d, args)
	if err != nil {
		return "", err
	}
	if err := g.single(ctx, op, key, d, ref, args); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, err := call()
	if err != nil {
		return "", safeErr(ctx, err)
	}
	if id == "" && !send {
		return "", GuardError("ValueError")
	}
	var results []model.MutationResult
	opts := audit.Options{}
	switch {
	case ref != nil:
		results = []model.MutationResult{{ID: source, OK: true}}
		if !send {
			opts.DraftID = &id
		}
	case !send:
		results = []model.MutationResult{{ID: id, OK: true}}
	}
	if err := g.recordDecision(ctx, op, key, d, ref, nil, nil); err != nil {
		return "", err
	}
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, results, opts); err != nil {
		return "", GuardError("AuditError")
	}
	return id, nil
}

// DraftNew returns the draft that the provider made. compose refuses a result without an ID.
func (g *Guard) DraftNew(ctx context.Context, draft model.NewDraft) (model.DraftResult, error) {
	var made model.DraftResult
	_, err := g.compose(ctx, audit.DraftNew, "", newArgs(draft), func() (string, error) {
		var err error
		made, err = g.mailbox.DraftNew(ctx, draft)
		return made.ID, err
	})
	if err != nil {
		return model.DraftResult{}, err
	}
	return made, nil
}

// ReplyRecipients gives the To and CC that a reply to message id goes to, worked out from the original by the
// adapter (all: reply all), each address cleaned. A provider whose reply picks its own recipients makes the final
// list at send time. A failed read gives sourceUnavailable, never an empty list.
func (g *Guard) ReplyRecipients(ctx context.Context, id string, all bool) (model.Recipients, error) {
	if id == "" {
		return model.Recipients{}, GuardError("invalid request")
	}
	r, err := g.mailbox.ReplyRecipients(ctx, id, all)
	if ctx.Err() != nil {
		return model.Recipients{}, ctx.Err()
	}
	if err != nil {
		return model.Recipients{}, sourceUnavailable
	}
	clean := func(v []string) []string {
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, content.CleanSubject(s))
		}
		return out
	}
	return model.Recipients{To: clean(r.To), CC: clean(r.CC), BCC: []string{}}, nil
}

// DraftReply returns the reply draft that the provider made, and the screening verdict of its source from the
// cache after the write: "" when there is none.
func (g *Guard) DraftReply(ctx context.Context, id string, reply model.Reply) (model.DraftResult, model.Verdict, error) {
	var made model.DraftResult
	_, err := g.compose(ctx, audit.DraftReply, id, newReplyArgs(id, reply), func() (string, error) {
		var err error
		made, err = g.mailbox.DraftReply(ctx, id, reply)
		return made.ID, err
	})
	if err != nil {
		return model.DraftResult{}, "", err
	}
	return made, g.cachedVerdict(ctx, id), nil
}

// DraftForward returns the forward draft that the provider made, and the screening verdict of its source from the
// cache after the write: "" when there is none. A forward draft needs no recipient.
func (g *Guard) DraftForward(ctx context.Context, id string, forward model.Forward) (model.DraftResult, model.Verdict, error) {
	var made model.DraftResult
	_, err := g.compose(ctx, audit.DraftForward, id, newForwardArgs(id, forward), func() (string, error) {
		var err error
		made, err = g.mailbox.DraftForward(ctx, id, forward)
		return made.ID, err
	})
	if err != nil {
		return model.DraftResult{}, "", err
	}
	return made, g.cachedVerdict(ctx, id), nil
}

// cachedVerdict is the screening verdict of message id in the cache: "" when there is none.
func (g *Guard) cachedVerdict(ctx context.Context, id string) model.Verdict {
	rows, err := g.cached(ctx, []string{id})
	if err != nil {
		return ""
	}
	return rows[id].Verdict
}
func (g *Guard) DraftUpdate(ctx context.Context, id, body string) (string, error) {
	return g.compose(ctx, audit.DraftUpdate, id, bodyArgs{id, body}, func() (string, error) { return g.mailbox.DraftUpdate(ctx, id, body) })
}

// SendNew, SendReply, and SendForward send mail at once under the mail_send decision. Their params and
// stored args are those of the matching draft operation. SendNew and SendForward refuse a send without a
// recipient before any policy decision.
func (g *Guard) SendNew(ctx context.Context, draft model.NewDraft) error {
	if !draft.Addressed() {
		return GuardError("invalid request")
	}
	_, err := g.compose(ctx, audit.MailSend, "", newArgs(draft), func() (string, error) { return "", g.mailbox.SendNew(ctx, draft) })
	return err
}
func (g *Guard) SendReply(ctx context.Context, id string, reply model.Reply) error {
	_, err := g.compose(ctx, audit.MailReply, id, newReplyArgs(id, reply), func() (string, error) { return "", g.mailbox.SendReply(ctx, id, reply) })
	return err
}
func (g *Guard) SendForward(ctx context.Context, id string, forward model.Forward) error {
	if !forward.Addressed() {
		return GuardError("invalid request")
	}
	_, err := g.compose(ctx, audit.MailForward, id, newForwardArgs(id, forward), func() (string, error) { return "", g.mailbox.SendForward(ctx, id, forward) })
	return err
}
