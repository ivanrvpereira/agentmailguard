package m365_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func calendarWriteOperations() map[string]func(context.Context, model.CalendarWriter) error {
	return map[string]func(context.Context, model.CalendarWriter) error{
		"invite": func(ctx context.Context, writer model.CalendarWriter) error {
			_, err := writer.Invite(ctx, model.NewEvent{Subject: "SECRET", Body: "SECRET", Start: now, End: now.Add(time.Hour), Attendees: []string{"a@example.test"}})
			return err
		},
		"update": func(ctx context.Context, writer model.CalendarWriter) error {
			subject := "SECRET"
			return writer.Update(ctx, "SECRET", model.EventChange{Subject: &subject})
		},
		"cancel": func(ctx context.Context, writer model.CalendarWriter) error {
			return writer.Cancel(ctx, "SECRET", "SECRET")
		},
		"respond": func(ctx context.Context, writer model.CalendarWriter) error {
			return writer.Respond(ctx, "SECRET", model.EventResponse{Response: "accept", Comment: "SECRET"})
		},
	}
}

func TestCalendarWritesSafeErrorsWithoutRetries(t *testing.T) {
	for name, call := range calendarWriteOperations() {
		for _, tc := range []struct {
			status int
			want   string
		}{{403, "Graph error 403"}, {429, "Graph error 429"}, {503, "Graph error 503"}} {
			t.Run(name+"/"+tc.want, func(t *testing.T) {
				calls := 0
				c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(tc.status)
					jsonResponse(w, map[string]any{"error": map[string]string{"code": "SECRET", "message": "SECRET"}})
				})
				err := call(context.Background(), c.Calendar().(model.CalendarWriter))
				if err == nil || err.Error() != tc.want || calls != 1 {
					t.Fatalf("write = %v, requests = %d", err, calls)
				}
			})
		}
	}
}

func TestCalendarInviteRequiresReturnedID(t *testing.T) {
	for _, result := range []map[string]any{{}, {"id": ""}, {"id": nil}, {"id": 123}} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, result) })
		id, err := c.Calendar().(model.CalendarWriter).Invite(context.Background(), model.NewEvent{Start: now, End: now.Add(time.Hour)})
		if id != "" || err == nil || err.Error() != "Graph error InvalidMetadata" {
			t.Fatalf("invite without ID = %q, %v", id, err)
		}
	}
}

func TestCalendarWritesHonorCanceledContext(t *testing.T) {
	for name, call := range calendarWriteOperations() {
		t.Run(name, func(t *testing.T) {
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("canceled write reached Graph") })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := call(ctx, c.Calendar().(model.CalendarWriter)); err == nil {
				t.Fatal("canceled write succeeded")
			}
		})
	}
}

func TestCalendarWritesShareMailboxMutationGate(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1.0/me/messages/blocker" {
			close(started)
			<-release
			jsonResponse(w, map[string]any{"categories": []string{}})
			return
		}
		if r.URL.Path != "/v1.0/me/messages/blocker" {
			t.Error("calendar write bypassed mutation gate")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	first := make(chan error, 1)
	go func() {
		results, err := c.Mailbox().Categorize(context.Background(), []string{"blocker"}, []string{"Keep"}, nil)
		if err == nil && (len(results) != 1 || !results[0].OK) {
			t.Error("blocking mailbox write failed")
		}
		first <- err
	}()
	<-started
	for name, call := range calendarWriteOperations() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- call(ctx, c.Calendar().(model.CalendarWriter)) }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s bypassed mutation gate", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s ignored cancellation while waiting", name)
		}
		cancel()
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestCalendarRespondAlwaysSendsSelectedResponse(t *testing.T) {
	for _, tc := range []struct{ response, action string }{
		{"accept", "accept"}, {"tentative", "tentativelyAccept"}, {"decline", "decline"}, {"", ""}, {"ACCEPT", ""}, {"SECRET", ""},
	} {
		t.Run(tc.response, func(t *testing.T) {
			requests := 0
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if tc.action == "" {
					t.Error("invalid response reached Graph")
				}
				var body map[string]any
				if r.Method != "POST" || r.URL.EscapedPath() != "/v1.0/me/events/e%2F%2E%2E%3F%23%25/"+tc.action || r.Header.Get("Prefer") != `IdType="ImmutableId", outlook.timezone="UTC"` || json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"comment": "Reply", "sendResponse": true}) {
					t.Error("response must send reply and must not propose a time")
				}
				w.WriteHeader(http.StatusAccepted)
			})
			writer, ok := c.Calendar().(model.CalendarWriter)
			if !ok {
				t.Fatal("calendar cannot respond")
			}
			err := writer.Respond(context.Background(), "e/..?#%", model.EventResponse{Response: tc.response, Comment: "Reply"})
			if tc.action == "" {
				if err == nil || err.Error() != "Graph error InvalidMetadata" {
					t.Fatalf("invalid response = %v", err)
				}
			} else if err != nil || requests != 1 {
				t.Fatalf("response = %v, requests = %d", err, requests)
			}
		})
	}
}

