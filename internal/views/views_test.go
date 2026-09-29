package views_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/content"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/views"
)

func ptr[T any](v T) *T { return &v }
func jsonFields(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
func TestMailVerdictViews(t *testing.T) {
	message := model.Message{ID: "id", Sender: model.Address{Name: "SECRET-NAME", Address: "a@example.test"}, Subject: "SECRET-SUBJECT", To: []model.Address{}, CC: []model.Address{}, Attachments: []string{}, ReceivedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), Flag: model.NoFlag, Categories: []string{"listed", "SECRET-CATEGORY"}, Body: ptr("RAW-BODY")}
	rendered := content.Rendered{Markdown: "VISIBLE-BODY", Links: map[string]string{"L1": "https://example.test/SECRET-TOKEN"}}
	for _, verdict := range []model.Verdict{model.Green, model.Yellow, model.Sealed, model.Red, model.Error} {
		t.Run(string(verdict), func(t *testing.T) {
			row := cache.Row{Verdict: verdict, Error: ptr("TimeoutError")}
			v, err := views.View(message, row, []string{"listed"}, &rendered)
			if err != nil {
				t.Fatal(err)
			}
			fields := jsonFields(t, v)
			data, _ := json.Marshal(v)
			for _, key := range []string{"id", "verdict", "received_at", "auth", "is_read", "flag", "categories"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("missing %s", key)
				}
			}
			if fields["is_read"] != false || fields["flag"] != "none" {
				t.Fatal(fields)
			}
			if fields["received_at"] != "2025-01-02T03:04:05+00:00" {
				t.Fatal("date differs from source", fields["received_at"])
			}
			for _, secret := range []string{"RAW-BODY", "SECRET-TOKEN", "SECRET-CATEGORY"} {
				if strings.Contains(string(data), secret) {
					t.Errorf("leaked %s", secret)
				}
			}
			if verdict == model.Green || verdict == model.Yellow {
				for _, key := range []string{"sender", "subject", "to", "cc", "attachments"} {
					if _, ok := fields[key]; !ok {
						t.Errorf("missing header %s", key)
					}
				}
			} else {
				for _, key := range []string{"subject", "to", "cc", "attachments"} {
					if _, ok := fields[key]; ok {
						t.Errorf("leaked header %s", key)
					}
				}
				if strings.Contains(string(data), "SECRET-NAME") {
					t.Fatal("display name leaked")
				}
			}
			if verdict == model.Error {
				if _, ok := fields["sender"]; ok {
					t.Fatal("error sender")
				}
				if fields["error"] != "TimeoutError" {
					t.Fatal(fields)
				}
			}
			if verdict == model.Green {
				if fields["body"] != "VISIBLE-BODY" || fields["truncated"] != false {
					t.Fatal(fields)
				}
			} else {
				for _, key := range []string{"body", "links", "truncated"} {
					if _, ok := fields[key]; ok {
						t.Fatal("body fields exposed")
					}
				}
			}
			pretty := views.ToText(v)
			if strings.Contains(pretty, "RAW-BODY") || strings.Contains(pretty, "SECRET-TOKEN") {
				t.Fatal("pretty leaks")
			}
			if summary := jsonFields(t, views.Summary(message, row, []string{"listed"})); summary["body"] != nil {
				t.Fatal("summary body")
			}
		})
	}
	if _, err := views.View(message, cache.Row{Verdict: model.Green}, nil, nil); err == nil {
		t.Fatal("green accepted missing body")
	}
	empty := content.Rendered{Links: map[string]string{}}
	v, err := views.View(model.Message{}, cache.Row{Verdict: model.Green}, nil, &empty)
	if err != nil {
		t.Fatal(err)
	}
	fields := jsonFields(t, v)
	if fields["body"] != "" || fields["subject"] != "" || fields["truncated"] != false {
		t.Fatal("available empty fields omitted")
	}
	for _, key := range []string{"to", "cc", "attachments", "categories"} {
		a, ok := fields[key].([]any)
		if !ok || len(a) != 0 {
			t.Errorf("%s not empty array: %v", key, fields[key])
		}
	}
}
