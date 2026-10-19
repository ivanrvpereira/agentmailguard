package server_test

import (
	"os"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// TestMain keeps scrypt cheap: at the production work factor, the race detector makes each password change take seconds.
func TestMain(m *testing.M) {
	credentials.FileWorkFactor = 1
	os.Exit(m.Run())
}
