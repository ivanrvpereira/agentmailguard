package server_test

import (
	"net/http"
	"path"
	"testing"
	"time"
)

// A reload builds new guards, but the writes of one account stay one at a time: a write that starts after the
// reload waits for a write that started before it. A write of another account, started later, still finishes
// meanwhile, so the wait is long enough to see the second write if it were not held.
func TestWritesOfAnAccountStayOneAtATimeAcrossAReload(t *testing.T) {
	const bound = 30 * time.Second
	held, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// The Graph API holds the write of message held until release closes, and reports the write of message second.
	graph := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch path.Base(r.URL.Path) {
		case "held":
			close(held)
			<-release
		case "second":
			close(second)
		}
		response(w, map[string]any{"id": "synthetic"})
	})
	d := graphDaemon(t, "", m365Grant, graph, nil)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	write := func(account, id string) <-chan int {
		done := make(chan int, 1)
		go func() { code, _ := accountRPC(t, d.s, "mail.read", account, `{"ids":["`+id+`"]}`); done <- code }()
		return done
	}
	await := func(what string, c <-chan struct{}) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(bound):
			t.Fatalf("%s did not happen", what)
		}
	}
	finished := func(what string, done <-chan int) {
		t.Helper()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Fatalf("%s: HTTP %d", what, code)
			}
		case <-time.After(bound):
			t.Fatalf("%s did not finish", what)
		}
	}
	first := write("work", "held")
	await("the first write of work", held)
	d.reload(t)
	next := write("work", "second")
	control := write("other", "control")
	finished("the write of other", control)
	select {
	case <-second:
		t.Fatal("a write after the reload ran while a write from before the reload held the account")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	await("the second write of work", second)
	finished("the first write of work", first)
	finished("the second write of work", next)
}
