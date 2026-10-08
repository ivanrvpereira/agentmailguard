package audit

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Policy keys that can appear in records and in refusal codes.
var policyKeys = map[string]bool{
	"mail_trash": true, "mail_draft": true, "mail_send": true,
	"calendar_invite": true, "calendar_update": true, "calendar_cancel": true, "calendar_respond": true,
}

// API operations that the write policy decides.
var policyOperations = map[string]bool{
	"mail.trash": true, "draft.new": true, "draft.reply": true, "draft.forward": true, "draft.update": true,
	"calendar.invite": true, "calendar.update": true, "calendar.cancel": true, "calendar.respond": true,
}

// Pending is the fixed per-ID result code of an action stored for approval.
const Pending = "pending"

// RefusedPrefix starts the fixed refusal code of an action that the policy denies, before the policy key.
const RefusedPrefix = "refused by policy: "

// validResultError accepts safe failure codes and the exact fixed policy codes.
func validResultError(text *string) error {
	if text != nil && (*text == Pending || strings.HasPrefix(*text, RefusedPrefix) && policyKeys[strings.TrimPrefix(*text, RefusedPrefix)]) {
		return nil
	}
	return model.ValidateFailureText(text)
}

// Decision is one policy decision. It holds IDs and fixed codes only, never content.
type Decision struct {
	Operation  string   // API operation name, for example mail.trash
	Key        string   // policy key, for example mail_trash
	Decision   string   // allow, approve, or deny
	Reasons    []string // policy, and sealed when the sealed decision made it stricter
	ID         *string  // the message or event the action references, if any
	ApprovalID *string  // the stored action, on approve
	Rule       *string  // the organizer rule that made the write, if any
}

type policyRecord struct {
	Time       string   `json:"time"`
	Account    string   `json:"account"`
	Event      string   `json:"event"`
	Operation  string   `json:"operation"`
	Key        string   `json:"key"`
	Decision   string   `json:"decision"`
	Reasons    []string `json:"reasons"`
	Rule       *string  `json:"rule,omitempty"`
	ID         *string  `json:"id,omitempty"`
	ApprovalID *string  `json:"approval_id,omitempty"`
}

type approvalRecord struct {
	Time       string `json:"time"`
	Account    string `json:"account"`
	Event      string `json:"event"`
	Operation  string `json:"operation"`
	State      string `json:"state"`
	ApprovalID string `json:"approval_id"`
}

var approvalID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Policy appends one policy decision record.
func (l *Log) Policy(ctx context.Context, account string, d Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !policyOperations[d.Operation] || !policyKeys[d.Key] || !validRule(d.Rule) || len(d.Reasons) == 0 {
		return errors.New("invalid policy audit record")
	}
	switch d.Decision {
	case "allow", "deny":
		if d.ApprovalID != nil {
			return errors.New("invalid policy audit record")
		}
	case "approve":
		if d.ApprovalID == nil || !approvalID.MatchString(*d.ApprovalID) {
			return errors.New("invalid policy audit record")
		}
	default:
		return errors.New("invalid policy audit record")
	}
	for i, reason := range d.Reasons {
		if (reason != "policy" && reason != "sealed") || i > 0 && d.Reasons[i-1] == reason {
			return errors.New("invalid policy audit record")
		}
	}
	return l.append(ctx, policyRecord{now(), account, "policy", d.Operation, d.Key, d.Decision, d.Reasons, d.Rule, d.ID, d.ApprovalID})
}

// Approval appends one approval event: created, approved, denied, done, or failed.
func (l *Log) Approval(ctx context.Context, account, operation, state, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch state {
	case "created", "approved", "denied", "done", "failed":
	default:
		return errors.New("invalid approval audit record")
	}
	if !policyOperations[operation] || !approvalID.MatchString(id) {
		return errors.New("invalid approval audit record")
	}
	return l.append(ctx, approvalRecord{now(), account, "approval", operation, state, id})
}
