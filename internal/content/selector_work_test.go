package content_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/content"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func TestSelectorWorkBudget(t *testing.T) {
	descendant := `.missing ` + strings.Repeat(`.x `, 18)
	sibling := `.missing` + strings.Repeat(` ~ .x`, 18)
	deep := strings.Repeat(`<div class="x">`, 36) + `CANARY` + strings.Repeat(`</div>`, 36)
	wide := strings.Repeat(`<span class="x">CANARY</span>`, 36)
	cases := map[string]string{
		"descendant":     `<style>` + descendant + `{display:none}</style>` + deep,
		"sibling":        `<style>` + sibling + `{display:none}</style>` + wide,
		"not":            `<style>.x:not(.other,` + descendant + `){display:none}</style>` + deep,
		"escaped-not":    `<style>.x:\6e ot(.other,` + descendant + `){display:none}</style>` + deep,
		"nested-not":     `<style>.x:not(:not(` + descendant + `)){display:none}</style>` + deep,
		"has":            `<style>body:has(` + descendant + `){display:none}</style>` + deep,
		"haschild":       `<style>.x:haschild(.x:not(` + descendant + `)){display:none}</style>` + deep,
		"nested-sibling": `<style>body:has(.x:not(` + sibling + `)){display:none}</style>` + wide,
	}
	if name := os.Getenv("AGENTMAILGUARD_TEST_SELECTOR_CHILD"); name != "" {
		body, ok := cases[name]
		if !ok {
			t.Fatal("unknown synthetic case")
		}
		body = `<a href="https://example.test/early">Visible prefix</a>` + body
		r, err := content.RenderBody(&body, model.HTML)
		if err == nil || err.Error() != "render ValueError" || r.Markdown != "" || len(r.Links) != 0 {
			t.Fatalf("excessive selector work did not fail closed: %+v %v", r, err)
		}
		return
	}
	for name := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSelectorWorkBudget$", "-test.count=1")
			cmd.Env = append(os.Environ(), "AGENTMAILGUARD_TEST_SELECTOR_CHILD="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("selector render exceeded three-second subprocess deadline")
			}
			if err != nil {
				t.Fatalf("renderer subprocess: %v\n%s", err, out)
			}
		})
	}
}

func TestOrdinaryClassNamesAreNotCountedAsPseudoSelectors(t *testing.T) {
	body := `<style>.disabled {color:black}</style>` + strings.Repeat(`<span class="disabled">x</span>`, 3000)
	r, err := content.RenderBody(&body, model.HTML)
	if err != nil || r.Markdown != strings.Repeat("x", 3000) {
		t.Fatalf("ordinary class selector rejected: rendered %d characters, %v", len(r.Markdown), err)
	}
}

func TestNewsletterSelectorsRemainUsable(t *testing.T) {
	cases := []string{
		`<style>body > div.newsletter table.layout > tbody > tr > td div.copy p a.link {color:black} [data-note="a ~ b > c"]:not(.hidden,.archive) {color:black} .copy .hidden {display:none}</style><div class="newsletter"><table class="layout"><tr><td><div class="copy"><p data-note="a ~ b > c"><a class="link" href="https://example.test/article">Visible</a></p><p class="hidden">CANARY</p></div></td></tr></table></div>`,
		`<style>div.newsletter > .row ~ .row > p:not(.hidden):has(a.link) {color:black} .hidden{display:none}</style><div class="newsletter"><div class="row"></div><div class="row"><p><a class="link" href="https://example.test/article">Visible</a></p></div><p class="hidden">CANARY</p></div>`,
		`<style>.newsletter ` + strings.Repeat(`> div `, 190) + `> p > a {color:black} .newsletter a.link, .newsletter div a {color:black}</style><div class="newsletter">` + strings.Repeat(`<div>`, 190) + `<p><a class="link" href="https://example.test/article">Visible</a></p>` + strings.Repeat(`</div>`, 191),
	}
	for i, body := range cases {
		r, err := content.RenderBody(&body, model.HTML)
		if err != nil || r.Markdown != "[Visible][L1]" || len(r.Links) != 1 || r.Links["L1"] != "https://example.test/article" {
			t.Errorf("newsletter %d rejected or changed: %+v %v", i, r, err)
		}
	}
}
