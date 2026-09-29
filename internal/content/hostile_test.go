package content_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard-go/internal/content"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func TestUnsupportedOrExcessiveCSSFailsClosed(t *testing.T) {
	for _, body := range []string{
		`<style>:unsupported-pseudo {display:none}</style><p>CANARY</p>`,
		`<style>` + strings.Repeat(`p {display:none}`, 2000) + `</style>` + strings.Repeat(`<p>CANARY</p>`, 1001),
	} {
		r, err := content.RenderBody(&body, model.HTML)
		if err == nil || err.Error() != "render ValueError" || r.Markdown != "" || len(r.Links) != 0 {
			t.Fatalf("CSS not refused safely: %v", err)
		}
	}
}

func TestNewsletterPseudoElementsDoNotBlockVisibleContent(t *testing.T) {
	body := `<style>p::before {content:"decoration"} p::after, .hidden {display:none} p:first-letter {font-size:18px}</style><p>Visible newsletter</p><p class="hidden">CANARY</p>`
	r, err := content.RenderBody(&body, model.HTML)
	if err != nil || r.Markdown != "Visible newsletter" || len(r.Links) != 0 {
		t.Fatalf("newsletter rendering: %q %v", r.Markdown, err)
	}
}

func TestUnclosedStylesheetCannotExposeHiddenContent(t *testing.T) {
	for _, rule := range []string{`display:none`, `visibility:hidden`, `color:transparent`, `display:none!important`} {
		body := `<style>.hidden{` + rule + `</style><p class="hidden">CANARY</p>Visible`
		r, err := content.RenderBody(&body, model.HTML)
		if err == nil && r.Markdown != "Visible" {
			t.Errorf("unclosed %s exposed content: %q", rule, r.Markdown)
		}
		if err != nil && (err.Error() != "render ValueError" || r.Markdown != "" || len(r.Links) != 0) {
			t.Fatalf("unsafe rendering failure: %+v %v", r, err)
		}
	}
}

func TestCompoundSelectorsCannotLeak(t *testing.T) {
	for _, body := range []string{
		`<style>p:not(.x,.y) {display:none}</style><p>CANARY</p>Visible`,
		`<style>[data-x="a,b"] {display:none}</style><p data-x="a,b">CANARY</p>Visible`,
		`<style>.x {d\69splay:none}</style><p class="x">CANARY</p>Visible`,
	} {
		r, err := content.RenderBody(&body, model.HTML)
		if err != nil || r.Markdown != "Visible" {
			t.Fatalf("compound CSS leaks: %q %v", r.Markdown, err)
		}
	}
}

func TestStructuredMarkdownSourceParity(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"<blockquote><p>one</p><p>two</p></blockquote>", "> one\n>\n> two"},
		{"<ul><li>one<ul><li>nested</li></ul></li><li>two</li></ul>", "- one\n- nested\n- two"},
		{"<ol start=3><li>a</li><li><p>b</p><p>c</p></li></ol>", "3. a\n4. b\n\nc"},
		{"<pre>  indented\n  code\n</pre>", "```\nindented\ncode\n```"},
		{"<h2>Hello <em>x</em></h2><p>one<br>two</p>", "## Hello *x*\n\none\ntwo"},
		{"<blockquote><p>one</p><blockquote>two</blockquote></blockquote>", "> one\n>\n> > two"},
		{"<p>A <b> bold </b> C</p>", "A **bold** C"},
		{"<p>one\x00two</p>", "onetwo"},
	} {
		r, err := content.RenderBody(&c.body, model.HTML)
		if err != nil || r.Markdown != c.want {
			t.Errorf("%q: got %q %v want %q", c.body, r.Markdown, err, c.want)
		}
	}
}

func TestCSSCascadeAndInheritedResets(t *testing.T) {
	cases := []struct{ body, want string }{
		{`<style>div span {display:none} #a {display:inline} .h {display:none!important}</style><div><span id="a" class="h" style="display:block">CANARY</span></div><p>Visible</p>`, "Visible"},
		{`<style>#a {display:none!important}</style><p id="a" style="display:block!important">Visible</p>`, "Visible"},
		{`<style>.h {color:white} .h {color:black}</style><p class="h">Visible</p>`, "Visible"},
		{`<style>[data-hide] + span {visibility:collapse}</style><i data-hide></i><span>CANARY</span><p>Visible</p>`, "Visible"},
		{`<div style="font-size:1pt;color:transparent"><span style="font-size:12px;color:black">Visible</span>CANARY</div>`, "Visible"},
		{`<div style="background:black"><p style="background:transparent;color:rgb(0 0 0)">CANARY</p><p style="color:rgb(255,255,255)">Visible</p></div>`, "Visible"},
		{`<div style="background-image:url(https://local.invalid/pixel)"><p style="color:white">Visible</p></div>`, "Visible"},
		{`<style>.h {background-image:url(https://local.invalid/pixel)}</style><div class="h"><p style="color:white">Visible</p></div>`, "Visible"},
		{`<div style="visibility:hidden"><span style="visibility:visible">CANARY</span></div>Visible`, "Visible"},
		{`<div style="margin-top:-500px">CANARY</div><span style="opacity:.1">Visible</span>`, "Visible"},
		{`<p style="clip:">CANARY</p>Visible`, "Visible"},
		{`<p style="clip-path:">CANARY</p>Visible`, "Visible"},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r, err := content.RenderBody(&c.body, model.HTML)
			if err != nil || r.Markdown != c.want {
				t.Fatalf("got %q %v, want %q", r.Markdown, err, c.want)
			}
		})
	}
}

