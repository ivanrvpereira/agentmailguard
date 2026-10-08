package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
)

var queued = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

const trashArgs = `{"ids":["m1","m2"],"body":"Hi Ana, see you tomorrow"}`

func TestPendingIDsAreDistinct32CharLowercaseHex(t *testing.T) {
	c, _ := open(t)
	format := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for range 50 {
		id, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(`{}`), queued)
		if err != nil || !format.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated ID %q: %v", id, err)
		}
		seen[id] = true
	}
}

func TestInvalidJSONArgsAreRefused(t *testing.T) {
	c, path := open(t)
	for _, args := range []string{``, `{`, `{"body":"RAW CONTENT CANARY"`, `RAW CONTENT CANARY`, `{} {}`} {
		if id, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(args), queued); err == nil {
			t.Errorf("invalid args %q stored as %s", args, id)
		} else if strings.Contains(err.Error(), "CANARY") {
			t.Errorf("error echoes args: %v", err)
		}
	}
	bytes, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(bytes), "CANARY") {
		t.Fatal("invalid args persisted")
	}
}

func TestAddedPendingActionReadsBackAfterReopen(t *testing.T) {
	c, path := open(t)
	id, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	want := cache.Pending{ID: id, Account: "work", Op: "mail.trash", Args: json.RawMessage(trashArgs), State: cache.StatePending, Created: queued, Updated: queued}
	got, err := c.GetPending(ctx, id)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v error=%v", got, err)
	}
	reopened, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.GetPending(ctx, id)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("pending action lost on reopen: got=%+v error=%v", got, err)
	}
}

func TestClaimMovesPendingToRunningOnce(t *testing.T) {
	c, _ := open(t)
	id, err := c.AddPending(ctx, "work", "draft.reply", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	claimed := queued.Add(time.Minute)
	want := cache.Pending{ID: id, Account: "work", Op: "draft.reply", Args: json.RawMessage(trashArgs), State: cache.StateRunning, Created: queued, Updated: claimed}
	got, err := c.ClaimPending(ctx, id, claimed)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("claim got=%+v error=%v", got, err)
	}
	if _, err := c.ClaimPending(ctx, id, claimed.Add(time.Minute)); !errors.Is(err, cache.ErrPendingHandled) {
		t.Fatalf("second claim: %v", err)
	}
	if got, err := c.GetPending(ctx, id); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("second claim changed item: %+v %v", got, err)
	}
	for _, missing := range []string{"0123456789abcdef0123456789abcdef", ""} {
		if _, err := c.ClaimPending(ctx, missing, claimed); !errors.Is(err, cache.ErrPendingNotFound) {
			t.Errorf("claim of missing ID: %v", err)
		}
		if _, err := c.GetPending(ctx, missing); !errors.Is(err, cache.ErrPendingNotFound) {
			t.Errorf("read of missing ID: %v", err)
		}
	}
}

func TestConcurrentClaimsHaveExactlyOneWinner(t *testing.T) {
	first, path := open(t)
	second, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	id, err := first.AddPending(ctx, "work", "mail.trash", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	var winners, handled atomic.Int32
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Go(func() {
			_, err := []*cache.Cache{first, second}[i%2].ClaimPending(ctx, id, queued)
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, cache.ErrPendingHandled):
				handled.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if winners.Load() != 1 || handled.Load() != 15 {
		t.Fatalf("winners=%d handled=%d", winners.Load(), handled.Load())
	}
}

func TestDenyOnlyFromPending(t *testing.T) {
	c, _ := open(t)
	waiting, err := c.AddPending(ctx, "work", "calendar.update", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	running, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(`{}`), queued)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimPending(ctx, running, queued); err != nil {
		t.Fatal(err)
	}
	denied := queued.Add(time.Hour)
	want := cache.Pending{ID: waiting, Account: "work", Op: "calendar.update", Args: json.RawMessage(trashArgs), State: cache.StateDenied, Created: queued, Updated: denied}
	if got, err := c.DenyPending(ctx, waiting, denied); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deny returned=%+v error=%v", got, err)
	}
	if got, err := c.GetPending(ctx, waiting); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deny got=%+v error=%v", got, err)
	}
	for _, id := range []string{waiting, running} {
		if _, err := c.DenyPending(ctx, id, denied.Add(time.Hour)); !errors.Is(err, cache.ErrPendingHandled) {
			t.Errorf("deny of handled item: %v", err)
		}
	}
	if _, err := c.ClaimPending(ctx, waiting, denied); !errors.Is(err, cache.ErrPendingHandled) {
		t.Errorf("claim of denied item: %v", err)
	}
	if got, err := c.GetPending(ctx, running); err != nil || got.State != cache.StateRunning {
		t.Errorf("deny changed running item: %+v %v", got, err)
	}
	if _, err := c.DenyPending(ctx, "0123456789abcdef0123456789abcdef", denied); !errors.Is(err, cache.ErrPendingNotFound) {
		t.Errorf("deny of missing ID: %v", err)
	}
}

