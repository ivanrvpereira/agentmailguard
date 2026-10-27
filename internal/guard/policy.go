package guard

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Policy keys. A key is the API operation name with "." changed to "_"; mail_draft covers the draft operations
// draft.new, draft.reply, draft.forward, draft.update, and draft.delete, and mail_send covers mail.send, mail.reply,
// mail.forward, and draft.send.
const (
	keyTrash = "mail_trash"
	keyDraft = "mail_draft"
	keySend  = "mail_send"
)

type approvalKey struct{}

// WithApproval marks ctx for the run of an action that a person approved. A guard write with this context
// skips the policy: it makes no decision, no sealed lookup, no policy record, and no pending item, and runs
// as an allowed action with its normal operation record. Only the approval run may create this context.
func WithApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalKey{}, true)
}

func approved(ctx context.Context) bool { return ctx.Value(approvalKey{}) != nil }

// withoutApproval removes the approval mark of ctx, for a new decision inside an approved run that the approval
// does not cover.
func withoutApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalKey{}, nil)
}

// SafeCode gives the safe failure code of err, for storage with a failed approval.
func SafeCode(err error) string { return safeCode(err) }

// refused is the fixed public refusal for a write that the policy denies.
func refused(key string) GuardError { return GuardError("refused by policy: " + key) }

// rank orders decisions allow < approve < deny. Any other value, also the zero value, counts as deny.
func rank(d config.Decision) int {
	switch d {
	case config.Allow:
		return 0
	case config.Approve:
		return 1
	}
	return 2
}

// normal maps the zero value and unknown values to deny, so a guard without a policy fails closed.
func normal(d config.Decision) config.Decision {
	return [...]config.Decision{config.Allow, config.Approve, config.Deny}[rank(d)]
}

// base is the base decision of key, or allow for an approved run. decide also skips the sealed decision of an approved run.
func (g *Guard) base(ctx context.Context, key string) config.Decision {
	if approved(ctx) {
		return config.Allow
	}
	return g.baseDecision(key)
}

func (g *Guard) baseDecision(key string) config.Decision {
	b := g.policy.Base
	switch key {
	case keyTrash:
		return normal(b.MailTrash)
	case keyDraft:
		return normal(b.MailDraft)
	case keySend:
		return normal(b.MailSend)
	case string(audit.CalendarInvite):
		return normal(b.CalendarInvite)
	case string(audit.CalendarUpdate):
		return normal(b.CalendarUpdate)
	case string(audit.CalendarCancel):
		return normal(b.CalendarCancel)
	case string(audit.CalendarRespond):
		return normal(b.CalendarRespond)
	}
	return config.Deny
}

// sealedDecision is the decision of key for a sealed message, and for a red message under any key but mail_trash.
// Only mail_trash has a sealed setting; every other key, such as a reply or forward under mail_draft or mail_send,
// is denied.
func (g *Guard) sealedDecision(key string) config.Decision {
	if key == keyTrash {
		return normal(g.policy.Sealed.MailTrash)
	}
	return config.Deny
}

// decision is the final policy decision for one action. reason is the policy record reason, sealed or red, of the
// referenced message's verdict when it made the decision stricter, and empty otherwise. jev is the Jev action check, nil when no check ran.
// draft is, for a draft send, the draft fields that the check saw, which a pending item stores beside its args; nil
// for other actions.
type decision struct {
	value  config.Decision
	reason string
	jev    *model.ActionCheck
	draft  *newDraftArgs
}

// actionEffects describes, by policy key, what an action does. Jev gets it beside the API operation name.
var actionEffects = map[string]string{
	keySend:                       "sends an email to the listed recipients",
	keyDraft:                      "creates a draft that the user may send later",
	string(audit.CalendarInvite):  "sends meeting invitations to the attendees",
	string(audit.CalendarUpdate):  "changes a meeting and notifies the attendees",
	string(audit.CalendarRespond): "sends a response to the meeting organizer",
	string(audit.CalendarCancel):  "cancels a meeting and notifies the attendees",
}

