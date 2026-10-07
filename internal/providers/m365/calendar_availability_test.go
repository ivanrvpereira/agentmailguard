package m365_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func availability(t *testing.T, h http.HandlerFunc) model.CalendarAvailability {
	t.Helper()
	reader, ok := graphClient(t, h).Calendar().(model.CalendarAvailability)
	if !ok {
		t.Fatal("M365 calendar has no availability")
	}
	return reader
}

var lisbon = time.FixedZone("WEST", 3600)

func TestCalendarAvailabilityQueriesGetScheduleAndLabelsRequestedAddresses(t *testing.T) {
	requests := 0
	reader := availability(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		want := map[string]any{"schedules": []any{"Alice@Example.test", "bob@example.test"}, "startTime": map[string]any{"dateTime": "2026-10-09T08:00:00", "timeZone": "UTC"}, "endTime": map[string]any{"dateTime": "2026-10-09T09:15:00", "timeZone": "UTC"}, "availabilityViewInterval": float64(30)}
		if r.Method != "POST" || r.URL.Path != "/v1.0/me/calendar/getSchedule" || r.URL.RawQuery != "" || !strings.Contains(r.Header.Get("Prefer"), `outlook.timezone="UTC"`) || !reflect.DeepEqual(body, want) {
			t.Errorf("request %s %s prefer=%q body=%v", r.Method, r.URL, r.Header.Get("Prefer"), body)
		}
		jsonResponse(w, map[string]any{"value": []any{
			map[string]any{"scheduleId": "BOB@example.test", "availabilityView": "220", "scheduleItems": []any{map[string]any{"subject": "CANARY subject", "location": "CANARY room", "status": "busy"}}, "workingHours": map[string]any{"timeZone": map[string]any{"name": "CANARY zone"}}},
			map[string]any{"scheduleId": "stranger@example.test", "availabilityView": "000"},
			map[string]any{"scheduleId": "alice@example.test", "availabilityView": "301", "unexpected": "CANARY field"},
		}})
	})
	got, err := reader.Availability(context.Background(), []string{"Alice@Example.test", "bob@example.test"}, time.Date(2026, 10, 9, 9, 0, 0, 0, lisbon), time.Date(2026, 10, 9, 10, 15, 0, 0, lisbon))
	want := []model.Schedule{
		{Address: "Alice@Example.test", Slots: []model.AvailabilityStatus{"out_of_office", "free", "tentative"}},
		{Address: "bob@example.test", Slots: []model.AvailabilityStatus{"busy", "busy", "free"}},
	}
	if err != nil || requests != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("availability=%#v err=%v requests=%d", got, err, requests)
	}
}

func TestCalendarAvailabilityMarksUnprovenParticipantsUnknown(t *testing.T) {
	ok := map[string]any{"scheduleId": "ok@example.test", "availabilityView": "00"}
	entry := func(fields map[string]any) map[string]any {
		e := map[string]any{"scheduleId": "a@example.test"}
		for k, v := range fields {
			e[k] = v
		}
		return e
	}
	unknown := []model.AvailabilityStatus{"unknown", "unknown"}
	free := []model.AvailabilityStatus{"free", "free"}
	for _, tc := range []struct {
		name    string
		entries []any
		want    []model.AvailabilityStatus
	}{
		{"null error is no error", []any{entry(map[string]any{"availabilityView": "00", "error": nil})}, free},
		{"missing entry", nil, unknown},
		{"provider error", []any{entry(map[string]any{"availabilityView": "00", "error": map[string]any{"message": "CANARY error", "responseCode": "ErrorAccessDenied"}})}, unknown},
		{"empty error object", []any{entry(map[string]any{"availabilityView": "00", "error": map[string]any{}})}, unknown},
		{"duplicate", []any{entry(map[string]any{"availabilityView": "00"}), entry(map[string]any{"availabilityView": "00"})}, unknown},
		{"duplicate differing case", []any{entry(map[string]any{"availabilityView": "22"}), map[string]any{"scheduleId": "A@EXAMPLE.TEST", "availabilityView": "00"}}, unknown},
		{"missing view", []any{entry(nil)}, unknown},
		{"null view", []any{entry(map[string]any{"availabilityView": nil})}, unknown},
		{"numeric view", []any{entry(map[string]any{"availabilityView": 0})}, unknown},
		{"empty view", []any{entry(map[string]any{"availabilityView": ""})}, unknown},
		{"short view", []any{entry(map[string]any{"availabilityView": "0"})}, unknown},
		{"long view", []any{entry(map[string]any{"availabilityView": "000"})}, unknown},
		{"working elsewhere code", []any{entry(map[string]any{"availabilityView": "04"})}, unknown},
		{"invalid character", []any{entry(map[string]any{"availabilityView": "0x"})}, unknown},
		{"multibyte view", []any{entry(map[string]any{"availabilityView": "é"})}, unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := availability(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"value": append([]any{ok}, tc.entries...)})
			})
			got, err := reader.Availability(context.Background(), []string{"a@example.test", "ok@example.test"}, now, now.Add(time.Hour))
			want := []model.Schedule{{Address: "a@example.test", Slots: tc.want}, {Address: "ok@example.test", Slots: free}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("availability=%#v err=%v", got, err)
			}
		})
	}
}

