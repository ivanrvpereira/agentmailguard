//go:build race

package e2e_test

// buildFlags builds the binary under test with the race detector, as the tests run with it. A binary with
// the race detector writes WARNING: DATA RACE to stderr and exits with code 66 when it found a race.
var buildFlags = []string{"-race"}