// checkAction asks Jev about the agent fields of args, for an action that the policy does not deny. A forward
// also gives Jev the text of the forwarded message, and a reply the recipients that it goes to (reply_recipients);
// if they are unavailable, the check fails. A draft send gives Jev the draft fields of d instead of its args. A flag
// or a failed check changes allow to approve; approve stays approve. An approved run is never checked again.
func (g *Guard) checkAction(ctx context.Context, op audit.Operation, key string, d decision, args any) (decision, error) {
	if approved(ctx) || d.value == config.Deny {
		return d, nil
	}
	if d.draft != nil {
		args = *d.draft
	}
	state, err := actionFields(args)
	if forward, ok := args.(forwardArgs); ok && err == nil {
		state["forwarded_message"], err = g.forwardedText(ctx, forward.ID)
	}
	if reply, ok := args.(replyArgs); ok && err == nil {
		var to model.Recipients
		if to, err = g.ReplyRecipients(ctx, reply.ID, reply.All); err == nil {
			state["reply_recipients"] = map[string]any{"to": to.To, "cc": to.CC}
		}
	}
	if ctx.Err() != nil {
		return d, ctx.Err()
	}
	var check model.ActionCheck
	switch {
	case err != nil:
		check = model.ActionFailure(safeCode(err))
	case state == nil:
		return d, nil
	default:
		check, err = g.screener.CheckAction(ctx, apiOperations[op], actionEffects[key], state)
		if ctx.Err() != nil {
			return d, ctx.Err()
		}
		if err != nil {
			check = model.ActionFailure(safeCode(err))
		} else {
			check = validCheck(check)
		}
	}
	d.jev = &check
	if check.Flag && d.value == config.Allow {
		d.value = config.Approve
	}
	return d, nil
}

// validCheck keeps a safe error code or a score from 0 to 1, sets the flag from the score, and keeps the
// mark of the fake engine.
func validCheck(c model.ActionCheck) model.ActionCheck {
	var v model.ActionCheck
	switch {
	case c.Error != nil:
		v = model.ActionFailure(*safeText(c.Error))
	case c.Score == nil:
		v = model.ActionFailure("ValidationError")
	default:
		v = model.ActionScore(*c.Score)
	}
	v.Fake = c.Fake
	return v
}

// actionFields gives the agent fields of the stored args without the referenced ID. Args with only the ID, such as
// a draft delete, and a response or a cancellation without a comment carry no agent text and give nil.
func actionFields(args any) (map[string]any, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, GuardError("ValueError")
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, GuardError("ValueError")
	}
	delete(fields, "id")
	if comment, ok := fields["comment"]; (ok && comment == "") || len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

// sourceUnavailable is the failed-check code when the text of a forwarded message, or the recipients of a reply,
// cannot be read.
const sourceUnavailable = GuardError("SourceUnavailableError")

// forwardedText reads the subject and rendered body of the message to forward through the screened read
// path. A failed read or a message whose body is not shown makes the text unavailable.
func (g *Guard) forwardedText(ctx context.Context, id string) (map[string]any, error) {
	m, _, r, err := g.read(ctx, id)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || r == nil {
		return nil, sourceUnavailable
	}
	return map[string]any{"subject": content.CleanSubject(m.Subject), "body": r.Markdown}, nil
}

