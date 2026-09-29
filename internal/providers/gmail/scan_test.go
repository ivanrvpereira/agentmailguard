package gmail_test

import (
	"context"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestScanCollectsPagesBeforeExactMetadataAndRetainsFailures(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 1000000, time.UTC)
	until := since.Add(time.Second)
	var mu sync.Mutex
	pages := 0
	dates := map[string]string{"lower": "1788220800001", "old": "1788220800000", "upper": "1788220801001", "draft": "1788220800002", "bad": "1e12"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" {
			mu.Lock()
			pages++
			mu.Unlock()
			q := r.URL.Query()
			if q.Get("q") != "after:1788134400 before:1788307201" || q.Get("labelIds") != "INBOX" {
				t.Error("scan margin")
			}
			if q.Get("pageToken") == "" {
				respond(w, map[string]any{"messages": []any{map[string]string{"id": "lower"}, map[string]string{"id": "old"}, nil}, "nextPageToken": "next"})
				return
			}
			respond(w, map[string]any{"messages": []any{map[string]string{"id": "upper"}, map[string]string{"id": "draft"}, map[string]string{"id": "bad"}}, "nextPageToken": "next"})
			return
		}
		mu.Lock()
		if pages != 2 {
			t.Error("metadata before all pages")
		}
		mu.Unlock()
		if r.URL.Query().Encode() != "fields=id%2ClabelIds%2CinternalDate&format=minimal" {
			t.Error("excess scan fields")
		}
		id := r.URL.Path[len("/messages/"):]
		labels := []string{"INBOX"}
		if id == "draft" {
			labels = append(labels, "DRAFT")
		}
		respond(w, map[string]any{"id": id, "labelIds": labels, "internalDate": dates[id]})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	scan, err := c.Mailbox().ScanInbox(context.Background(), since, &until)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Messages) != 1 || scan.Messages[0].ID != "lower" || !scan.Messages[0].ReceivedAt.Equal(since) || len(scan.Failures) != 3 {
		t.Fatalf("scan: %+v", scan)
	}
	if scan.Failures[0].ID != nil || scan.Failures[1].Error != "Gmail error InvalidPage" || scan.Failures[2].ID == nil || *scan.Failures[2].ID != "bad" {
		t.Fatalf("isolated failures: %+v", scan.Failures)
	}
}
func TestScanYearOneBoundsAndPartialPageFailure(t *testing.T) {
	since := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages" {
			if r.URL.Query().Get("q") != "after:-62135683200 before:-62135424000" {
				t.Error("year one margin")
			}
			if r.URL.Query().Get("pageToken") != "" {
				w.WriteHeader(400)
				respond(w, map[string]any{"error": map[string]string{"message": "SECRET"}})
				return
			}
			respond(w, map[string]any{"messages": []any{map[string]string{"id": "edge"}}, "nextPageToken": "next"})
			return
		}
		respond(w, map[string]any{"id": "edge", "labelIds": []string{"INBOX"}, "internalDate": strconv.FormatInt(since.UnixMilli(), 10)})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	scan, err := c.Mailbox().ScanInbox(context.Background(), since, &until)
	if err != nil || len(scan.Messages) != 1 || len(scan.Failures) != 1 || !reflect.DeepEqual(scan.Messages[0].ReceivedAt, since) {
		t.Fatalf("year one: %+v %v", scan, err)
	}
	if scan.Failures[0].Error != "Gmail error RequestFailed" {
		t.Fatal("page error leaks")
	}
}
