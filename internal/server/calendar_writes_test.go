package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type meetingRequest struct {
	Method, Path string
	Body         map[string]any
}

// recordingGraph serves event to every GET and records each other request, answering with reply (204 when empty).
// The returned function gives the only recorded request.
func recordingGraph(t *testing.T, event, reply string) (http.HandlerFunc, func() meetingRequest) {
	t.Helper()
	requests := make(chan meetingRequest, 4)
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, event)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- meetingRequest{r.Method, r.URL.Path, body}
		if reply == "" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, reply)
	}
	only := func() meetingRequest {
		t.Helper()
		if len(requests) != 1 {
			t.Fatalf("provider requests=%d", len(requests))
		}
		return <-requests
	}
	return handler, only
}

// calendarAllow allows the calendar keys, which the default policy denies.
const calendarAllow = "[policy]\ncalendar_invite='allow'\ncalendar_update='allow'\ncalendar_cancel='allow'\ncalendar_respond='allow'\n"

// calendarDaemon is a graphDaemon that allows the calendar writes, with handler as the Graph API.
func calendarDaemon(t *testing.T, scopes string, handler http.HandlerFunc) *daemon {
	t.Helper()
	return graphDaemon(t, calendarAllow, scopes, handler, newNativeAPI())
}

const organizerEvent = `{"id":"e1","subject":"CANARY raw","body":{"contentType":"text","content":"CANARY raw body"},"isOrganizer":true,"organizer":{"emailAddress":{"address":"synthetic@example.test"}},"start":{"dateTime":"2026-10-09T09:00:00","timeZone":"UTC"},"end":{"dateTime":"2026-10-09T10:00:00","timeZone":"UTC"},"attendees":[{"emailAddress":{"address":"keep@example.test"},"type":"optional"},{"emailAddress":{"address":"remove@example.test"},"type":"required"}]}`

var attendeeEvent = strings.Replace(organizerEvent, `"isOrganizer":true`, `"isOrganizer":false`, 1)

// Graph request bodies are owned by the provider tests; this one request proves that every update parameter reaches it.
func TestCalendarHTTPUpdateMapsParamsToSelectedFields(t *testing.T) {
	handler, request := recordingGraph(t, organizerEvent, "")
	d := calendarDaemon(t, "Calendars.ReadWrite", handler)
	status, body := accountRPC(t, d.s, "calendar.update", "", `{"id":"e1","subject":"CANARY replacement","location":"","body":"CANARY body","start":"2026-10-09T12:00:00+01:00","duration":"30m","add":["new@example.test"],"remove":["remove@example.test"],"online":true}`)
	if status != 200 || body != `{"id":"e1","ok":true}` {
		t.Fatalf("response=%d %s", status, body)
	}
	got := request()
	want := map[string]any{"subject": "CANARY replacement", "location": map[string]any{"displayName": ""}, "body": map[string]any{"contentType": "html", "content": "CANARY body"}, "start": map[string]any{"dateTime": "2026-10-09T11:00:00", "timeZone": "UTC"}, "end": map[string]any{"dateTime": "2026-10-09T11:30:00", "timeZone": "UTC"}, "attendees": []any{map[string]any{"emailAddress": map[string]any{"address": "keep@example.test"}, "type": "optional"}, map[string]any{"emailAddress": map[string]any{"address": "new@example.test"}, "type": "required"}}, "isOnlineMeeting": true, "onlineMeetingProvider": "teamsForBusiness"}
	if got.Method != "PATCH" || got.Path != "/me/events/e1" || !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("provider request=%+v", got)
	}
	assertAudited(t, d.home, "calendar_update")
}