// decide returns the decision of key for each message ID. It looks up or screens a message only when the
// sealed decision is stricter than the base decision; for a reply or forward that is whenever the base is not
// deny. A sealed or error verdict, or a failed lookup, counts as sealed. A red verdict counts as red, which gives
// the sealed decision to a reply or forward and leaves the base decision of a trash. The caller holds the mutation
// gate and not the security gate: screening can take the security gate.
func (g *Guard) decide(ctx context.Context, key string, ids []string) (map[string]decision, error) {
	base, sealed := g.base(ctx, key), g.sealedDecision(key)
	out := make(map[string]decision, len(ids))
	for _, id := range ids {
		out[id] = decision{value: base}
	}
	if approved(ctx) || rank(sealed) <= rank(base) {
		return out, nil
	}
	// A zero verdict, from a failed cache read or a failed screening, counts as sealed.
	verdicts := make([]model.Verdict, len(ids))
	if rows, err := g.cached(ctx, ids); err == nil {
		// The work never fails; a cancellation is returned below.
		_ = parallelItems(ctx, len(ids), func(i int) error {
			if row, ok := rows[ids[i]]; ok {
				verdicts[i] = row.Verdict
			} else if _, row, _, err := g.read(ctx, ids[i]); err == nil {
				verdicts[i] = row.Verdict
			}
			return nil
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		switch verdicts[i] {
		case model.Green, model.Yellow:
		case model.Red:
			if key != keyTrash {
				out[id] = decision{value: sealed, reason: "red"}
			}
		default:
			out[id] = decision{value: sealed, reason: "sealed"}
		}
	}
	return out, nil
}

// PendingError reports that the action was stored for approval and did not run. It is not a failure.
type PendingError struct{ ApprovalID string }

func (e PendingError) Error() string { return audit.Pending }

// apiOperations maps audit operations to the API operation names that pending items and policy records use.
var apiOperations = map[audit.Operation]string{
	audit.Trash: "mail.trash", audit.DraftNew: "draft.new", audit.DraftReply: "draft.reply", audit.DraftForward: "draft.forward", audit.DraftUpdate: "draft.update",
	audit.DraftDelete: "draft.delete", audit.MailSend: "mail.send", audit.MailReply: "mail.reply", audit.MailForward: "mail.forward", audit.DraftSend: "draft.send",
	audit.CalendarInvite: "calendar.invite", audit.CalendarUpdate: "calendar.update", audit.CalendarCancel: "calendar.cancel", audit.CalendarRespond: "calendar.respond",
}

// recordDecision writes the policy record of one action. A failed write fails closed.
// An approved run has no decision, so it writes no record.
func (g *Guard) recordDecision(ctx context.Context, op audit.Operation, key string, d decision, id, approval, rule *string) error {
	if approved(ctx) {
		return nil
	}
	reasons := []string{"policy"}
	if d.reason != "" {
		reasons = append(reasons, d.reason)
	}
	if d.jev != nil && d.jev.Failed() {
		reasons = append(reasons, "jev_error")
	} else if d.jev != nil && d.jev.Flag {
		reasons = append(reasons, "jev_flag")
	}
	if err := g.log.Policy(context.WithoutCancel(ctx), g.account, audit.Decision{Operation: apiOperations[op], Key: key, Decision: string(d.value), Reasons: reasons, ID: id, ApprovalID: approval, Rule: rule}); err != nil {
		return GuardError("AuditError")
	}
	return nil
}

// hold stores an approve action with args in the shape of the API params, and the draft fields of a draft send,
// then records the decision and the new item. If an audit write fails, the item is denied so that it can never run
// without its records.
func (g *Guard) hold(ctx context.Context, op audit.Operation, key string, d decision, id, rule *string, args any) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", GuardError("ValueError")
	}
	var draft []byte
	if d.draft != nil {
		if draft, err = json.Marshal(d.draft); err != nil {
			return "", GuardError("ValueError")
		}
	}
	approval, err := g.store.AddPending(ctx, g.account, apiOperations[op], data, draft, d.jev, g.now())
	if err != nil {
		return "", safeErr(ctx, err)
	}
	err = g.recordDecision(ctx, op, key, d, id, &approval, rule)
	if err == nil && g.log.Approval(context.WithoutCancel(ctx), g.account, apiOperations[op], "created", approval) != nil {
		err = GuardError("AuditError")
	}
	if err != nil {
		_, _ = g.store.DenyPending(context.WithoutCancel(ctx), approval, g.now())
		return "", err
	}
	return approval, nil
}

// single applies a decision to an action that is not per ID. It returns nil only for allow.
// deny gives the fixed refusal; approve stores args and gives PendingError.
func (g *Guard) single(ctx context.Context, op audit.Operation, key string, d decision, id *string, args any) error {
	switch d.value {
	case config.Allow:
		return nil
	case config.Approve:
		approval, err := g.hold(ctx, op, key, d, id, nil, args)
		if err != nil {
			return err
		}
		return PendingError{approval}
	}
	if err := g.recordDecision(ctx, op, key, d, id, nil, nil); err != nil {
		return err
	}
	return refused(key)
}

// trashPolicy decides each trash ID before any provider access. It records deny and approve decisions,
// stores each approve ID as its own pending item, and returns the results of the IDs it held back and
// the distinct IDs that the policy allows.
func (g *Guard) trashPolicy(ctx context.Context, ids []string, rule *string) (map[string]model.MutationResult, []string, error) {
	unique := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	decisions, err := g.decide(ctx, keyTrash, unique)
	if err != nil {
		return nil, nil, err
	}
	held := map[string]model.MutationResult{}
	allowed := []string{}
	for _, id := range unique {
		d := decisions[id]
		switch d.value {
		case config.Allow:
			allowed = append(allowed, id)
		case config.Approve:
			approval, err := g.hold(ctx, audit.Trash, keyTrash, d, &id, rule, idsArgs{[]string{id}})
			if err != nil {
				return nil, nil, err
			}
			held[id] = model.MutationResult{ID: id, Error: pointer(audit.Pending), ApprovalID: &approval}
		default:
			if err := g.recordDecision(ctx, audit.Trash, keyTrash, d, &id, nil, rule); err != nil {
				return nil, nil, err
			}
			held[id] = model.MutationResult{ID: id, Error: new(string(refused(keyTrash)))}
		}
	}
	return held, allowed, nil
}

