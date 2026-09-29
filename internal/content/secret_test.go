package content_test

import (
	"github.com/ivanrvpereira/agentmailguard-go/internal/content"
	"testing"
)

func TestMalformedHostAndPercentDecoding(t *testing.T) {
	for _, target := range []string{"https://[not-an-ipv6]/plain", "https://example／evil/plain", "https://example.test/a?x=%zz&token=secret"} {
		if !content.IsSecretURL(target) {
			t.Errorf("unsafe malformed URL accepted: %s", target)
		}
	}
	for _, target := range []string{"https://example.test/plain%zz", "https://benign%2eexample.test/plain"} {
		if content.IsSecretURL(target) {
			t.Errorf("benign percent URL rejected: %s", target)
		}
	}
}

func TestSecretURLs(t *testing.T) {
	for _, target := range []string{
		"https://acme.example/reset/abc123", "https://acme.example/account/verify?u=1", "https://acme.example/password-reset", "https://acme.example/a?token=xyz", "https://acme.example/a?x=1&code=123456", "https://acme.example/cb#access_token=abc", "https://login.microsoftonline.com/common/oauth2/authorize", "https://LOGIN.MICROSOFTONLINE.COM./x", "https://acme.okta.com/app/xyz", "https://acme.example/%72eset/abc", "https://acme.example/r?next=%253Ftoken%253Dabc", "https://acme.example/a?%74oken=1", "https://[not-an-ipv6/x", "https:///no-host", "https://%256cogin.microsoftonline.com/x",
	} {
		if !content.IsSecretURL(target) {
			t.Errorf("secret accepted: %s", target)
		}
	}
	for _, target := range []string{"https://dashboards.acme.example/q3", "https://news.example/2025/09/article?utm_source=mail", "https://acme.example/resetting-expectations-blog", "https://shop.example/products?id=42&color=red", "https://accounts.google.com.evil.example/x"} {
		if content.IsSecretURL(target) {
			t.Errorf("benign rejected: %s", target)
		}
	}
}
