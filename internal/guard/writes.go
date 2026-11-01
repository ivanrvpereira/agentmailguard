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
		return nil, publicErr(ctx, err)
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

// publicErr gives err itself when it is one of the fixed refusals, the only public prose (external GuardError text
// is not trusted), and its safe form otherwise.
func publicErr(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		switch err {
		case GuardError("move refused: the destination must be a folder ID from `folders`"),
			GuardError("move refused: use mail.trash"),
			GuardError("flag state must be one of flagged, complete, none"),
			GuardError("categorize needs at least one category to add or remove"),
			GuardError("categorize refused: not in the category list"):
			return err
		}
	}
	return safeErr(ctx, err)
}

// mutationCall runs op on ids. A move takes the messages from folder from.
func (g *Guard) mutationCall(ctx context.Context, op audit.Operation, ids []string, from string, opts audit.Options) ([]model.MutationResult, error) {
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
		call, err := g.writer(ctx, op, from, opts)
		if err != nil {
			return nil, err
		}
		if opts.Rule == nil {
			return call(ids)
		}
		return g.authorizedWrite(ctx, ids, *opts.Rule, call)
	})
}

// writer checks the arguments of op and gives the provider call that runs op. A move takes the messages from folder
// from.
func (g *Guard) writer(ctx context.Context, op audit.Operation, from string, opts audit.Options) (func([]string) ([]model.MutationResult, error), error) {
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
		call = func(ids []string) ([]model.MutationResult, error) {
			return g.mailbox.Move(ctx, ids, from, *opts.Folder)
		}
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
	return call, nil
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
	// Moves by message ID take the messages from the Inbox.
	return g.mutationCall(ctx, op, ids, model.Inbox, opts)
}

// conversation runs op on the messages of conversation thread in folder in, as manual runs it on message IDs. The
// provider chooses the messages under the write lock, right before the writes. A failed or invalid choice changes
// nothing; an empty one gets the argument checks of a run, then gives an empty result and writes no record, because
// nothing ran. The operation record
// carries thread; policy and approval records stay one per message.
func (g *Guard) conversation(ctx context.Context, op audit.Operation, thread, in string, opts audit.Options) ([]model.MutationResult, error) {
	if thread == "" || in == "" {
		return nil, GuardError("give a thread ID and a folder ID")
	}
	if err := g.acquire(ctx); err != nil {
		return nil, err
	}
	defer g.release()
	ids, err := g.mailbox.ThreadMessagesIn(ctx, thread, in)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, GuardError("ValueError")
		}
		seen[id] = true
	}
	if len(ids) == 0 {
		// Nothing changes, but the arguments get the checks of a run on messages.
		if _, err := g.writer(ctx, op, in, opts); err != nil {
			return nil, publicErr(ctx, err)
		}
		return []model.MutationResult{}, nil
	}
	opts.Thread = &thread
	return g.mutationCall(ctx, op, ids, in, opts)
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

// The conversation actions act on the messages of conversation thread in folder in (see conversation).
func (g *Guard) ArchiveConversation(ctx context.Context, thread, in string) ([]model.MutationResult, error) {
	return g.conversation(ctx, audit.Archive, thread, in, audit.Options{})
}
func (g *Guard) TrashConversation(ctx context.Context, thread, in string) ([]model.MutationResult, error) {
	return g.conversation(ctx, audit.Trash, thread, in, audit.Options{})
}
func (g *Guard) MoveConversation(ctx context.Context, thread, in, dest string) ([]model.MutationResult, error) {
	return g.conversation(ctx, audit.Move, thread, in, audit.Options{Folder: &dest})
}
func (g *Guard) MarkReadConversation(ctx context.Context, thread, in string, read bool) ([]model.MutationResult, error) {
	op := audit.Unread
	if read {
		op = audit.Read
	}
	return g.conversation(ctx, op, thread, in, audit.Options{})
}
func (g *Guard) CategorizeConversation(ctx context.Context, thread, in string, add, remove []string) ([]model.MutationResult, error) {
	return g.conversation(ctx, audit.Categorize, thread, in, audit.Options{Add: append([]string{}, add...), Remove: append([]string{}, remove...)})
}