func TestFinishOnlyFromRunningToDoneOrFailed(t *testing.T) {
	c, _ := open(t)
	add := func(claim bool) string {
		t.Helper()
		id, err := c.AddPending(ctx, "work", "draft.reply", json.RawMessage(trashArgs), queued)
		if err != nil {
			t.Fatal(err)
		}
		if claim {
			if _, err := c.ClaimPending(ctx, id, queued); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	finished := queued.Add(2 * time.Minute)
	for state, result := range map[cache.PendingState]string{cache.StateDone: `{"draft_id":"AAMkAD="}`, cache.StateFailed: "Graph error 404 ErrorItemNotFound"} {
		id := add(true)
		if err := c.FinishPending(ctx, id, state, result, finished); err != nil {
			t.Fatal(err)
		}
		want := cache.Pending{ID: id, Account: "work", Op: "draft.reply", Args: json.RawMessage(trashArgs), State: state, Result: &result, Created: queued, Updated: finished}
		if got, err := c.GetPending(ctx, id); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("finish %s got=%+v error=%v", state, got, err)
		}
		if err := c.FinishPending(ctx, id, cache.StateFailed, "TimeoutError", finished); !errors.Is(err, cache.ErrPendingHandled) {
			t.Errorf("second finish of %s item: %v", state, err)
		}
	}

	running := add(true)
	for _, target := range []cache.PendingState{cache.StatePending, cache.StateRunning, cache.StateDenied, "purple"} {
		if err := c.FinishPending(ctx, running, target, "x", finished); err == nil {
			t.Errorf("finish to %q accepted", target)
		}
	}
	if err := c.FinishPending(ctx, running, cache.StateFailed, "raw provider text RAW CONTENT CANARY", finished); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Errorf("unsafe failure text: %v", err)
	}
	if got, err := c.GetPending(ctx, running); err != nil || got.State != cache.StateRunning || got.Result != nil || !got.Updated.Equal(queued) {
		t.Errorf("refused finish changed item: %+v %v", got, err)
	}

	waiting := add(false)
	if err := c.FinishPending(ctx, waiting, cache.StateDone, "x", finished); !errors.Is(err, cache.ErrPendingHandled) {
		t.Errorf("finish of unclaimed item: %v", err)
	}
	if got, err := c.GetPending(ctx, waiting); err != nil || got.State != cache.StatePending {
		t.Errorf("finish changed unclaimed item: %+v %v", got, err)
	}
	if err := c.FinishPending(ctx, "0123456789abcdef0123456789abcdef", cache.StateDone, "x", finished); !errors.Is(err, cache.ErrPendingNotFound) {
		t.Errorf("finish of missing ID: %v", err)
	}
}

func TestCacheClearKeepsPendingActions(t *testing.T) {
	c, _ := open(t)
	if err := c.Put(ctx, "work", row("m1")); err != nil {
		t.Fatal(err)
	}
	id, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := c.Clear(ctx); err != nil || count != 1 {
		t.Fatalf("clear=%d error=%v", count, err)
	}
	if got, err := c.GetPending(ctx, id); err != nil || got.State != cache.StatePending || string(got.Args) != trashArgs {
		t.Fatalf("clear removed pending action: %+v %v", got, err)
	}
}

// Callers tell cancellation apart from storage failures, which get fixed text.
func TestCancelledPendingIOReturnsCancellation(t *testing.T) {
	c, _ := open(t)
	id, err := c.AddPending(ctx, "work", "mail.trash", json.RawMessage(trashArgs), queued)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, operation := range []func() error{
		func() error {
			_, err := c.AddPending(cancelled, "work", "mail.trash", json.RawMessage(`{}`), queued)
			return err
		},
		func() error { _, err := c.GetPending(cancelled, id); return err },
		func() error { _, err := c.ClaimPending(cancelled, id, queued); return err },
		func() error { _, err := c.DenyPending(cancelled, id, queued); return err },
		func() error { return c.FinishPending(cancelled, id, cache.StateDone, "x", queued) },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("expected cancellation, got %v", err)
		}
	}
}
