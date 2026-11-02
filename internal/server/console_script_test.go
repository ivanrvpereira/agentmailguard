package server_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The console script is served like every page: only on a console host, only after sign-in, with the security
// headers, as JavaScript that no cache keeps. No other asset exists.
func TestConsoleServesItsScriptOnlyAfterSignIn(t *testing.T) {
	f := consoleDaemon(t, "", true)
	w := f.get(t, "/assets/console.js", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Body.String(), "// @ts-check\n") {
		t.Fatalf("script: %d %q %s", w.Code, w.Header(), w.Body)
	}
	if w := f.get(t, "/assets/console.js", func(r *http.Request) { r.Header.Del("Authorization") }); w.Code != 401 || strings.Contains(w.Body.String(), "@ts-check") {
		t.Fatalf("script without sign-in: %d %s", w.Code, w.Body)
	}
	if w := f.get(t, "/assets/console.js", func(r *http.Request) { r.Host = "evil.example" }); w.Code != 403 || strings.Contains(w.Body.String(), "@ts-check") {
		t.Fatalf("script on a foreign host: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/assets/", "/assets", "/assets/other.js", "/assets/console.js/x", "/assets/Console.js", "/assets/console.js.map", "/assets/../console.js", "/console.js"} {
		if w := f.get(t, path, nil); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := f.send(t, httptest.NewRequest(method, "/assets/console.js", nil), func(r *http.Request) { r.Header.Set("Origin", "http://"+consoleHost) })
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Errorf("%s script: %d %q", method, w.Code, w.Header().Get("Allow"))
		}
	}
}

// Every console page loads the console script once, as a module from the console, and has no other script and no
// event handler attribute: the empty list, a list with every kind of row, a ticked table, and the action pages of an
// action that needs review, of an HTML body, and of a handled action.
func TestConsolePagesLoadOnlyTheConsoleScript(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	pages := map[string]string{"empty list": f.get(t, "/", nil).Body.String()}
	f.setScore(.62)
	review := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(.1)
	for _, w := range waitingOps {
		pend(t, f.d.s, w.op, w.params)
	}
	rendered := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","html":true,"body":"<p>Hi</p><script>alert(1)</script>"}`)[0]
	handled := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	if w := f.decide(t, handled, "deny", nil); w.Code != 303 {
		t.Fatalf("deny: %d", w.Code)
	}
	pages["list"] = f.get(t, "/", nil).Body.String()
	pages["ticked table"] = f.get(t, "/?select=messages&approved=1", nil).Body.String()
	pages["needs review"] = f.get(t, "/a/"+review, nil).Body.String()
	pages["rendered"] = f.get(t, "/a/"+rendered, nil).Body.String()
	pages["handled"] = f.get(t, "/a/"+handled, nil).Body.String()
	pages["handled again"] = f.decide(t, handled, "deny", nil).Body.String()
	for name, page := range pages {
		t.Run(name, func(t *testing.T) { onlyConsoleLinks(t, page) })
	}
}