// compose applies the mail_draft decision to a draft operation, or the mail_send decision to a send, and the
// Jev action check, then runs call. source is the message that the action references; a new message has
// none. Reply and forward reference a received message, so the sealed decision applies to their source.
// Update, delete, and send of a draft act on a draft and have no sealed check, but unless the policy denies them,
// they are refused before the Jev check and any write when their source is not a draft (onlyDraft); an approved run
// checks again. Jev sees the agent fields, for a forward also the text of the forwarded message, and for a draft
// send the draft's fields as read. A draft delete sends nothing and has no Jev check. call gives the draft ID of a
// draft operation that makes or changes a draft; for other actions it gives none, so the operation record has only
// the source ID, or no ID for a new message.
//
// Under the mutation gate, a draft update, delete, or send first withdraws the waiting sends of its draft, before its
// decision and any refusal, so one draft has at most one waiting send and no approval can claim an old send once a
// change of the draft holds the gate. A refused, held, or failed operation leaves them withdrawn. An update that
// withdrew a send and ran then replaces it (replaceSend) and gives the new approval ID as the second result; it is ""
// otherwise. A failed replacement gives its error after the update ran, as a failed audit record after a write does,
// and leaves no waiting send.
func (g *Guard) compose(ctx context.Context, op audit.Operation, source string, args any, call func() (string, error)) (string, string, error) {
	if err := g.acquire(ctx); err != nil {
		return "", "", err
	}
	defer g.release()
	send := op == audit.MailSend || op == audit.MailReply || op == audit.MailForward || op == audit.DraftSend
	key := keyDraft
	if send {
		key = keySend
	}
	var ref *string
	if op != audit.DraftNew && op != audit.MailSend {
		ref = new(source)
	}
	onDraft := op == audit.DraftUpdate || op == audit.DraftDelete || op == audit.DraftSend
	makesDraft := !send && op != audit.DraftDelete
	// Withdrawing only removes, so an operation on a draft withdraws the waiting sends of its draft before any refusal.
	withdrawn := 0
	if onDraft {
		var err error
		if withdrawn, err = g.withdrawSends(ctx, source); err != nil {
			return "", "", err
		}
	}
	d := decision{value: g.base(ctx, key)}
	if ref != nil && !onDraft {
		decisions, err := g.decide(ctx, key, []string{source})
		if err != nil {
			return "", "", err
		}
		d = decisions[source]
	}
	if onDraft && d.value != config.Deny {
		draft, err := g.onlyDraft(ctx, op, source)
		if err != nil {
			return "", "", err
		}
		if op == audit.DraftSend {
			d.draft = new(checkedDraft(draft))
		}
	}
	d, err := g.checkAction(ctx, op, key, d, args)
	if err != nil {
		return "", "", err
	}
	if err := g.single(ctx, op, key, d, ref, args); err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	id, err := call()
	if err != nil {
		return "", "", safeErr(ctx, err)
	}
	if id == "" && makesDraft {
		return "", "", GuardError("ValueError")
	}
	var results []model.MutationResult
	opts := audit.Options{}
	switch {
	case ref != nil:
		results = []model.MutationResult{{ID: source, OK: true}}
		if makesDraft {
			opts.DraftID = &id
		}
	case makesDraft:
		results = []model.MutationResult{{ID: id, OK: true}}
	}
	if err := g.recordDecision(ctx, op, key, d, ref, nil, nil); err != nil {
		return "", "", err
	}
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, results, opts); err != nil {
		return "", "", GuardError("AuditError")
	}
	if op != audit.DraftUpdate || withdrawn == 0 {
		return id, "", nil
	}
	approval, err := g.replaceSend(withoutApproval(ctx), id)
	if err != nil {
		return "", "", err
	}
	return id, approval, nil
}

// withdrawSends withdraws the waiting draft sends of draft in the account, with one approval record each, and gives
// how many it withdrew. It never runs or approves anything. A failed record fails closed; the items stay withdrawn.
func (g *Guard) withdrawSends(ctx context.Context, draft string) (int, error) {
	items, err := g.store.WithdrawDraftSends(ctx, g.account, draft, g.now())
	if err != nil {
		return 0, safeErr(ctx, err)
	}
	for _, p := range items {
		if g.log.Approval(context.WithoutCancel(ctx), p.Account, p.Op, "withdrawn", p.ID) != nil {
			return len(items), GuardError("AuditError")
		}
	}
	return len(items), nil
}

// replaceSend makes a new mail_send decision for draft id, whose waiting send an update withdrew, with a fresh read
// of the draft and a fresh Jev check. The caller holds the mutation gate and gives ctx without the approval mark, so
// the new send never inherits an approved update. allow and approve both store a new waiting send and give its
// approval ID: a draft change never sends mail by itself. deny stores none and gives "".
func (g *Guard) replaceSend(ctx context.Context, id string) (string, error) {
	d := decision{value: g.base(ctx, keySend)}
	if d.value == config.Deny {
		return "", g.recordDecision(ctx, audit.DraftSend, keySend, d, &id, nil, nil)
	}
	draft, err := g.onlyDraft(ctx, audit.DraftSend, id)
	if err != nil {
		return "", err
	}
	d.draft = new(checkedDraft(draft))
	if d, err = g.checkAction(ctx, audit.DraftSend, keySend, d, idArgs{id}); err != nil {
		return "", err
	}
	d.value = config.Approve
	return g.hold(ctx, audit.DraftSend, keySend, d, &id, nil, idArgs{id})
}

// checkedDraft is the part of a draft that Jev checks and a waiting draft send stores.
func checkedDraft(d model.DraftContent) newDraftArgs {
	return newArgs(model.NewDraft{To: d.To, CC: d.CC, BCC: d.BCC, Subject: d.Subject, Body: d.Body, HTML: d.HTML})
}