func TestHostileDepthAndRuns(t *testing.T) {
	start := time.Now()
	for _, body := range []string{strings.Repeat("<div>", 201), strings.Repeat("<table><tr><td>", 8000)} {
		if _, err := content.RenderBody(&body, model.HTML); err == nil {
			t.Fatal("deep input accepted")
		}
	}
	body := strings.Repeat("<table><tr><td>", 66) + "Visible"
	r, err := content.RenderBody(&body, model.HTML)
	if err != nil || r.Markdown != "Visible" {
		t.Fatalf("at depth cap: %q %v", r.Markdown, err)
	}
	body = strings.Repeat(`<p style="display:none">CANARY</p>`, 10000) + "Visible"
	r, err = content.RenderBody(&body, model.HTML)
	if err != nil || r.Markdown != "Visible" {
		t.Fatalf("siblings: %q %v", r.Markdown, err)
	}
	for _, body := range []string{strings.Repeat("x", 1000000) + "://example.test", strings.Repeat("-", 1000000) + "://x"} {
		r, err = content.RenderBody(&body, model.Text)
		if err != nil || r.Markdown != "[link removed]" {
			t.Fatalf("long scheme failed: length %d %v", len(r.Markdown), err)
		}
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("hostile inputs exceeded 10s")
	}
}

func TestUnicodeTruncationAndOmittedLinks(t *testing.T) {
	for _, c := range []struct {
		body string
		kind model.ContentType
		want string
	}{
		{strings.Repeat("😀", 19998) + " https://x.example/1 more", model.Text, strings.Repeat("😀", 19998)},
		{"<p>" + strings.Repeat("a", 19990) + ` <a href="https://x.example">link text</a> tail</p>`, model.HTML, strings.Repeat("a", 19990)},
	} {
		r, err := content.RenderBody(&c.body, c.kind)
		if err != nil || !r.Truncated || r.Markdown != c.want || len(r.Links) != 0 {
			t.Fatalf("truncation: %d %v %v %v", utf8.RuneCountInString(r.Markdown), r.Links, r.Truncated, err)
		}
	}
	body := strings.Repeat("😀", 20000)
	r, err := content.RenderBody(&body, model.Text)
	if err != nil || r.Truncated || r.Markdown != body {
		t.Fatal("exact cap truncated")
	}
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, `<p>Paragraph %d text <a href="https://x.example/%d">link %d</a></p>`, i, i, i)
	}
	body = b.String()
	r, err = content.RenderBody(&body, model.HTML)
	if err != nil || !r.Truncated || utf8.RuneCountInString(r.Markdown) > content.MaxChars {
		t.Fatal("paragraph truncation failed")
	}
	for i := 1; i <= len(r.Links); i++ {
		id := fmt.Sprintf("L%d", i)
		if !strings.Contains(r.Markdown, "["+id+"]") || r.Links[id] == "" {
			t.Fatalf("noncontiguous %s", id)
		}
	}
}

func TestIDNANamePreparation(t *testing.T) {
	r := content.Rendered{Links: map[string]string{"L1": "https://ＦＯＯ.com/", "L2": "https://faß.de/", "L3": "https://😀.example/"}}
	want := map[string]string{"L1": "foo.com", "L2": "fass.de", "L3": "xn--e28h.example"}
	if !reflect.DeepEqual(r.Domains(), want) {
		t.Fatalf("name preparation differs: %v", r.Domains())
	}
}

func TestDomainsAndSupplementarySelectors(t *testing.T) {
	r := content.Rendered{Links: map[string]string{"L1": "https://Dashboards.Acme.com/q", "L2": "https://аpple.com/", "L3": "https://a..b/", "L4": "https://a%3cb/"}}
	if !reflect.DeepEqual(r.Domains(), map[string]string{"L1": "dashboards.acme.com", "L2": "xn--pple-43d.com", "L3": "invalid-host", "L4": "invalid-host"}) {
		t.Fatal(r.Domains())
	}
	body := "[L\U000e01003] \x1b]8;;https://osc.test\x1b\\visible\x1b]8;;\x07 ://x +http://x"
	r, err := content.RenderBody(&body, model.Text)
	if err != nil || r.Markdown != `\[L3\] visible [link removed] [link removed]` {
		t.Fatalf("%q %v", r.Markdown, err)
	}
}