// scriptContract is every selector, element ID, and data attribute that the console script reads, with the markup of
// the page that renders it: list or action.
var scriptContract = []struct{ script, page, markup string }{
	{"waiting", "list", `<body data-refresh-seconds="300" data-waiting="2">`},
	{"refreshSeconds", "list", `<body data-refresh-seconds="300" data-waiting="2">`},
	{"section[data-group]", "list", `<section class="tbl t-mail" aria-labelledby="h-messages" data-group="messages">`},
	{"a.all", "list", `<a class="all" href="/?select=messages"`},
	{"label.all", "list", `<label class="all" hidden><input class="allbox" type="checkbox"><span class="vh">Select all in Messages</span></label>`},
	{"input.allbox", "list", `<input class="allbox" type="checkbox">`},
	{"input.sel", "list", `<input class="sel" type="checkbox" form="batch" name="id" value="`},
	{"review", "list", `" data-review>`},
	{"selstatus", "list", `<p class="vh" id="selstatus" aria-live="polite"></p>`},
	{"batch", "list", `<form id="batch" method="post" action="/batch"`},
	{"form.ack", "list", `<form class="ack" method="post" action="/a/`},
	{"form.ack", "action", `<form class="decide ack" method="post" action="/a/`},
	{`input[name="confirm"]`, "list", `<input type="checkbox" name="confirm" value="flagged" required>`},
	{`input[name="confirm"]`, "action", `<input type="checkbox" name="confirm" value="flagged" required>`},
	{`button[value="approve"]`, "list", `<button class="btn go" type="submit" name="decision" value="approve">Approve<span class="vh">`},
	{`button[value="approve"]`, "action", `<button class="btn go" type="submit" name="decision" value="approve">Approve</button>`},
	{"input.sel:checked, .row details[open], details.recent[open], details.refresh[open]", "list", `<li class="row risky">`},
	{"input.sel:checked, .row details[open], details.recent[open], details.refresh[open]", "list", "\n  <details>\n    <summary>"},
	{"input.sel:checked, .row details[open], details.recent[open], details.refresh[open]", "list", `<details class="recent">`},
	{"input.sel:checked, .row details[open], details.recent[open], details.refresh[open]", "list", `<details class="refresh">`},
}

// scriptReads are the selectors, element IDs, and data attributes in the console script.
var scriptReads = regexp.MustCompile(`(?:querySelector|querySelectorAll|matches|closest|getElementById)\((?:"([^"]*)"|'([^']*)')\)|dataset\.([A-Za-z]+)`)

// The console script and the templates agree: the pages render everything that the script reads, and the script
// reads nothing that scriptContract does not list.
func TestConsoleScriptReadsOnlyWhatTheTemplatesRender(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	f.setScore(.62)
	review := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(.1)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	handled := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	if w := f.decide(t, handled, "deny", nil); w.Code != 303 {
		t.Fatalf("deny: %d", w.Code)
	}
	pages := map[string]string{"list": f.get(t, "/", nil).Body.String(), "action": f.get(t, "/a/"+review, nil).Body.String()}
	listed := map[string]bool{}
	for _, c := range scriptContract {
		listed[c.script] = true
		if !strings.Contains(pages[c.page], c.markup) {
			t.Errorf("the %s page lacks %q, which the script reads as %q", c.page, c.markup, c.script)
		}
	}
	script := f.get(t, "/assets/console.js", nil).Body.String()
	read := map[string]bool{}
	for _, m := range scriptReads.FindAllStringSubmatch(script, -1) {
		read[m[1]+m[2]+m[3]] = true
	}
	for name := range listed {
		if !read[name] {
			t.Errorf("the script no longer reads %q; update scriptContract with the template", name)
		}
	}
	for name := range read {
		if !listed[name] {
			t.Errorf("the script reads %q, which scriptContract does not list", name)
		}
	}
}

// htmlSinks are the script APIs that could turn text into markup or code, or reach the network.
var htmlSinks = regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment|setHTML|parseFromString|srcdoc|\beval\b|\bFunction\b|\bfetch\b|XMLHttpRequest|sendBeacon|WebSocket|EventSource|\bimport\s*\(|set(?:Timeout|Interval)\s*\(\s*["'` + "`]")

// textReads are uses of element text that are not an assignment: the script must never read row text.
var textReads = regexp.MustCompile(`(?:textContent|innerText|outerText)(?:\s*[^=\s]|\s*==)`)

// The console script uses no HTML sink, no code from strings, and no network call, and only writes element text.
func TestConsoleScriptHasNoHTMLSinks(t *testing.T) {
	f := consoleDaemon(t, "", true)
	script := f.get(t, "/assets/console.js", nil).Body.String()
	if !strings.Contains(script, ".textContent = ") {
		t.Fatalf("script: %s", script)
	}
	if m := htmlSinks.FindAllString(script, -1); m != nil {
		t.Errorf("the script uses %q", m)
	}
	if m := textReads.FindAllString(script, -1); m != nil {
		t.Errorf("the script reads element text: %q", m)
	}
}