func TestCalendarAvailabilityRootFailuresStaySafe(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		status     int
		body       string
	}{
		{"forbidden", "Graph error 403", 403, `{"error":{"code":"CANARY","message":"CANARY raw"}}`},
		{"throttled", "Graph error 429", 429, `{"error":{"message":"CANARY raw"}}`},
		{"not json", "Graph error InvalidPage", 200, `CANARY`},
		{"missing value", "Graph error InvalidPage", 200, `{"CANARY":[]}`},
		{"null value", "Graph error InvalidPage", 200, `{"value":null}`},
		{"object value", "Graph error InvalidPage", 200, `{"value":{"scheduleId":"a@example.test","availabilityView":"00"}}`},
		{"string entry", "Graph error InvalidPage", 200, `{"value":["CANARY"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			reader := availability(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			got, err := reader.Availability(context.Background(), []string{"a@example.test"}, now, now.Add(time.Hour))
			if got != nil || err == nil || err.Error() != tc.want || requests != 1 {
				t.Fatalf("availability=%v err=%v requests=%d", got, err, requests)
			}
		})
	}
}

func TestCalendarAvailabilityRefusesUnboundedOrCanceledQueriesWithoutGraph(t *testing.T) {
	reader := availability(t, func(w http.ResponseWriter, r *http.Request) { t.Error("refused query reached Graph") })
	twenty := make([]string, 21)
	for i := range twenty {
		twenty[i] = string(rune('a'+i)) + "@example.test"
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx        context.Context
		addresses  []string
		start, end time.Time
	}{
		"canceled":         {canceled, []string{"a@example.test"}, now, now.Add(time.Hour)},
		"no addresses":     {context.Background(), nil, now, now.Add(time.Hour)},
		"21 addresses":     {context.Background(), twenty, now, now.Add(time.Hour)},
		"empty range":      {context.Background(), []string{"a@example.test"}, now, now},
		"reversed range":   {context.Background(), []string{"a@example.test"}, now, now.Add(-time.Minute)},
		"62 days":          {context.Background(), []string{"a@example.test"}, now, now.Add(62 * 24 * time.Hour)},
		"overflowed range": {context.Background(), []string{"a@example.test"}, time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if got, err := reader.Availability(tc.ctx, tc.addresses, tc.start, tc.end); got != nil || err == nil {
			t.Errorf("%s: availability=%v err=%v", name, got, err)
		}
	}
	got, err := availability(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"value": []any{}})
	}).Availability(context.Background(), twenty[:20], now, now.Add(62*24*time.Hour-time.Minute))
	if err != nil || len(got) != 20 || len(got[19].Slots) != 2976 || got[19].Slots[2975] != "unknown" {
		t.Fatalf("largest query: %d schedules, err=%v", len(got), err)
	}
}
