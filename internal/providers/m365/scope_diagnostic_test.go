package m365_test

import (
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

func TestExtraScopesListsOnlyUnrequestedGrants(t *testing.T) {
	scopes := []string{"user.read", "Mail.ReadWrite", "Calendars.Read", "MailboxSettings.Read", "offline_access", "openid", "profile", "email", "Mail.Send", "Contacts.Read", "role:Mail.ReadWrite"}
	if extra := m365.ExtraScopes(scopes); !reflect.DeepEqual(extra, []string{"Mail.Send", "Contacts.Read", "role:Mail.ReadWrite"}) {
		t.Fatalf("extra scopes: %v", extra)
	}
	if extra := m365.ExtraScopes(scopes[:8]); len(extra) != 0 {
		t.Fatalf("requested scopes reported as extra: %v", extra)
	}
}
