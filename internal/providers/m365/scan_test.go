package m365_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScanExactWindowMetadataOnlyAndPartialFailure(t *testing.T) {
	since := time.Date(2026, 9, 21, 2, 0, 0, 123456000, time.FixedZone("plus2", 7200))
	until := since.Add(time.Hour)
	item := func(id, date string, draft any) any {
		return map[string]any{"id": id, "receivedDateTime": date, "isDraft": draft}
	}
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v1.0/me/mailFolders/inbox/messages" {
			t.Error("scan wrote or wrong folder")
		}
		switch q.Get("page") {
		case "":
			if q.Get("$select") != "id,receivedDateTime,isDraft" || q.Get("$filter") != "receivedDateTime ge 2026-09-21T00:00:00.123456Z and receivedDateTime lt 2026-09-21T01:00:00.123456Z" {
				t.Errorf("wrong scan query %v", q)
			}
			jsonResponse(w, map[string]any{"value": []any{item("edge", since.Format(time.RFC3339Nano), false), item("old", "2026-09-21T00:00:00.123455Z", false), item("draft", since.Format(time.RFC3339Nano), true), item("upper", until.Format(time.RFC3339Nano), false), item("bad-date", "SECRET", false), item("bad-draft", since.Format(time.RFC3339Nano), "false"), 17, nil}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/mailFolders/inbox/messages?page=2"})
		case "2":
			jsonResponse(w, map[string]any{"value": []any{item("second", "2026-09-21T00:30:00Z", false)}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/mailFolders/inbox/messages?page=3"})
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"code":"SECRET","message":"SECRET"}}`))
		}
	})
	scan, err := c.Mailbox().ScanInbox(context.Background(), since, &until)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Messages) != 2 || scan.Messages[0].ID != "edge" || scan.Messages[1].ID != "second" || len(scan.Failures) != 5 {
		t.Fatalf("scan %+v", scan)
	}
	for i, f := range scan.Failures {
		expected := "Graph error InvalidMetadata"
		if i == 4 {
			expected = "Graph error RequestFailed"
		}
		if f.Error != expected {
			t.Fatalf("unsafe/wrong failure %+v", f)
		}
	}
	if _, err := c.Mailbox().ScanInbox(context.Background(), until, &since); err == nil {
		t.Fatal("inverted bounds accepted")
	}
}
func TestScanRejectsPaginationBeforeCredentialBearingRequest(t *testing.T) {
	for _, link := range []any{"https://evil.invalid/v1.0/me/mailFolders/inbox/messages", "/v1.0/me/mailFolders/inbox/messages?page=2", "PATH", "CYCLE", "", 17} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(strings.ReplaceAll(toString(link), "/", "_")), " ", "_"), func(t *testing.T) {
			var calls atomic.Int64
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				next := link
				if link == "PATH" {
					next = "http://" + r.Host + "/v1.0/me/messages?x=1"
				}
				if link == "CYCLE" {
					next = "http://" + r.Host + r.URL.RequestURI()
				}
				jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "keep", "receivedDateTime": "2026-09-21T01:00:00Z", "isDraft": false}}, "@odata.nextLink": next})
			})
			scan, err := c.Mailbox().ScanInbox(context.Background(), now, nil)
			if err != nil || len(scan.Messages) != 1 || len(scan.Failures) != 1 || scan.Failures[0].Error != "Graph error InvalidPage" || calls.Load() != 1 {
				t.Fatalf("invalid link not isolated %+v %v %d", scan, err, calls.Load())
			}
		})
	}
}
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "non-string"
}
