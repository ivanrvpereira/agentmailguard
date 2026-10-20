package content_test

import (
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestPythonReference(t *testing.T) {
	data, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Content []struct {
			Name, Kind, Input, Markdown string
			Links, Domains              map[string]string
			Truncated                   bool
		}
		Subjects []struct{ Input, Output string }
	}
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	for _, c := range oracle.Content {
		t.Run(c.Name, func(t *testing.T) {
			r, err := content.RenderBody(&c.Input, model.ContentType(c.Kind))
			if err != nil {
				t.Fatal(err)
			}
			if r.Markdown != c.Markdown || !reflect.DeepEqual(r.Links, c.Links) || !reflect.DeepEqual(r.Domains(), c.Domains) || r.Truncated != c.Truncated {
				t.Fatalf("got %q %v %v %v; want %q %v %v %v", r.Markdown, r.Links, r.Domains(), r.Truncated, c.Markdown, c.Links, c.Domains, c.Truncated)
			}
			if strings.Contains(r.Markdown, "CANARY") || strings.Contains(r.Markdown, "<") || strings.Contains(r.Markdown, "://") || utf8.RuneCountInString(r.Markdown) > content.MaxChars {
				t.Fatal("leak gate failed")
			}
			for line := range strings.SplitSeq(r.Markdown, "\n") {
				if strings.Contains(strings.TrimLeft(line, "> "), ">") {
					t.Fatal("raw angle bracket")
				}
			}
			markers := map[string]bool{}
			for _, match := range regexp.MustCompile(`\[(L\d+)\]`).FindAllStringSubmatch(r.Markdown, -1) {
				markers[match[1]] = true
			}
			if len(markers) != len(r.Links) {
				t.Fatal("link marker/registry mismatch")
			}
			for id := range markers {
				if _, ok := r.Links[id]; !ok {
					t.Fatal("unregistered link marker")
				}
			}
			for _, u := range r.Links {
				if strings.Contains(u, "CANARY") {
					t.Fatal("hidden link")
				}
			}
			for _, ch := range r.Markdown {
				if (unicode.Is(unicode.Cc, ch) || unicode.Is(unicode.Cf, ch)) && ch != '\n' && ch != '\t' {
					t.Fatal("invisible character")
				}
			}
		})
	}
	for _, c := range oracle.Subjects {
		if got := content.CleanSubject(c.Input); got != c.Output {
			t.Fatalf("subject mismatch, lengths %d %d", len(got), len(c.Output))
		}
	}
}

// Variation-selector runs and Hangul fillers are invisible carriers; a single emoji presentation selector is legitimate.
func TestInvisibleCarriersStripped(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind model.ContentType
		want string
	}{
		{"\u2764\ufe0f ok", model.Text, "\u2764\ufe0f ok"},
		{"X\ufe00\ufe0f\ufe03\ufe0a\ufe01Y", model.Text, "X\ufe00Y"},
		{"X\ufe00<span>\ufe01</span><!-- -->\ufe02<b></b>\ufe03Y", model.HTML, "X\ufe00Y"},
		{"a\u3164b\u115fc\u1160d\uffa0e", model.Text, "abcde"},
	} {
		r, err := content.RenderBody(&c.in, c.kind)
		if err != nil || r.Markdown != c.want {
			t.Errorf("%q: got %q %v want %q", c.in, r.Markdown, err, c.want)
		}
	}
}

func TestFetchedBodyAndCleanText(t *testing.T) {
	if _, err := content.RenderBody(nil, model.Text); err == nil {
		t.Fatal("missing body accepted")
	}
	empty := ""
	r, err := content.RenderBody(&empty, model.Text)
	if err != nil || r.Markdown != "" || len(r.Links) != 0 || r.Truncated {
		t.Fatalf("empty body: %+v %v", r, err)
	}
	text := "Ｆｕｌｌ <b> \x1b[31mhi\x1b[0m\u200b [ L2 ] https://x.example/a ftp://x.example/x"
	r, err = content.RenderBody(&text, model.Text)
	if err != nil || r.Markdown != "Full ‹b› hi \\[ L2 \\] [L1] [link removed]" || r.Links["L1"] != "https://x.example/a" {
		t.Fatalf("render: %+v %v", r, err)
	}
}