func TestCalendarCancelSendsComment(t *testing.T) {
	requests := 0
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if r.Method != "POST" || r.URL.EscapedPath() != "/v1.0/me/events/e%2F%2E%2E%3F%23%25/cancel" || json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"comment": "Canceled"}) {
			t.Error("cancel must use the cancellation action with comment, never DELETE")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	writer, ok := c.Calendar().(model.CalendarWriter)
	if !ok {
		t.Fatal("calendar cannot cancel")
	}
	if err := writer.Cancel(context.Background(), "e/..?#%", "Canceled"); err != nil || requests != 1 {
		t.Fatalf("cancel = %v, requests = %d", err, requests)
	}
}

func TestCalendarUpdateRejectsMalformedAttendees(t *testing.T) {
	for _, attendees := range []any{
		nil, "SECRET", []any{nil}, []any{17}, []any{map[string]any{}},
		[]any{map[string]any{"type": "optional", "emailAddress": "SECRET"}},
		[]any{map[string]any{"type": "optional", "emailAddress": map[string]any{"address": 17}}},
		[]any{map[string]any{"type": "optional", "emailAddress": map[string]any{"address": " "}}},
		[]any{map[string]any{"type": "SECRET", "emailAddress": map[string]any{"address": "a@example.test"}}},
		[]any{map[string]any{"emailAddress": map[string]any{"address": "a@example.test"}}},
	} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("malformed attendees overwritten")
			}
			jsonResponse(w, map[string]any{"attendees": attendees})
		})
		writer := c.Calendar().(model.CalendarWriter)
		if err := writer.Update(context.Background(), "e", model.EventChange{AddAttendees: []string{"new@example.test"}}); err == nil || err.Error() != "Graph error InvalidMetadata" {
			t.Fatalf("malformed attendees accepted: %v", err)
		}
	}
}

