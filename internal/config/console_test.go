package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

func TestConsoleDefaultsWithoutTable(t *testing.T) {
	got, err := parse(t, head+work)
	if err != nil || !reflect.DeepEqual(got.Console, config.Console{Listen: "localhost:8788", Username: "admin"}) {
		t.Fatalf("console=%+v error=%v", got.Console, err)
	}
}

func TestConsoleReadsListenAndHosts(t *testing.T) {
	c, err := parse(t, head+"[console]\nlisten = '100.64.0.1:9000'\nhosts = ['my-mac.example.ts.net', 'my-mac.example.ts.net:9000', '100.64.0.1', '[fd7a::1]:9000', '[fd7a::1]']\n"+work)
	got := c.Console
	want := config.Console{Listen: "100.64.0.1:9000", Hosts: []string{"my-mac.example.ts.net", "my-mac.example.ts.net:9000", "100.64.0.1", "[fd7a::1]:9000", "[fd7a::1]"}, Username: "admin"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("console=%+v error=%v", got, err)
	}
	c, err = parse(t, head+"[console]\nhosts = []\n"+work)
	if err != nil || c.Console.Listen != "localhost:8788" || len(c.Console.Hosts) != 0 {
		t.Fatalf("console=%+v error=%v", c.Console, err)
	}
}

// The console username is 1 to 64 letters, digits, and . _ - @. HTTP Basic authentication cannot carry a colon in it.
func TestConsoleReadsUsername(t *testing.T) {
	for _, user := range []string{"admin", "Admin.Ops_2-x@example.test", "a", strings.Repeat("u", 64)} {
		c, err := parse(t, head+"[console]\nusername = '"+user+"'\n"+work)
		if err != nil || c.Console.Username != user {
			t.Errorf("%q: console=%+v error=%v", user, c.Console, err)
		}
	}
}

func TestInvalidConsoleNamesKeyPathWithoutValue(t *testing.T) {
	for _, tc := range []struct{ name, text, path string }{
		{"not a table", "console = 'SECRET-CANARY'\n", "console"},
		{"unknown key", "[console]\nlisten_on = 'SECRET-CANARY'\n", "console.listen_on"},
		{"odd key", "[console]\n\"a.b\\u001b\" = 'SECRET-CANARY'\n", `console."a.b\x1b"`},
		{"listen not a string", "[console]\nlisten = 8788\n", "console.listen"},
		{"listen without port", "[console]\nlisten = 'SECRET-CANARY'\n", "console.listen"},
		{"listen empty host", "[console]\nlisten = ':8788'\n", "console.listen"},
		{"listen port zero", "[console]\nlisten = 'localhost:0'\n", "console.listen"},
		{"listen port too big", "[console]\nlisten = 'localhost:65536'\n", "console.listen"},
		{"listen named port", "[console]\nlisten = 'localhost:http'\n", "console.listen"},
		{"listen odd host", "[console]\nlisten = 'SECRET/CANARY:8788'\n", "console.listen"},
		{"hosts not an array", "[console]\nhosts = 'SECRET-CANARY'\n", "console.hosts"},
		{"hosts empty entry", "[console]\nhosts = ['ok.example', '']\n", "console.hosts"},
		{"hosts not a string", "[console]\nhosts = [1]\n", "console.hosts"},
		{"hosts odd characters", "[console]\nhosts = ['SECRET-CANARY/evil']\n", "console.hosts"},
		{"hosts bad port", "[console]\nhosts = ['SECRET-CANARY:99999']\n", "console.hosts"},
		{"hosts empty port", "[console]\nhosts = ['SECRET-CANARY:']\n", "console.hosts"},
		{"hosts IPv6 without brackets", "[console]\nhosts = ['fd7a::1']\n", "console.hosts"},
		{"hosts name in brackets", "[console]\nhosts = ['[SECRET-CANARY]:9000']\n", "console.hosts"},
		{"username not a string", "[console]\nusername = 1\n", "console.username"},
		{"username empty", "[console]\nusername = ''\n", "console.username"},
		{"username colon", "[console]\nusername = 'SECRET:CANARY'\n", "console.username"},
		{"username space", "[console]\nusername = 'SECRET CANARY'\n", "console.username"},
		{"username control", "[console]\nusername = \"SECRET\\u001bCANARY\"\n", "console.username"},
		{"username not ASCII", "[console]\nusername = 'SECRET-CANÁRY'\n", "console.username"},
		{"username too long", "[console]\nusername = '" + strings.Repeat("S", 65) + "'\n", "console.username"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, head+tc.text+work)
			refusal(t, err, tc.path)
		})
	}
}
