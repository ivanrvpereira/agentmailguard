package server

import "time"

// SetListReadDeadline sets the deadline of the live reads of the list page and returns a function that restores it.
// Tests of this package do not run in parallel.
func SetListReadDeadline(d time.Duration) (restore func()) {
	old := listReadDeadline
	listReadDeadline = d
	return func() { listReadDeadline = old }
}

// SetRefreshSeconds sets the interval of the Auto-refresh choice value, such as "5m", and returns a function that
// restores it.
func SetRefreshSeconds(value string, seconds int) (restore func()) {
	for i := range refreshChoices {
		if refreshChoices[i].Value == value {
			old := refreshChoices[i].Seconds
			refreshChoices[i].Seconds = seconds
			return func() { refreshChoices[i].Seconds = old }
		}
	}
	panic("no refresh choice " + value)
}
