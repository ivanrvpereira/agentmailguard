package views_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

func TestScreeningStateMatchesPythonExplicitFields(t *testing.T) {
	r := content.Rendered{Markdown: "Visible", Links: map[string]string{"L1": "https://example.test/private"}, Truncated: true}
	cases := []struct {
		name  string
		state map[string]any
		want  string
	}{
		{"mail", views.ScreeningState(model.Message{ID: "not-screened", IsRead: true, Flag: model.Flagged, Categories: []string{"not-screened"}}, r), `{"sender":{"name":"","address":""},"subject":"","to":[],"cc":[],"attachments":[],"auth":{},"body":"Visible","links":{"L1":"example.test"}}`},
		{"event", views.EventScreeningState(model.Event{ID: "not-screened"}, &r), `{"subject":"","all_day":false,"recurrence":null,"location":"","organizer":{"name":"","address":""},"attendees":[],"join_url":null,"attachments":[],"categories":[],"show_as":"","cancelled":false,"body":"Visible","links":{"L1":"example.test"},"truncated":true}`},
		{"private event", views.EventScreeningState(model.Event{ID: "not-screened", IsPrivate: true}, &r), `{"subject":"","all_day":false,"recurrence":null,"location":"","organizer":{"name":"","address":""},"attendees":[],"join_url":null,"attachments":[],"categories":[],"show_as":"","cancelled":false}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want map[string]any
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			if got := jsonFields(t, c.state); !reflect.DeepEqual(got, want) {
				t.Fatalf("state differs from Python: got %v; want %v", got, want)
			}
		})
	}
}
