package views_test

import (
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
	"strings"
	"testing"
)

func TestCalendarPolicy(t *testing.T) {
	event := model.Event{ID: "id", Subject: "Subject <b>", Location: "Location", Organizer: model.Address{Name: "NAME", Address: "o@example.test"}, Attendees: []model.Attendee{{Address: model.Address{Name: "ATTENDEE", Address: "a@example.test"}, Response: "accepted"}}, JoinURL: new("https://meet.example.test/join"), Categories: []string{"listed", "unlisted"}}
	rendered := content.Rendered{Markdown: "BODY", Links: map[string]string{"L1": "https://example.test/TOKEN"}}
	for _, verdict := range []model.Verdict{model.Green, model.Yellow, model.Red, model.Sealed, model.Error} {
		t.Run(string(verdict), func(t *testing.T) {
			v, err := views.EventView(event, model.Screening{Verdict: verdict, Error: new("TimeoutError")}, &rendered, []string{"listed"})
			if err != nil {
				t.Fatal(err)
			}
			fields := jsonFields(t, v)
			for _, key := range []string{"id", "verdict", "start", "end", "organizer"} {
				if _, ok := fields[key]; !ok {
					t.Fatal("missing", key)
				}
			}
			if verdict == model.Green {
				if fields["body"] != "BODY" || fields["all_day"] != false || fields["cancelled"] != false || fields["show_as"] != "" {
					t.Fatal(fields)
				}
				if fields["join_url"] != *event.JoinURL {
					t.Fatal(fields)
				}
				if _, ok := fields["recurrence"]; ok {
					t.Fatal("unavailable recurrence")
				}
			} else {
				for _, key := range []string{"body", "links", "truncated", "all_day", "recurrence", "location", "join_url", "attachments", "categories", "show_as", "cancelled"} {
					if _, ok := fields[key]; ok {
						t.Fatal("leaked", key)
					}
				}
				organizer := fields["organizer"].(map[string]any)
				if len(organizer) != 1 {
					t.Fatal(organizer)
				}
			}
			if verdict == model.Yellow {
				a := fields["attendees"].([]any)[0].(map[string]any)
				if len(a) != 1 {
					t.Fatal(a)
				}
				if fields["subject"] != "Subject ‹b›" {
					t.Fatal(fields)
				}
			} else if verdict != model.Green {
				if _, ok := fields["subject"]; ok {
					t.Fatal("minimal event subject")
				}
				if _, ok := fields["attendees"]; ok {
					t.Fatal("minimal event attendees")
				}
			}
		})
	}
	if _, err := views.EventView(event, model.Screening{Verdict: model.Green}, nil, nil); err == nil {
		t.Fatal("green public event missing body")
	}
	event.IsPrivate = true
	v, err := views.EventView(event, model.Screening{Verdict: model.Green}, &rendered, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := jsonFields(t, v)
	for _, key := range []string{"body", "links", "truncated"} {
		if _, ok := fields[key]; ok {
			t.Fatal("private body exposed")
		}
	}
	if _, err := views.EventView(event, model.Screening{Verdict: model.Green}, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://example.test", "https://example.test/with space", "https://example.test/é", "https://example.test/\n"} {
		event.JoinURL = &target
		state := views.EventScreeningState(event, &rendered)
		if state["join_url"] != nil {
			t.Fatal("unsafe join URL", state)
		}
	}
}
func TestScreeningStateCoverageAndCleaning(t *testing.T) {
	m := model.Message{ID: "id", Subject: strings.Repeat("a\u200b", 30000), Sender: model.Address{Name: "<x>", Address: "https://example.test"}, To: []model.Address{{Name: "[L2]"}}, Attachments: []string{"x\x1b[31m.pdf"}, Categories: []string{"listed"}, Flag: model.Flagged, IsRead: true}
	r := content.Rendered{Markdown: "BODY", Links: map[string]string{"L1": "https://example.test/TOKEN"}, Truncated: true}
	state := jsonFields(t, views.ScreeningState(m, r))
	if state["subject"] != strings.Repeat("a", 30000) {
		t.Fatal("subject truncated")
	}
	if state["body"] != "BODY" {
		t.Fatal("screened fields missing")
	}
	sender := state["sender"].(map[string]any)
	if sender["name"] != "‹x›" || sender["address"] != "[link removed]" {
		t.Fatal(sender)
	}
	if state["links"].(map[string]any)["L1"] != "example.test" {
		t.Fatal("raw URL screened")
	}
	event := model.Event{ID: "e", Subject: "<subject>", Location: "https://x.test", Organizer: m.Sender, Attachments: m.Attachments, Categories: m.Categories}
	es := views.EventScreeningState(event, &r)
	for _, key := range []string{"subject", "all_day", "recurrence", "location", "organizer", "attendees", "join_url", "attachments", "categories", "show_as", "cancelled", "body", "links", "truncated"} {
		if _, ok := es[key]; !ok {
			t.Error("missing", key)
		}
	}
}
