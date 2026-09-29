//go:build !darwin || !cgo

package credentials

import "github.com/ivanrvpereira/agentmailguard-go/internal/config"

// NewMacOS refuses unsupported platforms. There is no plaintext fallback.
func NewMacOS(string, []config.Account) (Store, error) { return nil, ErrUnavailable }