// end cannot be sent with duration, so it needs its own request.
func TestCalendarHTTPUpdateMapsEndParam(t *testing.T) {
	handler, request := recordingGraph(t, organizerEvent, "")
	d := calendarDaemon(t, "Calendars.ReadWrite", handler)
	status, body := accountRPC(t, d.s, "calendar.update", "", `{"id":"e1","end":"2026-10-09T11:30:00+01:00"}`)
	if status != 200 || body != `{"id":"e1","ok":true}` {
		t.Fatalf("response=%d %s", status, body)
	}
	if got := request(); !reflect.DeepEqual(got.Body["end"], map[string]any{"dateTime": "2026-10-09T10:30:00", "timeZone": "UTC"}) {
		t.Fatalf("provider request=%+v", got)
	}
}

func TestCalendarHTTPCancelAndRespondMapParams(t *testing.T) {
	for _, tc := range []struct{ op, event, params, path, audit string }{
		{"calendar.cancel", organizerEvent, `{"id":"e1","comment":"CANARY comment"}`, "/me/events/e1/cancel", "calendar_cancel"},
		{"calendar.respond", attendeeEvent, `{"id":"e1","response":"tentative","comment":"CANARY comment"}`, "/me/events/e1/tentativelyAccept", "calendar_respond"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			handler, request := recordingGraph(t, tc.event, "")
			d := calendarDaemon(t, "Calendars.ReadWrite", handler)
			status, body := accountRPC(t, d.s, tc.op, "", tc.params)
			if status != 200 || body != `{"id":"e1","ok":true}` {
				t.Fatalf("response=%d %s", status, body)
			}
			if got := request(); got.Method != "POST" || got.Path != tc.path || got.Body["comment"] != "CANARY comment" {
				t.Fatalf("provider request=%+v, want POST %s with the comment", got, tc.path)
			}
			assertAudited(t, d.home, tc.audit)
		})
	}
}

func TestCalendarHTTPRespondNoNotifyMapsToSendResponse(t *testing.T) {
	handler, request := recordingGraph(t, attendeeEvent, "")
	d := calendarDaemon(t, "Calendars.ReadWrite", handler)
	status, body := accountRPC(t, d.s, "calendar.respond", "", `{"id":"e1","response":"decline","no_notify":true}`)
	if status != 200 || body != `{"id":"e1","ok":true}` {
		t.Fatalf("response=%d %s", status, body)
	}
	if got := request(); got.Path != "/me/events/e1/decline" || got.Body["sendResponse"] != false {
		t.Fatalf("provider request=%+v, want sendResponse false", got)
	}
	assertAudited(t, d.home, "calendar_respond")
}

func TestCalendarHTTPRespondRequiresExplicitOrganizerMetadata(t *testing.T) {
	for name, field := range map[string]string{"explicit false": `"isOrganizer":false,`, "missing": "", "null": `"isOrganizer":null,`, "string": `"isOrganizer":"false",`} {
		t.Run(name, func(t *testing.T) {
			var writes atomic.Int32
			event := strings.Replace(organizerEvent, `"isOrganizer":true,`, field, 1)
			d := calendarDaemon(t, "Calendars.ReadWrite", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, event)
					return
				}
				writes.Add(1)
				w.WriteHeader(202)
			})
			status, body := accountRPC(t, d.s, "calendar.respond", "", validCalendarParams["calendar.respond"])
			if name == "explicit false" {
				if status != 200 || body != `{"id":"e1","ok":true}` || writes.Load() != 1 {
					t.Fatalf("attendee RSVP: response=%d %s writes=%d", status, body, writes.Load())
				}
				return
			}
			if status != 500 || body != `{"error":"operation failed"}` || writes.Load() != 0 {
				t.Fatalf("unproven attendee role: response=%d %s writes=%d", status, body, writes.Load())
			}
			assertNoAudit(t, d.home)
		})
	}
}

var validCalendarParams = map[string]string{
	"calendar.invite":  `{"to":["a@example.test"],"subject":"","body":"","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z"}`,
	"calendar.update":  `{"id":"e1","subject":""}`,
	"calendar.cancel":  `{"id":"e1"}`,
	"calendar.respond": `{"id":"e1","response":"accept"}`,
}

