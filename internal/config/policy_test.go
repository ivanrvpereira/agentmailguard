package config_test

import (
	"context"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

const work = "[accounts.work]\nprovider = 'm365'\nclient_id = 'c'\ntenant_id = 't'\n"

func TestPolicyDefaultsWithoutPolicyTables(t *testing.T) {
	got, err := config.LoadAccount(context.Background(), file(t, work), nil)
	want := config.Policy{
		Base: config.Decisions{
			MailTrash: "allow", MailDraft: "allow", MailSend: "deny",
			CalendarInvite: "deny", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
		},
		Sealed: config.SealedDecisions{MailTrash: "deny", MailDraft: "deny", MailSend: "deny"},
	}
	if err != nil || got.Policy != want {
		t.Fatalf("policy=%+v error=%v", got.Policy, err)
	}
}

func TestGlobalPolicyOverridesDefaultsKeyByKey(t *testing.T) {
	path := file(t, "[policy]\nmail_send = 'approve'\nmail_trash = 'deny'\ncalendar_respond = 'allow'\n"+work)
	got, err := config.LoadAccount(context.Background(), path, nil)
	want := config.Decisions{
		MailTrash: "deny", MailDraft: "allow", MailSend: "approve",
		CalendarInvite: "deny", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "allow",
	}
	if err != nil || got.Policy.Base != want {
		t.Fatalf("base=%+v error=%v", got.Policy.Base, err)
	}
}

func TestAccountPolicyBeatsGlobalPolicyForThatAccountOnly(t *testing.T) {
	path := file(t, `[policy]
mail_send = "approve"
calendar_invite = "approve"
[accounts.personal]
provider = "gmail"
client_id = "c"
client_secret = "s"
[accounts.personal.policy]
mail_send = "deny"
mail_draft = "approve"
[accounts.other]
provider = "gmail"
client_id = "c"
client_secret = "s"
`)
	got, err := config.LoadAccounts(context.Background(), path)
	if err != nil || len(got) != 2 {
		t.Fatalf("accounts=%v error=%v", got, err)
	}
	personal := config.Decisions{
		MailTrash: "allow", MailDraft: "approve", MailSend: "deny",
		CalendarInvite: "approve", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
	}
	other := config.Decisions{
		MailTrash: "allow", MailDraft: "allow", MailSend: "approve",
		CalendarInvite: "approve", CalendarUpdate: "deny", CalendarCancel: "deny", CalendarRespond: "deny",
	}
	if got[0].Policy.Base != personal || got[1].Policy.Base != other {
		t.Fatalf("personal=%+v other=%+v", got[0].Policy.Base, got[1].Policy.Base)
	}
}

func TestSealedPolicyResolvesAccountThenGlobalThenDeny(t *testing.T) {
	path := file(t, `[policy]
mail_trash = "deny"
[policy.sealed]
mail_trash = "approve"
mail_draft = "allow"
[accounts.personal]
provider = "m365"
client_id = "c"
tenant_id = "t"
[accounts.personal.policy.sealed]
mail_trash = "allow"
[accounts.other]
provider = "m365"
client_id = "c"
tenant_id = "t"
`)
	got, err := config.LoadAccounts(context.Background(), path)
	if err != nil || len(got) != 2 {
		t.Fatalf("accounts=%v error=%v", got, err)
	}
	personal := config.SealedDecisions{MailTrash: "allow", MailDraft: "allow", MailSend: "deny"}
	other := config.SealedDecisions{MailTrash: "approve", MailDraft: "allow", MailSend: "deny"}
	if got[0].Policy.Sealed != personal || got[1].Policy.Sealed != other {
		t.Fatalf("personal=%+v other=%+v", got[0].Policy.Sealed, got[1].Policy.Sealed)
	}
	if got[0].Policy.Base.MailTrash != "deny" {
		t.Fatalf("sealed table changed the base decision: %+v", got[0].Policy.Base)
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
		{"[accounts.\"my work\"]\nprovider = 'm365'\nclient_id = 'c'\ntenant_id = 't'\n[accounts.\"my work\".policy]\nmail_send = 'SECRET-CANARY'\n", `accounts."my work".policy.mail_send`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := file(t, tc.text)
			// An exact match also proves that the value is absent.
			want := "invalid policy: " + tc.path
			_, err := config.LoadAccount(context.Background(), path, nil)
			if err == nil || err.Error() != want {
				t.Fatalf("LoadAccount error=%v, want %q", err, want)
			}
			_, err = config.LoadAccounts(context.Background(), path)
			if err == nil || err.Error() != want {
				t.Fatalf("LoadAccounts error=%v, want %q", err, want)
			}
		})
	}
}

func TestLoadAccountValidatesPoliciesOfUnselectedAccounts(t *testing.T) {
	path := file(t, work+`[accounts.personal]
provider = "gmail"
client_id = "c"
client_secret = "s"
[accounts.personal.policy.sealed]
calendar_invite = "SECRET-CANARY"
`)
	name := "work"
	for _, selected := range []*string{nil, &name} {
		_, err := config.LoadAccount(context.Background(), path, selected)
		if err == nil || err.Error() != "invalid policy: accounts.personal.policy.sealed.calendar_invite" {
			t.Fatalf("error=%v", err)
		}
	}
}
