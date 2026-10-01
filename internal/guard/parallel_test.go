package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

type pendingScreen struct {
	id          string
	allow, done chan struct{}
}

type coordinatedJev struct {
	*screening.Jev
	entered             chan pendingScreen
	active, peak, calls atomic.Int32
}

func newCoordinatedJev(t *testing.T) *coordinatedJev {
	t.Helper()
	s := &coordinatedJev{entered: make(chan pendingScreen, 100)}
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			State struct {
				Subject string `json:"subject"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		p := pendingScreen{request.State.Subject, make(chan struct{}), make(chan struct{})}
		n := s.active.Add(1)
		s.calls.Add(1)
		for old := s.peak.Load(); n > old; old = s.peak.Load() {
			if s.peak.CompareAndSwap(old, n) {
				break
			}
		}
		defer func() { s.active.Add(-1); close(p.done) }()
		s.entered <- p
		select {
		case <-p.allow:
		case <-r.Context().Done():
			return
		case <-stop:
			return
		}
		index, err := strconv.Atoi(strings.TrimPrefix(p.id, "m"))
		if err != nil {
			t.Error(err)
			return
		}
		if index%5 == 4 {
			http.Error(w, "CANARY external response", http.StatusServiceUnavailable)
			return
		}
		injection, account := 0.0, 0.0
		switch index % 5 {
		case 1:
			injection = .35
		case 2:
			injection = .9
		case 3:
			account = .9
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev", "usage": map[string]any{}, "answers": map[string]any{
			"injection":        map[string]any{"type": "noul", "noul": injection},
			"account_security": map[string]any{"type": "noul", "noul": account},
			"folder":           map[string]any{"type": "choice", "choice": "route", "confidence": .9, "probabilities": map[string]float64{"route": .9, "stay": .1}},
			"category:work":    map[string]any{"type": "noul", "noul": .9},
		}})
	}))
	t.Cleanup(func() { close(stop); server.Close() })
	s.Jev = screening.New(screening.Options{Endpoint: server.URL, LookupEnv: func(string) string { return "synthetic" }})
	return s
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for independent work to enter or drain")
	}
	var zero T
	return zero
}

func (s *coordinatedJev) finishOutOfOrder(t *testing.T, count int) {
	t.Helper()
	first := make([]pendingScreen, 8)
	for i := range first {
		first[i] = receive(t, s.entered)
	}
	sort.Slice(first, func(i, j int) bool { return first[i].id < first[j].id })
	// Keep the earliest item blocked while later items complete and a ninth starts.
	for i := 7; i > 0; i-- {
		close(first[i].allow)
		receive(t, first[i].done)
	}
	next := receive(t, s.entered)
	close(next.allow)
	receive(t, next.done)
	close(first[0].allow)
	receive(t, first[0].done)
	for range count - 9 {
		p := receive(t, s.entered)
		close(p.allow)
		receive(t, p.done)
	}
}

func batchIDs() []string {
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%02d", i)
	}
	return ids
}

func TestClassifyParallelJevPreservesOrderedIndependentOutcomes(t *testing.T) {
	s := newCoordinatedJev(t)
	g, _, path := setup(t, concurrentMailbox{}, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		items []guard.Classified
		err   error
	}
	done := make(chan result, 1)
	ids := batchIDs()
	go func() { items, err := g.Classify(ctx, ids, orgRules()); done <- result{items, err} }()
	s.finishOutOfOrder(t, len(ids))
	got := receive(t, done)
	if got.err != nil || len(got.items) != len(ids) {
		t.Fatalf("classification: count=%d error=%v", len(got.items), got.err)
	}
	verdicts := []model.Verdict{model.Green, model.Yellow, model.Red, model.Sealed, model.Error}
	for i, item := range got.items {
		if item.ID != ids[i] || item.Row == nil || item.Row.Verdict != verdicts[i%5] || !item.Screened || item.Classified != (i%5 != 4) {
			t.Fatalf("item %d lost its ordered independent outcome: %+v", i, item)
		}
		if i%5 == 4 && (item.Error == nil || *item.Error != "TypeSafeAPIError 503") {
			t.Fatalf("unsafe failure: %+v", item)
		}
	}
	if s.peak.Load() != 8 || s.active.Load() != 0 || s.calls.Load() != 40 {
		t.Fatalf("peak=%d active=%d calls=%d; want 8, 0, 40", s.peak.Load(), s.active.Load(), s.calls.Load())
	}
	if len(auditRecords(t, path)) != 0 {
		t.Fatal("classification audited a mutation")
	}
}

func TestMailParallelJevPreservesOrderedIndependentOutcomes(t *testing.T) {
	for _, surface := range []string{"List", "Search"} {
		t.Run(surface, func(t *testing.T) {
			s := newCoordinatedJev(t)
			b := &mailbox{}
			ids := batchIDs()
			for _, id := range ids {
				b.messages = append(b.messages, message(id))
			}
			g, _, path := setup(t, b, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				items []views.MailSummary
				err   error
			}
			done := make(chan result, 1)
			go func() {
				var items []views.MailSummary
				var err error
				if surface == "List" {
					items, err = g.List(ctx, "inbox", 40, 0)
				} else {
					items, err = g.Search(ctx, model.SearchQuery{Text: ptr("synthetic")}, 40)
				}
				done <- result{items, err}
			}()
			s.finishOutOfOrder(t, len(ids))
			got := receive(t, done)
			if got.err != nil || len(got.items) != len(ids) {
				t.Fatalf("read: count=%d error=%v", len(got.items), got.err)
			}
			verdicts := []model.Verdict{model.Green, model.Yellow, model.Red, model.Sealed, model.Error}
			for i, item := range got.items {
				if item.ID != ids[i] || item.Verdict != verdicts[i%5] {
					t.Fatalf("item %d lost ordered outcome: %+v", i, item)
				}
				if i%5 >= 2 && item.Subject != nil {
					t.Fatal("hidden subject exposed")
				}
			}
			if s.peak.Load() != 8 || s.calls.Load() != 40 || s.active.Load() != 0 {
				t.Fatalf("peak=%d calls=%d active=%d", s.peak.Load(), s.calls.Load(), s.active.Load())
			}
			if len(b.gets) != 1 || !b.gets[0].body || len(auditRecords(t, path)) != 0 {
				t.Fatal("read repeated fetch or wrote audit")
			}
		})
	}
}

type batchCalendar struct {
	model.Calendar
	events []model.Event
}

func (c batchCalendar) List(context.Context, time.Time, time.Time) ([]model.Event, error) {
	return c.events, nil
}

func TestCalendarParallelJevPreservesOrderedIndependentOutcomes(t *testing.T) {
	s := newCoordinatedJev(t)
	b := &mailbox{}
	_, c, path := setup(t, b, s)
	cal := batchCalendar{}
	ids := batchIDs()
	for _, id := range ids {
		cal.events = append(cal.events, model.Event{ID: id, Subject: id, Start: now, End: now.Add(time.Hour), Body: ptr("synthetic"), ContentType: ptr(model.Text)})
	}
	g := guard.New("work", b, cal, s, c, audit.New(path), guard.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		items []views.CalendarView
		err   error
	}
	done := make(chan result, 1)
	go func() { items, err := g.CalendarList(ctx, now, now.Add(time.Hour)); done <- result{items, err} }()
	s.finishOutOfOrder(t, len(ids))
	got := receive(t, done)
	if got.err != nil || len(got.items) != len(ids) {
		t.Fatalf("calendar: count=%d error=%v", len(got.items), got.err)
	}
	verdicts := []model.Verdict{model.Green, model.Yellow, model.Red, model.Sealed, model.Error}
	for i, item := range got.items {
		if item.ID != ids[i] || item.Verdict != verdicts[i%5] {
			t.Fatalf("item %d lost ordered outcome: %+v", i, item)
		}
		if i%5 != 0 && item.Body != nil {
			t.Fatal("hidden calendar body exposed")
		}
	}
	if s.peak.Load() != 8 || s.calls.Load() != 40 || s.active.Load() != 0 {
		t.Fatalf("peak=%d calls=%d active=%d", s.peak.Load(), s.calls.Load(), s.active.Load())
	}
	if len(auditRecords(t, path)) != 0 {
		t.Fatal("calendar wrote audit")
	}
}

type blockedFetchBox struct {
	concurrentMailbox
	entered chan struct{}
}

func (b blockedFetchBox) Get(ctx context.Context, _ []string, _ bool) ([]model.Message, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestClassificationBoundsProviderWorkBeforeJevAdmission(t *testing.T) {
	b := blockedFetchBox{entered: make(chan struct{}, 40)}
	g, _, _ := setup(t, b, concurrentScreener{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.Classify(ctx, batchIDs(), orgRules()); done <- err }()
	for range 8 {
		receive(t, b.entered)
	}
	select {
	case <-b.entered:
		t.Fatal("more than eight items started provider work before Jev admission")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked provider work did not cancel/drain: %v", err)
	}
	select {
	case <-b.entered:
		t.Fatal("queued provider work started after cancellation")
	default:
	}
}

func TestParallelJevCancellationDrainsAndStopsQueuedItems(t *testing.T) {
	for _, surface := range []string{"Classify", "List", "Search", "CalendarList"} {
		t.Run(surface, func(t *testing.T) {
			s := newCoordinatedJev(t)
			b := &mailbox{}
			cal := batchCalendar{}
			for _, id := range batchIDs() {
				b.messages = append(b.messages, message(id))
				cal.events = append(cal.events, model.Event{ID: id, Subject: id, Start: now, End: now.Add(time.Hour), Body: ptr("synthetic"), ContentType: ptr(model.Text)})
			}
			_, c, path := setup(t, b, s)
			g := guard.New("work", b, cal, s, c, audit.New(path), guard.Options{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch surface {
				case "Classify":
					_, err = g.Classify(ctx, batchIDs(), orgRules())
				case "List":
					_, err = g.List(ctx, "inbox", 40, 0)
				case "Search":
					_, err = g.Search(ctx, model.SearchQuery{Text: ptr("synthetic")}, 40)
				case "CalendarList":
					_, err = g.CalendarList(ctx, now, now.Add(time.Hour))
				}
				done <- err
			}()
			pending := make([]pendingScreen, 8)
			for i := range pending {
				pending[i] = receive(t, s.entered)
			}
			cancel()
			if err := receive(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled batch: %v", err)
			}
			for _, p := range pending {
				receive(t, p.done)
			}
			if s.active.Load() != 0 || s.calls.Load() != 8 {
				t.Fatalf("cancelled batch did not drain/stop: active=%d calls=%d", s.active.Load(), s.calls.Load())
			}
			if len(auditRecords(t, path)) != 0 {
				t.Fatal("cancellation audited a mutation")
			}
		})
	}
}