// DraftNew returns the draft that the provider made. compose refuses a result without an ID.
func (g *Guard) DraftNew(ctx context.Context, draft model.NewDraft) (model.DraftResult, error) {
	var made model.DraftResult
	_, _, err := g.compose(ctx, audit.DraftNew, "", newArgs(draft), func() (string, error) {
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
	return model.Recipients{To: content.CleanList(r.To), CC: content.CleanList(r.CC), BCC: []string{}}, nil
}

// DraftReply returns the reply draft that the provider made, and the screening verdict of its source from the
// cache after the write: "" when there is none.
func (g *Guard) DraftReply(ctx context.Context, id string, reply model.Reply) (model.DraftResult, model.Verdict, error) {
	var made model.DraftResult
	_, _, err := g.compose(ctx, audit.DraftReply, id, newReplyArgs(id, reply), func() (string, error) {
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
	_, _, err := g.compose(ctx, audit.DraftForward, id, newForwardArgs(id, forward), func() (string, error) {
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

// DraftUpdateNotDraft is the public refusal of a draft update of a message that is not a draft.
const DraftUpdateNotDraft = GuardError("draft update refused: not a draft")

// UpdatedDraft is the result of a draft update: the draft's message ID, which may be new, its conversation, and the
// approval ID of the new waiting send that replaced a withdrawn one; "" when the update made none.
type UpdatedDraft struct{ ID, ThreadID, SendApprovalID string }

// DraftUpdate applies change to draft id. A change without a field, or with HTML but no body, is refused before any
// policy decision.
func (g *Guard) DraftUpdate(ctx context.Context, id string, change model.DraftChange) (UpdatedDraft, error) {
	if id == "" || change.Empty() || change.HTMLWithoutBody() {
		return UpdatedDraft{}, GuardError("invalid request")
	}
	var thread string
	draft, send, err := g.compose(ctx, audit.DraftUpdate, id, newDraftChangeArgs(id, change), func() (string, error) {
		var draft string
		var err error
		draft, thread, err = g.mailbox.DraftUpdate(ctx, id, change)
		return draft, err
	})
	if err != nil {
		return UpdatedDraft{}, err
	}
	return UpdatedDraft{ID: draft, ThreadID: thread, SendApprovalID: send}, nil
}

// Public refusals of a draft write on a message that is not a draft.
const (
	DraftDeleteNotDraft = GuardError("draft delete refused: not a draft")
	DraftSendNotDraft   = GuardError("draft send refused: not a draft")
)

// notDraftRefusals maps each draft write to its refusal of a message that is not a draft.
var notDraftRefusals = map[audit.Operation]GuardError{audit.DraftUpdate: DraftUpdateNotDraft, audit.DraftDelete: DraftDeleteNotDraft, audit.DraftSend: DraftSendNotDraft}

// onlyDraft reads message id for the draft write op and refuses it with the refusal of op unless it is a draft. A
// failed read fails closed with its safe code.
func (g *Guard) onlyDraft(ctx context.Context, op audit.Operation, id string) (model.DraftContent, error) {
	draft, err := g.mailbox.ReadDraft(ctx, id)
	if err != nil {
		return model.DraftContent{}, safeErr(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return model.DraftContent{}, err
	}
	if !draft.IsDraft {
		return model.DraftContent{}, notDraftRefusals[op]
	}
	return draft, nil
}

// DraftDelete deletes draft id under the mail_draft decision; DraftSend sends it as it is at the provider under the
// mail_send decision. Their stored args are only the ID; a waiting send also stores the draft fields that Jev checked.
func (g *Guard) DraftDelete(ctx context.Context, id string) error {
	if id == "" {
		return GuardError("invalid request")
	}
	_, _, err := g.compose(ctx, audit.DraftDelete, id, idArgs{id}, func() (string, error) { return "", g.mailbox.DraftDelete(ctx, id) })
	return err
}
func (g *Guard) DraftSend(ctx context.Context, id string) error {
	if id == "" {
		return GuardError("invalid request")
	}
	_, _, err := g.compose(ctx, audit.DraftSend, id, idArgs{id}, func() (string, error) { return "", g.mailbox.DraftSend(ctx, id) })
	return err
}

// SendNew, SendReply, and SendForward send mail at once under the mail_send decision. Their params and
// stored args are those of the matching draft operation. SendNew and SendForward refuse a send without a
// recipient before any policy decision.
func (g *Guard) SendNew(ctx context.Context, draft model.NewDraft) error {
	if !draft.Addressed() {
		return GuardError("invalid request")
	}
	_, _, err := g.compose(ctx, audit.MailSend, "", newArgs(draft), func() (string, error) { return "", g.mailbox.SendNew(ctx, draft) })
	return err
}
func (g *Guard) SendReply(ctx context.Context, id string, reply model.Reply) error {
	_, _, err := g.compose(ctx, audit.MailReply, id, newReplyArgs(id, reply), func() (string, error) { return "", g.mailbox.SendReply(ctx, id, reply) })
	return err
}
func (g *Guard) SendForward(ctx context.Context, id string, forward model.Forward) error {
	if !forward.Addressed() {
		return GuardError("invalid request")
	}
	_, _, err := g.compose(ctx, audit.MailForward, id, newForwardArgs(id, forward), func() (string, error) { return "", g.mailbox.SendForward(ctx, id, forward) })
	return err
}