func TestCalendarUpdateAttendeeDeltaPreservesExistingProperties(t *testing.T) {
	optional := map[string]any{"type": "optional", "emailAddress": map[string]any{"name": "Alice", "address": "Alice@example.test"}, "status": map[string]any{"response": "accepted", "time": "2026-10-01T12:00:00Z"}, "proposedNewTime": map[string]any{"custom": "preserve"}}
	resource := map[string]any{"type": "resource", "emailAddress": map[string]any{"name": "Room", "address": "room@example.test"}}
	writes := 0
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1.0/me/events/e%2F1" {
			t.Error("unescaped event ID")
		}
		if r.Method == "GET" {
			if r.URL.Query().Get("$select") != "attendees" {
				t.Error("attendee read overfetch")
			}
			jsonResponse(w, map[string]any{"attendees": []any{optional, resource, map[string]any{"type": "required", "emailAddress": map[string]any{"address": "remove@example.test"}}}})
			return
		}
		writes++
		var body map[string]any
		want := map[string]any{"attendees": []any{optional, resource, map[string]any{"type": "required", "emailAddress": map[string]any{"address": "new@example.test"}}}}
		if r.Method != "PATCH" || json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, want) {
			t.Errorf("attendee delta dropped properties, duplicated identities, or replaced body: %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	writer := c.Calendar().(model.CalendarWriter)
	if err := writer.Update(context.Background(), "e/1", model.EventChange{AddAttendees: []string{"ALICE@example.test", "new@example.test", "NEW@example.test"}, RemoveAttendees: []string{"REMOVE@example.test"}}); err != nil || writes != 1 {
		t.Fatalf("attendee update = %v, writes = %d", err, writes)
	}
}

func TestCalendarUpdateRejectsUnresolvedDuration(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unresolved duration reached Graph") })
	writer := c.Calendar().(model.CalendarWriter)
	duration := 30 * time.Minute
	if err := writer.Update(context.Background(), "e", model.EventChange{Duration: &duration}); err == nil || err.Error() != "Graph error InvalidMetadata" {
		t.Fatalf("unresolved duration = %v", err)
	}
}

func TestCalendarUpdateSelectedFields(t *testing.T) {
	start := time.Date(2026, 10, 9, 9, 0, 0, 0, time.FixedZone("WEST", 3600))
	end := start.Add(30 * time.Minute)
	subject, location := "Changed", ""
	for _, tc := range []struct {
		name   string
		change model.EventChange
		want   map[string]any
	}{
		{"subject", model.EventChange{Subject: &subject}, map[string]any{"subject": "Changed"}},
		{"clear-location", model.EventChange{Location: &location}, map[string]any{"location": map[string]any{"displayName": ""}}},
		{"resolved-time", model.EventChange{Start: &start, End: &end}, map[string]any{"start": map[string]any{"dateTime": "2026-10-09T08:00:00", "timeZone": "UTC"}, "end": map[string]any{"dateTime": "2026-10-09T08:30:00", "timeZone": "UTC"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "PATCH" || r.URL.EscapedPath() != "/v1.0/me/events/e%2F%2E%2E%3F%23%25" || r.Header.Get("Prefer") != `IdType="ImmutableId", outlook.timezone="UTC"` {
					t.Error("unexpected update request")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, tc.want) {
					t.Errorf("update must preserve body and unselected fields: %#v", body)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			writer, ok := c.Calendar().(model.CalendarWriter)
			if !ok {
				t.Fatal("calendar cannot update")
			}
			if err := writer.Update(context.Background(), "e/..?#%", tc.change); err != nil || requests != 1 {
				t.Fatalf("update = %v, requests = %d", err, requests)
			}
		})
	}
}

func TestCalendarInvitePayload(t *testing.T) {
	for _, teams := range []bool{false, true} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.EscapedPath() != "/v1.0/me/events" || r.Header.Get("Prefer") != `IdType="ImmutableId", outlook.timezone="UTC"` {
				t.Error("unexpected invite request")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			expected := map[string]any{
				"subject": "Planning", "body": map[string]any{"contentType": "text", "content": "Agenda"},
				"location":  map[string]any{"displayName": "Room"},
				"start":     map[string]any{"dateTime": "2026-10-09T08:00:00", "timeZone": "UTC"},
				"end":       map[string]any{"dateTime": "2026-10-09T08:30:00", "timeZone": "UTC"},
				"attendees": []any{map[string]any{"type": "required", "emailAddress": map[string]any{"address": "a@example.test"}}},
			}
			if teams {
				expected["isOnlineMeeting"] = true
				expected["onlineMeetingProvider"] = "teamsForBusiness"
			}
			if !reflect.DeepEqual(body, expected) {
				t.Errorf("invite payload = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			jsonResponse(w, map[string]string{"id": "immutable-event"})
		})
		writer, ok := c.Calendar().(model.CalendarWriter)
		if !ok {
			t.Fatal("calendar cannot invite")
		}
		start := time.Date(2026, 10, 9, 9, 0, 0, 0, time.FixedZone("WEST", 3600))
		id, err := writer.Invite(context.Background(), model.NewEvent{Subject: "Planning", Body: "Agenda", Location: "Room", Start: start, End: start.Add(30 * time.Minute), Attendees: []string{"a@example.test"}, Teams: teams})
		if err != nil || id != "immutable-event" {
			t.Fatalf("invite = %q, %v", id, err)
		}
	}
}