// Stored args mirror the API params of each operation, so a stored action can run through the normal API call.
type idsArgs struct {
	IDs []string `json:"ids"`
}
type idArgs struct {
	ID string `json:"id"`
}
type newDraftArgs struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	HTML    bool     `json:"html"`
}

// storedList copies an address list for stored args. A nil list becomes an empty list, because the API refuses null.
func storedList(v []string) []string { return append([]string{}, v...) }

// newArgs stores every field of a new message. A nil list becomes an empty list, because the API refuses null.
func newArgs(d model.NewDraft) newDraftArgs {
	return newDraftArgs{storedList(d.To), storedList(d.CC), storedList(d.BCC), d.Subject, d.Body, d.HTML}
}

type replyArgs struct {
	ID   string   `json:"id"`
	Body string   `json:"body"`
	HTML bool     `json:"html"`
	All  bool     `json:"all"`
	CC   []string `json:"cc"`
	BCC  []string `json:"bcc"`
}

// newReplyArgs stores every field of a reply. A nil list becomes an empty list, because the API refuses null.
func newReplyArgs(id string, r model.Reply) replyArgs {
	return replyArgs{id, r.Body, r.HTML, r.All, storedList(r.CC), storedList(r.BCC)}
}

type draftChangeArgs struct {
	ID      string    `json:"id"`
	To      *[]string `json:"to,omitempty"`
	CC      *[]string `json:"cc,omitempty"`
	BCC     *[]string `json:"bcc,omitempty"`
	Subject *string   `json:"subject,omitempty"`
	Body    *string   `json:"body,omitempty"`
	HTML    bool      `json:"html,omitempty"`
}

// newDraftChangeArgs stores only the fields of the change: a field that was not given stays out. A given list that
// is nil becomes an empty list, which clears, because the API refuses null.
func newDraftChangeArgs(id string, c model.DraftChange) draftChangeArgs {
	list := func(v *[]string) *[]string {
		if v == nil {
			return nil
		}
		return new(storedList(*v))
	}
	return draftChangeArgs{id, list(c.To), list(c.CC), list(c.BCC), c.Subject, c.Body, c.HTML}
}

type forwardArgs struct {
	ID   string   `json:"id"`
	To   []string `json:"to"`
	CC   []string `json:"cc"`
	BCC  []string `json:"bcc"`
	Body string   `json:"body"`
	HTML bool     `json:"html"`
}

// newForwardArgs stores every field of a forward. A nil list becomes an empty list, because the API refuses null.
func newForwardArgs(id string, f model.Forward) forwardArgs {
	return forwardArgs{id, storedList(f.To), storedList(f.CC), storedList(f.BCC), f.Body, f.HTML}
}

type inviteArgs struct {
	To       []string `json:"to"`
	Optional []string `json:"optional,omitempty"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	HTML     bool     `json:"html"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Location string   `json:"location"`
	Online   bool     `json:"online"`
}
type changeArgs struct {
	ID       string   `json:"id"`
	Subject  *string  `json:"subject,omitempty"`
	Location *string  `json:"location,omitempty"`
	Body     *string  `json:"body,omitempty"`
	HTML     bool     `json:"html,omitempty"`
	Start    *string  `json:"start,omitempty"`
	End      *string  `json:"end,omitempty"`
	Duration *string  `json:"duration,omitempty"`
	Add      []string `json:"add,omitempty"`
	Remove   []string `json:"remove,omitempty"`
	Online   *bool    `json:"online,omitempty"`
}
type cancelArgs struct {
	ID      string `json:"id"`
	Comment string `json:"comment"`
}
type respondArgs struct {
	ID       string                `json:"id"`
	Response model.MeetingResponse `json:"response"`
	Comment  string                `json:"comment"`
	NoNotify bool                  `json:"no_notify,omitempty"`
}

func stamp3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return new(t.Format(time.RFC3339Nano))
}

// updateArgs gives only the fields of the original request: optional fields that were not given stay out.
func updateArgs(id string, change model.EventChange, duration time.Duration) changeArgs {
	args := changeArgs{ID: id, Subject: change.Subject, Location: change.Location, Body: change.Body, HTML: change.HTML, Start: stamp3339(change.Start), End: stamp3339(change.End)}
	if duration > 0 {
		args.Duration = new(duration.String())
	}
	if len(change.AddAttendees) > 0 {
		args.Add = change.AddAttendees
	}
	if len(change.RemoveAttendees) > 0 {
		args.Remove = change.RemoveAttendees
	}
	if change.Online {
		args.Online = new(true)
	}
	return args
}
