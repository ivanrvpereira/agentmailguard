package config_test

import (
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

func screeningOf(t *testing.T, text string) (config.Screening, error) {
	t.Helper()
	c, err := parse(t, head+text)
	return c.Screening, err
}

func TestScreeningDefaultsToJevWithoutTable(t *testing.T) {
	got, err := screeningOf(t, work)
	if err != nil || got != (config.Screening{Provider: "jev"}) {
		t.Fatalf("screening=%+v error=%v", got, err)
	}
}

func TestScreeningReadsProviderAndEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://jev.example.test/v1/systemone", "https://jev.example.test:8443/v1/systemone?x=1", "https://[::1]:8443/v1/systemone"} {
		got, err := screeningOf(t, "[screening]\nprovider = 'jev'\nendpoint = '"+endpoint+"'\n"+work)
		if err != nil || got != (config.Screening{Provider: "jev", Endpoint: endpoint}) {
			t.Fatalf("screening=%+v error=%v", got, err)
		}
	}
	got, err := screeningOf(t, "[screening]\nprovider = 'fake'\n"+work)
	if err != nil || got != (config.Screening{Provider: "fake"}) {
		t.Fatalf("screening=%+v error=%v", got, err)
	}
}

func TestScreeningReadsModel(t *testing.T) {
	got, err := screeningOf(t, "[screening]\nendpoint = 'https://api.typesafe.ai/v1/systemone'\nmodel = 'jev-1.13.0'\n"+work)
	if err != nil || got != (config.Screening{Provider: "jev", Endpoint: "https://api.typesafe.ai/v1/systemone", Model: "jev-1.13.0"}) {
		t.Fatalf("screening=%+v error=%v", got, err)
	}
	got, err = screeningOf(t, "[screening]\nmodel = 'typesafe/jev-1.13'\n"+work)
	if err != nil || got != (config.Screening{Provider: "jev", Model: "typesafe/jev-1.13"}) {
		t.Fatalf("screening=%+v error=%v", got, err)
	}
}

func TestInvalidScreeningNamesKeyPathWithoutValue(t *testing.T) {
	for _, tc := range []struct{ name, text, path string }{
		{"not a table", "screening = 'SECRET-CANARY'\n", "screening"},
		{"unknown key", "[screening]\nkey = 'SECRET-CANARY'\n", "screening.key"},
		{"unknown provider", "[screening]\nprovider = 'SECRET-CANARY'\n", "screening.provider"},
		{"provider not a string", "[screening]\nprovider = 1\n", "screening.provider"},
		{"plain http", "[screening]\nendpoint = 'http://localhost/SECRET-CANARY'\n", "screening.endpoint"},
		{"no host", "[screening]\nendpoint = 'https:///SECRET-CANARY'\n", "screening.endpoint"},
		{"user info", "[screening]\nendpoint = 'https://user:SECRET-CANARY@jev.example.test/'\n", "screening.endpoint"},
		{"not a URL", "[screening]\nendpoint = 'SECRET-CANARY'\n", "screening.endpoint"},
		{"no host name with port", "[screening]\nendpoint = 'https://:443/SECRET-CANARY'\n", "screening.endpoint"},
		{"port zero", "[screening]\nendpoint = 'https://jev.example.test:0/SECRET-CANARY'\n", "screening.endpoint"},
		{"port too large", "[screening]\nendpoint = 'https://jev.example.test:99999/SECRET-CANARY'\n", "screening.endpoint"},
		{"empty port", "[screening]\nendpoint = 'https://jev.example.test:/SECRET-CANARY'\n", "screening.endpoint"},
		{"colon in host name", "[screening]\nendpoint = 'https://jev.example.test:443:1/SECRET-CANARY'\n", "screening.endpoint"},
		{"fragment", "[screening]\nendpoint = 'https://jev.example.test/#SECRET-CANARY'\n", "screening.endpoint"},
		{"model not a string", "[screening]\nmodel = 1\n", "screening.model"},
		{"empty model", "[screening]\nmodel = ''\n", "screening.model"},
		{"model with space", "[screening]\nmodel = 'SECRET CANARY'\n", "screening.model"},
		{"model with tab", "[screening]\nmodel = \"SECRET-CANARY\\tx\"\n", "screening.model"},
		{"model with newline", "[screening]\nmodel = \"SECRET-CANARY\\u000A\"\n", "screening.model"},
		{"model too long", "[screening]\nmodel = 'SECRET-CANARY" + strings.Repeat("a", 116) + "'\n", "screening.model"},
		{"model with other character", "[screening]\nmodel = 'SECRET-CANARY@jev'\n", "screening.model"},
		{"model with leading slash", "[screening]\nmodel = '/SECRET-CANARY'\n", "screening.model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := screeningOf(t, tc.text+work)
			refusal(t, err, tc.path)
		})
	}
}
