package content

import (
	"net/url"
	"regexp"
	"strings"
)

// Patterns ported verbatim from content/secret_url_patterns.toml (host patterns anchored).
var secretPath = regexp.MustCompile(`(?i)/(?:reset|password|passwd|verify|verification|confirm|activate|activation|magic|invite|recover|unlock|otp|sso|auth)(?:[/._-]|$)`)
var secretQuery = regexp.MustCompile(`(?i)(?:^|[?&#])(?:(?:access_|id_|refresh_)?token|code|otp|key|secret|signature|sig|ticket|auth|session|sid|hash|verifier)=`)
var secretHost = regexp.MustCompile(`(?i)^(?:(?:accounts\.google\.com|login\.microsoftonline\.com|login\.live\.com|login\.microsoft\.com|appleid\.apple\.com|id\.atlassian\.com|login\.salesforce\.com|signin\.aws\.amazon\.com)|[a-z0-9.-]{1,200}\.(?:okta\.com|oktapreview\.com|auth0\.com|onelogin\.com|duosecurity\.com|pingidentity\.com))$`)

func decode(s string) string {
	for range 10 {
		// Decode valid escapes even when another escape is malformed, like urllib.unquote.
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '%' && i+2 < len(s) {
				if decoded, err := url.PathUnescape(s[i : i+3]); err == nil {
					b.WriteString(decoded)
					i += 2
					continue
				}
			}
			b.WriteByte(s[i])
		}
		next := b.String()
		if next == s {
			break
		}
		s = next
	}
	return s
}

// IsSecretURL refuses likely account-access links. Parse failures and missing hosts are secret.
func IsSecretURL(target string) bool {
	u, err := splitURL(target)
	if err != nil {
		return true
	}
	host := strings.Trim(decode(u.Hostname()), ".")
	if host == "" {
		return true
	}
	return secretHost.MatchString(host) || secretPath.MatchString(decode(u.Path)) || secretQuery.MatchString(decode(u.RawQuery+"#"+u.Fragment))
}
