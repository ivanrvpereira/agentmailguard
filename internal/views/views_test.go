package views_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

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
	message := model.Message{ID: "id", Sender: model.Address{Name: "SECRET-NAME", Address: "a@example.test"}, Subject: "SECRET-SUBJECT", To: []model.Address{}, CC: []model.Address{}, BCC: []model.Address{{Address: "hidden@example.test"}}, Attachments: []string{}, ReceivedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), Flag: model.NoFlag, Categories: []string{"listed", "SECRET-CATEGORY"}, Body: new("RAW-BODY")}
	rendered := content.Rendered{Markdown: "VISIBLE-BODY", Links: map[string]string{"L1": "https://example.test/SECRET-TOKEN"}}
	for _, verdict := range []model.Verdict{model.Green, model.Yellow, model.Sealed, model.Red, model.Error} {
		t.Run(string(verdict), func(t *testing.T) {
			row := views.Screen{Verdict: verdict, Error: new("TimeoutError")}
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
				for _, key := range []string{"sender", "subject", "to", "cc", "bcc", "attachments"} {
					if _, ok := fields[key]; !ok {
						t.Errorf("missing header %s", key)
					}
				}
			} else {
				for _, key := range []string{"subject", "to", "cc", "bcc", "attachments"} {
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
	if _, err := views.View(message, views.Screen{Verdict: model.Green}, nil, nil); err == nil {
		t.Fatal("green accepted missing body")
	}
	empty := content.Rendered{Links: map[string]string{}}
	v, err := views.View(model.Message{}, views.Screen{Verdict: model.Green}, nil, &empty)
	if err != nil {
		t.Fatal(err)
	}
	fields := jsonFields(t, v)
	if fields["body"] != "" || fields["subject"] != "" || fields["truncated"] != false {
		t.Fatal("available empty fields omitted")
	}
	for _, key := range []string{"to", "cc", "bcc", "attachments", "categories"} {
		a, ok := fields[key].([]any)
		if !ok || len(a) != 0 {
			t.Errorf("%s not empty array: %v", key, fields[key])
		}
	}
}

// The pretty text shows the view's fields in a fixed order, skips empty rows, shows dates in local time, and lists
// links in numeric order.
func TestToTextShowsTheViewAsLabeledRows(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("TST", 2*60*60)
	t.Cleanup(func() { time.Local = local })
	received := "2026-03-07T09:30:00+00:00"
	green := views.MailView{
		MailSummary: views.MailSummary{ID: "m1", ThreadID: "t1", Verdict: model.Green, ReceivedAt: received, IsRead: true, Flag: model.Flagged, Categories: []string{"Blue", "Red"},
			Auth:   map[string]*string{"spf": new("pass"), "dkim": new("pass"), "dmarc": nil, "compauth": new(""), "compauth_reason": nil},
			Sender: &views.Address{Name: new("Alice Example"), Address: "alice@example.test"},
			To:     &[]views.Address{{Address: "bob@example.test"}, {Name: new(""), Address: "carol@example.test"}},
			CC:     &[]views.Address{}, Subject: new("Quarterly numbers"), Attachments: &[]string{"report.pdf"}},
		Body: new("Hello [L2] and [L10]."), Links: &map[string]string{"L10": "example.org", "L2": "example.test"}, Truncated: new(true),
	}
	red := views.MailView{MailSummary: views.MailSummary{ID: "m2", Verdict: model.Red, ReceivedAt: received, Auth: map[string]*string{}, Flag: model.NoFlag, Categories: []string{},
		Sender: &views.Address{Address: "x@example.test"}, Reason: new("injection")}}
	failed := views.MailView{MailSummary: views.MailSummary{ID: "m3", Verdict: model.Error, ReceivedAt: received, Auth: map[string]*string{}, Flag: model.NoFlag, Categories: []string{},
		Reason: new("screening_error"), Error: new("TimeoutError")}}
	for name, tc := range map[string]struct {
		view views.MailView
		want string
	}{
		"green": {green, `From:        Alice Example <alice@example.test>
To:          bob@example.test, carol@example.test
Subject:     Quarterly numbers
Date:        2026-03-07 11:30 TST
Verdict:     green
Auth:        spf=pass dkim=pass
Read:        yes
Flag:        flagged
Categories:  Blue, Red
Attachments: report.pdf
ID:          m1
Thread:      t1

Hello [L2] and [L10].

(body truncated)

[L2] example.test
[L10] example.org`},
		"red": {red, `From:        x@example.test
Date:        2026-03-07 11:30 TST
Verdict:     red (injection)
Read:        no
Flag:        none
ID:          m2`},
		"error": {failed, `Date:        2026-03-07 11:30 TST
Verdict:     error (screening_error)
Error:       TimeoutError
Read:        no
Flag:        none
ID:          m3`},
	} {
		if got := views.ToText(tc.view); got != tc.want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, tc.want)
		}
	}
}