// The Gmail grant is in TestGmailMeetingHTTPNeedsTheWriteGrant.
func TestCalendarHTTPRequiresWriteGrant(t *testing.T) {
	for op, params := range validCalendarParams {
		t.Run(op, func(t *testing.T) {
			var requests atomic.Int32
			d := calendarDaemon(t, "User.Read Mail.ReadWrite Calendars.Read role:Calendars.ReadWrite", func(w http.ResponseWriter, r *http.Request) { requests.Add(1); io.WriteString(w, organizerEvent) })
			status, body := accountRPC(t, d.s, op, "", params)
			if status != 500 || body != `{"error":"missing_scopes"}` || requests.Load() != 0 {
				t.Fatalf("response=%d %s requests=%d", status, body, requests.Load())
			}
			assertNoAudit(t, d.home)
		})
	}
}

func TestCalendarHTTPRejectsInvalidInputBeforeAccountAccess(t *testing.T) {
	var requests atomic.Int32
	d := calendarDaemon(t, "Calendars.ReadWrite", func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) })
	invalids := map[string][]string{
		"calendar.invite": {
			`{}`, `{"to":[],"subject":"","body":"","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z"}`,
			strings.Replace(validCalendarParams["calendar.invite"], `"subject":"",`, "", 1), strings.Replace(validCalendarParams["calendar.invite"], `"body":"",`, "", 1),
			strings.Replace(validCalendarParams["calendar.invite"], `["a@example.test"]`, `[""]`, 1),
			strings.Replace(validCalendarParams["calendar.invite"], `"subject":""`, `"subject":"`+strings.Repeat("s", 65537)+`"`, 1),
			strings.Replace(validCalendarParams["calendar.invite"], `2026-10-09T10:00:00Z`, `2026-10-09T09:00:00Z`, 1),
			strings.Replace(validCalendarParams["calendar.invite"], `2026-10-09T09:00:00Z`, `0000-10-09T09:00:00Z`, 1),
			strings.Replace(validCalendarParams["calendar.invite"], `2026-10-09T10:00:00Z`, `9999-12-31T23:45:00-01:00`, 1),
			strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"online":"false"}`, strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"online":0}`,
			// The old name has no alias.
			strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"teams":true}`,
			// An address is either required or optional.
			strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"optional":["A@EXAMPLE.test"]}`,
			strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"optional":[""]}`,
			strings.TrimSuffix(validCalendarParams["calendar.invite"], "}") + `,"optional":"b@example.test"}`,
		},
		"calendar.update": {
			`{}`, `{"id":"e1"}`, `{"id":"e1","add":[],"remove":[]}`, `{"id":"e1","body":7}`, `{"id":"e1","start":"bad"}`, `{"id":"e1","end":"bad"}`,
			`{"id":"e1","duration":"0m"}`, `{"id":"e1","duration":"-1h"}`, `{"id":"e1","duration":"1d"}`, `{"id":"e1","duration":"999999999999h"}`,
			`{"id":"e1","end":"2026-10-09T09:00:00Z","duration":"1h"}`,
			`{"id":"e1","start":"2026-10-09T10:00:00Z","end":"2026-10-09T09:00:00Z"}`,
			`{"id":"e1","start":"9999-12-31T23:45:00Z","duration":"1h"}`,
			`{"id":"e1","end":"0000-10-09T09:00:00Z"}`,
			`{"id":"e1","add":["SAME@example.test"],"remove":["same@example.test"]}`,
			`{"id":"e1","add":[""]}`,
			`{"id":"e1","subject":"` + strings.Repeat("s", 65537) + `"}`, `{"id":"e1","location":"` + strings.Repeat("s", 4097) + `"}`,
			// Updates can add an online meeting but never remove it.
			`{"id":"e1","online":false}`, `{"id":"e1","subject":"","online":false}`, `{"id":"e1","online":"true"}`, `{"id":"e1","teams":true}`,
		},
		"calendar.cancel":  {`{}`, `{"id":""}`, `{"id":"` + strings.Repeat("x", 4097) + `"}`, `{"id":"e1","response":"accept"}`},
		"calendar.respond": {`{}`, `{"id":"e1"}`, `{"id":"e1","response":"Accept"}`, `{"id":"e1","response":"decline","comment":7}`, `{"id":"e1","response":"accept","no_notify":"true"}`, `{"id":"e1","response":"accept","comment":"CANARY","no_notify":true}`},
	}
	for op, fields := range map[string][]string{
		"calendar.invite":  {"to", "optional", "subject", "body", "start", "end", "location", "online"},
		"calendar.update":  {"id", "subject", "location", "body", "start", "end", "duration", "add", "remove", "online"},
		"calendar.cancel":  {"id", "comment"},
		"calendar.respond": {"id", "response", "comment", "no_notify"},
	} {
		for _, field := range fields {
			var p map[string]any
			if err := json.Unmarshal([]byte(validCalendarParams[op]), &p); err != nil {
				t.Fatal(err)
			}
			p[field] = nil
			encoded, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			invalids[op] = append(invalids[op], string(encoded))
		}
	}
	for op, good := range validCalendarParams {
		invalids[op] = append(invalids[op], strings.TrimSuffix(good, "}")+`,"unknown":true}`, strings.TrimSuffix(good, "}")+`,"comment":null}`)
		key := `"id":"e1"`
		if op == "calendar.invite" {
			key = `"subject":""`
		}
		invalids[op] = append(invalids[op], strings.Replace(good, key, key+`,`+key, 1))
	}
	for op, cases := range invalids {
		for _, params := range cases {
			status, body := accountRPC(t, d.s, op, "", params)
			if status != 400 || body != `{"error":"invalid request"}` {
				t.Errorf("%s %.160s: %d %s", op, params, status, body)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input accessed provider")
	}
}

func TestCalendarHTTPRefusalsNeverWriteOrExposeRawEvents(t *testing.T) {
	for _, op := range []string{"calendar.update", "calendar.cancel", "calendar.respond"} {
		for _, state := range []string{"recurring", "cancelled", "wrong role", "wrong id", "fetch error", "invalid range"} {
			if state == "invalid range" && op != "calendar.update" {
				continue
			}
			t.Run(op+"/"+state, func(t *testing.T) {
				var writes atomic.Int32
				event := organizerEvent
				if op == "calendar.respond" {
					event = attendeeEvent
				}
				want := "operation failed"
				switch state {
				case "recurring":
					event = strings.TrimSuffix(event, "}") + `,"type":"occurrence"}`
					want = "calendar write refused: recurring events are not supported"
				case "cancelled":
					event = strings.TrimSuffix(event, "}") + `,"isCancelled":true}`
					want = "calendar write refused: event is cancelled"
				case "wrong role":
					if op == "calendar.respond" {
						event = organizerEvent
						want = "calendar response refused: organizer cannot respond"
					} else {
						event = attendeeEvent
						want = "calendar write refused: only the organizer can update or cancel"
					}
				case "wrong id":
					event = strings.Replace(event, `"id":"e1"`, `"id":"other"`, 1)
				case "invalid range":
					want = "invalid request"
				}
				d := calendarDaemon(t, "Calendars.ReadWrite", func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						writes.Add(1)
						w.WriteHeader(204)
						return
					}
					if state == "fetch error" {
						w.WriteHeader(400)
						io.WriteString(w, `{"error":{"code":"CANARY","message":"CANARY raw error"}}`)
						return
					}
					io.WriteString(w, event)
				})
				params := validCalendarParams[op]
				if state == "invalid range" {
					params = `{"id":"e1","end":"2026-10-09T08:00:00Z"}`
				}
				status, body := accountRPC(t, d.s, op, "", params)
				if status != 500 || body != `{"error":"`+want+`"}` || writes.Load() != 0 {
					t.Fatalf("response=%d %s writes=%d", status, body, writes.Load())
				}
				assertNoAudit(t, d.home)
			})
		}
	}
}

func TestCalendarHTTPWriteAndAuditFailuresStaySafe(t *testing.T) {
	for op, params := range validCalendarParams {
		for _, mode := range []string{"write error", "audit error", "auth error"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				var writes atomic.Int32
				scopes := "Calendars.ReadWrite"
				if mode == "auth error" {
					scopes = ""
				}
				d := calendarDaemon(t, scopes, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" {
						event := organizerEvent
						if op == "calendar.respond" {
							event = attendeeEvent
						}
						io.WriteString(w, event)
						return
					}
					writes.Add(1)
					if mode == "write error" {
						w.WriteHeader(400)
						io.WriteString(w, `{"error":{"code":"ErrorAccessDenied","message":"CANARY provider body"}}`)
						return
					}
					io.WriteString(w, `{"id":"new-event"}`)
				})
				if mode == "audit error" {
					if err := os.Mkdir(filepath.Join(d.home, "audit.jsonl"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				status, body := accountRPC(t, d.s, op, "", params)
				wantWrites := int32(1)
				if mode == "auth error" {
					wantWrites = 0
				}
				if status != 500 || body != `{"error":"operation failed"}` || writes.Load() != wantWrites {
					t.Fatalf("response=%d %s writes=%d", status, body, writes.Load())
				}
			})
		}
	}
}

func TestCalendarHTTPInviteMapsParamsAndOnlyReturnsID(t *testing.T) {
	handler, request := recordingGraph(t, "", `{"id":"new-event","subject":"CANARY raw"}`)
	d := calendarDaemon(t, "User.Read cAlEnDaRs.ReAdWrItE", handler)
	status, body := accountRPC(t, d.s, "calendar.invite", "", `{"to":["CANARY@example.test"],"optional":["CANARY-optional@example.test"],"subject":"CANARY subject","body":"CANARY body","location":"CANARY room","start":"2026-10-09T10:00:00+01:00","end":"2026-10-09T19:00:00+01:00"}`)
	if status != 200 || body != `{"id":"new-event"}` {
		t.Fatalf("response=%d %s", status, body)
	}
	got := request()
	// Teams is the default.
	want := map[string]any{"subject": "CANARY subject", "body": map[string]any{"contentType": "html", "content": "CANARY body"}, "location": map[string]any{"displayName": "CANARY room"}, "start": map[string]any{"dateTime": "2026-10-09T09:00:00", "timeZone": "UTC"}, "end": map[string]any{"dateTime": "2026-10-09T18:00:00", "timeZone": "UTC"}, "attendees": []any{map[string]any{"emailAddress": map[string]any{"address": "CANARY@example.test"}, "type": "required"}, map[string]any{"emailAddress": map[string]any{"address": "CANARY-optional@example.test"}, "type": "optional"}}, "isOnlineMeeting": true, "onlineMeetingProvider": "teamsForBusiness"}
	if got.Method != "POST" || got.Path != "/me/events" || !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("provider request=%+v", got)
	}
	assertAudited(t, d.home, "calendar_invite")
}

func TestCalendarHTTPInviteExplicitFalseOptsOutOfOnlineMeeting(t *testing.T) {
	handler, request := recordingGraph(t, "", `{"id":"new-event"}`)
	d := calendarDaemon(t, "Calendars.ReadWrite", handler)
	status, body := accountRPC(t, d.s, "calendar.invite", "", strings.TrimSuffix(validCalendarParams["calendar.invite"], "}")+`,"online":false}`)
	if status != 200 || body != `{"id":"new-event"}` {
		t.Fatalf("response=%d %s", status, body)
	}
	if got := request(); got.Body["isOnlineMeeting"] != false || got.Body["onlineMeetingProvider"] != nil {
		t.Fatalf("provider request=%+v, want isOnlineMeeting false and no provider", got)
	}
}
