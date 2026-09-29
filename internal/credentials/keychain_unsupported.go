//go:build !darwin || !cgo

package credentials

// NewMacOS refuses unsupported platforms. There is no plaintext fallback.
func NewMacOS() (Store, error) { return nil, ErrUnavailable }
