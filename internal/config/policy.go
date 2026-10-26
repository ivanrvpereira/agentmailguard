package config

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Decision is what the server does with one write action: allow, approve, or deny.
type Decision string

const (
	Allow   Decision = "allow"
	Approve Decision = "approve"
	Deny    Decision = "deny"
)

// Decisions holds one decision per policy key. The struct is comparable, so a policy change
// makes a loaded Account differ from the one the server started with.
type Decisions struct {
	MailTrash, MailDraft, MailSend                                  Decision
	CalendarInvite, CalendarUpdate, CalendarCancel, CalendarRespond Decision
}

// SealedDecisions applies to actions on an existing message with the verdict sealed.
type SealedDecisions struct {
	MailTrash, MailDraft, MailSend Decision
}

// Policy is the fully resolved write policy of one account.
type Policy struct {
	Base   Decisions
	Sealed SealedDecisions
}

func (d *Decisions) field(key string) *Decision {
	switch key {
	case "mail_trash":
		return &d.MailTrash
	case "mail_draft":
		return &d.MailDraft
	case "mail_send":
		return &d.MailSend
	case "calendar_invite":
		return &d.CalendarInvite
	case "calendar_update":
		return &d.CalendarUpdate
	case "calendar_cancel":
		return &d.CalendarCancel
	case "calendar_respond":
		return &d.CalendarRespond
	}
	return nil
}

func (s *SealedDecisions) field(key string) *Decision {
	switch key {
	case "mail_trash":
		return &s.MailTrash
	case "mail_draft":
		return &s.MailDraft
	case "mail_send":
		return &s.MailSend
	}
	return nil
}

const bareKey = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// keyPath renders a TOML key path. Other key names are quoted, so file text cannot
// add lines or terminal escapes to an error.
func keyPath(keys ...string) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key
		if key == "" || strings.Trim(key, bareKey) != "" {
			parts[i] = strconv.Quote(key)
		}
	}
	return strings.Join(parts, ".")
}

const invalidDecision = "must be allow, approve, or deny"

// DefaultPolicy is the policy without any [policy] table.
var DefaultPolicy = Policy{
	Base: Decisions{
		MailTrash: Allow, MailDraft: Allow, MailSend: Deny,
		CalendarInvite: Deny, CalendarUpdate: Deny, CalendarCancel: Deny, CalendarRespond: Deny,
	},
	Sealed: SealedDecisions{MailTrash: Deny, MailDraft: Deny, MailSend: Deny},
}

// globalPolicy is the default policy with the [policy] table applied. Each account table applies on top of it.
func globalPolicy(document map[string]any) (Policy, error) {
	p := DefaultPolicy
	return p, p.apply(document, "policy")
}

// apply overrides p with the optional policy table that parent holds under the last key of path.
func (p *Policy) apply(parent map[string]any, path ...string) error {
	value, present := parent[path[len(path)-1]]
	if !present {
		return nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return fail("must be a table", path...)
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		if key != "sealed" {
			if p.Base.field(key) == nil {
				return fail("unknown key", append(path, key)...)
			}
			if !set(p.Base.field(key), table[key]) {
				return fail(invalidDecision, append(path, key)...)
			}
			continue
		}
		sealed, ok := table[key].(map[string]any)
		if !ok {
			return fail("must be a table", append(path, key)...)
		}
		for _, k := range slices.Sorted(maps.Keys(sealed)) {
			if p.Sealed.field(k) == nil {
				return fail("unknown key", append(path, key, k)...)
			}
			if !set(p.Sealed.field(k), sealed[k]) {
				return fail(invalidDecision, append(path, key, k)...)
			}
		}
	}
	return nil
}

// set stores value in field when the key is known and value is exactly a decision.
func set(field *Decision, value any) bool {
	d, ok := value.(string)
	if field == nil || !ok || (Decision(d) != Allow && Decision(d) != Approve && Decision(d) != Deny) {
		return false
	}
	*field = Decision(d)
	return true
}
