package m365

// LoginError retains the provider's fixed error code. Message exposes only a
// locally defined login stage, never a wrapped error or provider response.
type LoginError uint8

const (
	loginTokenExchange LoginError = iota + 1
	loginProfile
	loginCredentialSave
	loginInvalidToken
	loginBrowserStart
	loginAuthorization
	loginDeviceRequest
	loginDeviceAuthorization
	loginDevicePolling
	loginTimeout
	loginCanceled
)

func (LoginError) Error() string { return string(authError) }

func (e LoginError) Message() string {
	switch e {
	case loginTokenExchange:
		return "provider login failed: token exchange failed"
	case loginProfile:
		return "provider login failed: profile lookup failed"
	case loginCredentialSave:
		return "provider login failed: Keychain credential save failed"
	case loginInvalidToken:
		return "provider login failed: token validation failed"
	case loginBrowserStart:
		return "provider login failed: could not start browser authentication"
	case loginAuthorization:
		return "provider login failed: authorization response failed; check Microsoft sign-in logs"
	case loginDeviceRequest:
		return "provider login failed: device-code request failed"
	case loginDeviceAuthorization:
		return "provider login failed: device-code authorization refused; organizations can block device code, so sign in with --no-browser instead, or check Microsoft sign-in logs"
	case loginDevicePolling:
		return "provider login failed: device-code polling failed"
	case loginTimeout:
		return "provider login failed: login timed out"
	case loginCanceled:
		return "provider login failed: login canceled"
	default:
		return "provider login failed"
	}
}
