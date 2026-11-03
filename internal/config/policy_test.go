package config_test

import (
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

// policyOf returns the policy of each account of a valid file, by alias.
func policyOf(t *testing.T, text string) map[string]config.Policy {
	t.Helper()
	c, err := parse(t, text)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]config.Policy{}
	for _, a := range c.Accounts {
		out[a.Alias] = a.Policy
	}
	return out
}

func TestPolicyDefaultsWithoutPolicyTables(t *testing.T) {
	got := policyOf(t, head+work)["work"]
	want := config.Policy{
		Base: config.Decisions{
			MailTrash: "allow", MailSpam: "allow", MailDraft: "allow", MailSend: "deny",
			CalendarInvite: "deny", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
		},
		Sealed: config.SealedDecisions{MailTrash: "deny", MailSpam: "deny"},
	}
	if got != want {
		t.Fatalf("policy=%+v", got)
	}
}

func TestSpamPolicyAcceptsBaseAndSealedOverrides(t *testing.T) {
	got := policyOf(t, head+"[policy]\nmail_spam = 'deny'\n[policy.sealed]\nmail_spam = 'approve'\n"+work+"[accounts.work.policy]\nmail_spam = 'approve'\n[accounts.work.policy.sealed]\nmail_spam = 'allow'\n")["work"]
	if got.Base.MailSpam != config.Approve || got.Sealed.MailSpam != config.Allow {
		t.Fatalf("spam policy=%+v", got)
	}
}

func TestGlobalPolicyOverridesDefaultsKeyByKey(t *testing.T) {
	got := policyOf(t, head+"[policy]\nmail_send = 'approve'\nmail_trash = 'deny'\ncalendar_respond = 'allow'\n"+work)["work"]
	want := config.Decisions{
		MailTrash: "deny", MailSpam: "allow", MailDraft: "allow", MailSend: "approve",
		CalendarInvite: "deny", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "allow",
	}
	if got.Base != want {
		t.Fatalf("base=%+v", got.Base)
	}
}

func TestAccountPolicyBeatsGlobalPolicyForThatAccountOnly(t *testing.T) {
	got := policyOf(t, `version = 1
default_account = "personal"
[policy]
mail_send = "approve"
calendar_invite = "approve"
[accounts.personal]
provider = "gmail"
email = "personal@example.test"
client_id = "c"
client_secret = "s"
[accounts.personal.policy]
mail_send = "deny"
mail_draft = "approve"
[accounts.other]
provider = "gmail"
email = "other@example.test"
client_id = "c"
client_secret = "s"
`)
	personal := config.Decisions{
		MailTrash: "allow", MailSpam: "allow", MailDraft: "approve", MailSend: "deny",
		CalendarInvite: "approve", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
	}
	other := config.Decisions{
		MailTrash: "allow", MailSpam: "allow", MailDraft: "allow", MailSend: "approve",
		CalendarInvite: "approve", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
	}
	if got["personal"].Base != personal || got["other"].Base != other {
		t.Fatalf("personal=%+v other=%+v", got["personal"].Base, got["other"].Base)
	}
}

func TestSealedPolicyResolvesAccountThenGlobalThenDeny(t *testing.T) {
	got := policyOf(t, `version = 1
default_account = "personal"
[policy]
mail_trash = "deny"
[policy.sealed]
mail_trash = "approve"
[accounts.personal]
provider = "m365"
email = "personal@example.test"
client_id = "c"
tenant_id = "t"
[accounts.personal.policy.sealed]
mail_trash = "allow"
[accounts.other]
provider = "m365"
email = "other@example.test"
client_id = "c"
tenant_id = "t"
`)
	personal := config.SealedDecisions{MailTrash: "allow", MailSpam: "deny"}
	other := config.SealedDecisions{MailTrash: "approve", MailSpam: "deny"}
	if got["personal"].Sealed != personal || got["other"].Sealed != other {
		t.Fatalf("personal=%+v other=%+v", got["personal"].Sealed, got["other"].Sealed)
	}
	if got["personal"].Base.MailTrash != "deny" {
		t.Fatalf("sealed table changed the base decision: %+v", got["personal"].Base)
	}
}

// Replies and forwards of a sealed message are always refused, so the sealed tables accept only mail_trash and mail_spam.
func TestSealedTablesRefuseMailDraftAndMailSend(t *testing.T) {
	for _, tc := range []struct{ text, path string }{
		{"[policy.sealed]\nmail_draft = 'allow'\n" + work, "policy.sealed.mail_draft"},
		{"[policy.sealed]\nmail_send = 'deny'\n" + work, "policy.sealed.mail_send"},
		{work + "[accounts.work.policy.sealed]\nmail_draft = 'deny'\n", "accounts.work.policy.sealed.mail_draft"},
		{work + "[accounts.work.policy.sealed]\nmail_send = 'approve'\n", "accounts.work.policy.sealed.mail_send"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, err := parse(t, head+tc.text)
			if err == nil || err.Error() != "config.toml: "+tc.path+": unknown key" {
				t.Fatalf("error=%v, want unknown key at %s", err, tc.path)
			}
		})
	}
}

func TestInvalidPolicyNamesKeyPathWithoutValue(t *testing.T) {
	for _, tc := range []struct{ text, path string }{
		{"[policy]\nmail_sned = 'SECRET-CANARY'\n" + work, "policy.mail_sned"},
		{"[policy.foo]\nmail_send = 'deny'\nx = 'SECRET-CANARY'\n" + work, "policy.foo"},
		{work + "[accounts.work.policy.foo]\nx = 'SECRET-CANARY'\n", "accounts.work.policy.foo"},
		{"policy = 'SECRET-CANARY'\n" + work, "policy"},
		{"[policy]\nsealed = 'SECRET-CANARY'\n" + work, "policy.sealed"},
		{work + "policy = 'SECRET-CANARY'\n", "accounts.work.policy"},
		{work + "[accounts.work.policy]\nsealed = 'SECRET-CANARY'\n", "accounts.work.policy.sealed"},
		{"[policy]\nmail_send = 'SECRET-CANARY'\n" + work, "policy.mail_send"},
		{"[policy]\nmail_send = 'Allow'\n" + work, "policy.mail_send"},
		{"[policy]\nmail_send = ' allow'\n" + work, "policy.mail_send"},
		{"[policy]\nmail_send = 1\n" + work, "policy.mail_send"},
		{"[policy]\nmail_send = ['allow']\n" + work, "policy.mail_send"},
		{"[policy]\nmail_send = { x = 'allow' }\n" + work, "policy.mail_send"},
		{work + "[accounts.work.policy]\ncalendar_cancel = 'SECRET-CANARY'\n", "accounts.work.policy.calendar_cancel"},
		{"[policy.sealed]\ncalendar_respond = 'allow'\n" + work, "policy.sealed.calendar_respond"},
		{work + "[accounts.work.policy.sealed]\ncalendar_invite = 'deny'\n", "accounts.work.policy.sealed.calendar_invite"},
		{"[policy.sealed]\nmail_sned = 'SECRET-CANARY'\n" + work, "policy.sealed.mail_sned"},
		{"[policy.sealed]\nmail_trash = 'SECRET-CANARY'\n" + work, "policy.sealed.mail_trash"},
		{"[policy.sealed.foo]\nx = 'SECRET-CANARY'\n" + work, "policy.sealed.foo"},
		// Key names come from the file; unusual ones are quoted so the path stays one safe line.
		{"[policy]\n\"a.b\\u001b\" = 'deny'\n" + work, `policy."a.b\x1b"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, err := parse(t, head+tc.text)
			refusal(t, err, tc.path)
		})
	}
}
