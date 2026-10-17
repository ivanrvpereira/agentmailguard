//go:build !race

package e2e_test

// buildFlags builds the binary under test without the race detector, as the tests run without it.
var buildFlags = []string{}
