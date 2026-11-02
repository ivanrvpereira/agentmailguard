package server

import "time"

// SetListReadDeadline sets the deadline of the live reads of the list page and returns a function that restores it.
// Tests of this package do not run in parallel.
func SetListReadDeadline(d time.Duration) (restore func()) {
	old := listReadDeadline
	listReadDeadline = d
	return func() { listReadDeadline = old }
}
