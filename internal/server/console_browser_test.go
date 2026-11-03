package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// The browser tests drive the console script in headless Chrome. They run only with AMG_BROWSER_TESTS=1, and skip
// when no Chrome or Chromium is installed.
const browserTestsEnv = "AMG_BROWSER_TESTS"

// browserNames are the Chrome and Chromium commands that the browser tests look for, in order.
var browserNames = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "headless-shell", "headless_shell", "chrome",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// browserTimeout bounds every browser call, and browserLifetime the browser of a test.
const (
	browserTimeout  = 20 * time.Second
	browserLifetime = 2 * time.Minute
)

// violationListener reports every content security policy and Trusted Types violation of a page to the console.
// The browser runs it before the page's own script, outside the page's policy.
const violationListener = `document.addEventListener("securitypolicyviolation", (e) => console.error("securitypolicyviolation", e.violatedDirective, e.blockedURI));`

// consoleBrowser is headless Chrome signed in to the console of a console fixture, served on a loopback port that
// the console accepts as its host.
type consoleBrowser struct {
	ctx  context.Context
	base string
	mu   sync.Mutex
	// messages are the console messages of every page, dialogs the texts of the dialogs that a page opened.
	messages, dialogs []string
}

// browserConsole starts a console fixture with config and a browser on its console. At the end, the test fails on a
// dialog and on any console message, such as a policy violation or an exception, for which expected is false.
func browserConsole(t *testing.T, config string, expected func(message string) bool) (*consoleFixture, *consoleBrowser) {
	t.Helper()
	if os.Getenv(browserTestsEnv) != "1" {
		t.Skip("set " + browserTestsEnv + "=1 to run the browser tests")
	}
	path := ""
	for _, name := range browserNames {
		if p, err := exec.LookPath(name); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	srv := httptest.NewUnstartedServer(nil)
	f := consoleDaemon(t, config+"[console]\nhosts=['"+srv.Listener.Addr().String()+"']\n", true)
	srv.Config.Handler = f.console
	srv.Start()
	t.Cleanup(srv.Close)

	// The browser lives at most browserLifetime; every call in it has its own browserTimeout.
	life, cancelLife := context.WithTimeout(context.Background(), browserLifetime)
	t.Cleanup(cancelLife)
	alloc, cancelAlloc := chromedp.NewExecAllocator(life, append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	b := &consoleBrowser{ctx: ctx, base: srv.URL}
	// Start the browser on the tab's own context, which a call's timeout must not end, so that the subscriptions
	// below have a tab.
	if err := chromedp.Do(ctx); err != nil {
		t.Fatal(err)
	}
	messages := chromedp.Console(ctx)
	dialogs := chromedp.Events(ctx, cdppage.JavascriptDialogOpening)
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			b.mu.Lock()
			b.messages = append(b.messages, m.String())
			b.mu.Unlock()
		}
	}()
	go func() {
		for d, err := range dialogs {
			if err != nil {
				return
			}
			b.mu.Lock()
			b.dialogs = append(b.dialogs, d.Message)
			b.mu.Unlock()
			_, _ = chromedp.Call(ctx, cdppage.HandleJavaScriptDialog, cdppage.HandleJavaScriptDialogParams{})
		}
	}()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(consoleUser+":"+consolePassword))
	b.do(t, func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		if _, err := cdp.Call(ctx, tg, network.SetExtraHTTPHeaders, network.SetExtraHTTPHeadersParams{Headers: network.Headers{"Authorization": auth}}); err != nil {
			return chromedp.Void{}, err
		}
		_, err := cdp.Call(ctx, tg, cdppage.AddScriptToEvaluateOnNewDocument, cdppage.AddScriptToEvaluateOnNewDocumentParams{Source: violationListener})
		return chromedp.Void{}, err
	})
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.dialogs) > 0 {
			t.Errorf("dialogs: %q", b.dialogs)
		}
		for _, m := range b.messages {
			if expected == nil || !expected(m) {
				t.Errorf("console message: %s", m)
			}
		}
	})
	return f, b
}

// do runs the action in the browser within browserTimeout.
func (b *consoleBrowser) do(t *testing.T, a chromedp.Action[chromedp.Void]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, browserTimeout)
	defer cancel()
	if _, err := chromedp.Run(ctx, a); err != nil {
		t.Fatal(err)
	}
}

// open loads the console path.
func (b *consoleBrowser) open(t *testing.T, path string) {
	t.Helper()
	b.do(t, chromedp.Navigate(b.base+path))
}

// eval evaluates the expression in the page and decodes its result into T.
func eval[T any](t *testing.T, b *consoleBrowser, expression string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, browserTimeout)
	defer cancel()
	v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expression))
	if err != nil {
		t.Fatalf("%s: %v", expression, err)
	}
	return v
}

// waitUntil polls the expression until it is true, also across page loads, and fails after within.
func (b *consoleBrowser) waitUntil(t *testing.T, expression string, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(b.ctx, time.Second)
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expression))
		cancel()
		if err == nil && ok {
			return
		}
	}
	t.Fatalf("not within %s: %s", within, expression)
}

// mark marks the loaded page; reloaded is true once another page has loaded.
const (
	mark     = "window.amgTestMark = true"
	reloaded = `document.readyState === "complete" && window.amgTestMark !== true`
)

// boxState is the state of the matching checkboxes: c for checked, i for indeterminate, - for neither.
func boxState(t *testing.T, b *consoleBrowser, selector string) string {
	t.Helper()
	sel, err := json.Marshal(selector)
	if err != nil {
		t.Fatal(err)
	}
	return eval[string](t, b, `Array.from(document.querySelectorAll(`+string(sel)+`), (x) => x.indeterminate ? "i" : x.checked ? "c" : "-").join("")`)
}

// The list reloads itself when the interval is up, but never while the owner works: while a row box is ticked, a
// row, Recents, or the Auto-refresh menu is open. It reloads once the page has been idle for the grace period, and
// the reload keeps the filter but not the ticked table or the batch notice.
func TestBrowserConsoleRefreshPausesWhileTheOwnerWorks(t *testing.T) {
	// A 3-second interval has a 3-second grace period. The script checks once a second, so the page reloads at
	// least 2 seconds after the last busy moment.
	t.Cleanup(server.SetRefreshSeconds("5m", 3))
	f, b := browserConsole(t, everyWriteWaits, nil)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	if err := f.d.s.Deny(context.Background(), pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]); err != nil {
		t.Fatal(err)
	}
	b.open(t, "/")
	if got := eval[string](t, b, "document.body.dataset.refreshSeconds"); got != "3" {
		t.Fatalf("refresh seconds %q", got)
	}
	for _, c := range []struct{ name, start, stop string }{
		{"a ticked box", `document.querySelector("input.sel").click()`, `document.querySelector("input.sel").click()`},
		{"an open row", `document.querySelector(".row details").open = true`, `document.querySelector(".row details").open = false`},
		{"open Recents", `document.querySelector("details.recent").open = true`, `document.querySelector("details.recent").open = false`},
		{"the open menu", `document.querySelector("details.refresh").open = true`, `document.querySelector("details.refresh").open = false`},
	} {
		eval[chromedp.Void](t, b, mark)
		eval[chromedp.Void](t, b, c.start)
		time.Sleep(4 * time.Second)
		if eval[bool](t, b, reloaded) {
			t.Fatalf("%s: the page reloaded while busy", c.name)
		}
		stopped := eval[float64](t, b, "("+c.stop+", Date.now())")
		b.waitUntil(t, reloaded, 8*time.Second)
		if idle := eval[float64](t, b, "performance.timeOrigin") - stopped; idle < 1500 {
			t.Fatalf("%s: the page reloaded %.0f ms after the owner stopped, within the grace period", c.name, idle)
		}
	}
	b.open(t, "/?account=work&select=messages&approved=1")
	eval[chromedp.Void](t, b, mark)
	time.Sleep(4 * time.Second)
	if eval[bool](t, b, reloaded) {
		t.Fatal("the page with a ticked table reloaded")
	}
	eval[chromedp.Void](t, b, `document.querySelector("input.allbox").click()`)
	b.waitUntil(t, reloaded, 8*time.Second)
	if got := eval[string](t, b, "location.pathname + location.search"); got != "/?account=work" {
		t.Fatalf("reloaded to %q", got)
	}
	if got := boxState(t, b, "input.sel"); got != "--" {
		t.Fatalf("boxes after the reload: %q", got)
	}
}

// With the script, each table's header link becomes a checkbox that ticks the table's rows and shows checked or
// indeterminate; Clear clears it. A screen-reader line repeats the selection count, and the title shows the waiting
// count.
func TestBrowserConsoleHeaderCheckboxSelectsATable(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	f.setScore(.62)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	f.setScore(.1)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	pend(t, f.d.s, "calendar.invite", `{"to":["a@example.test"],"subject":"s","body":"b","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)
	b.open(t, "/?refresh=off")
	if got := eval[string](t, b, "document.title"); got != "(4) AgentMailGuard · Web console" {
		t.Fatalf("title %q", got)
	}
	if got := eval[string](t, b, `Array.from(document.querySelectorAll(".colhead .all"), (x) => getComputedStyle(x).display).join(" ")`); got != "none flex none flex" {
		t.Fatalf("header link and box display %q", got)
	}
	status := func() string { return eval[string](t, b, `document.getElementById("selstatus").textContent`) }
	messages := `section[data-group="messages"] input.sel`
	for _, step := range []struct{ click, headers, rows, status string }{
		{`section[data-group="messages"] input.allbox`, "c-", "ccc-", "3 selected, 1 needs review: Approve skips them"},
		{"input.sel[data-review]", "i-", "cc--", "2 selected"},
		{`section[data-group="meetings"] input.sel`, "ic", "cc-c", "3 selected"},
		{"input.sel[data-review]", "cc", "cccc", "4 selected, 1 needs review: Approve skips them"},
		{`section[data-group="meetings"] input.allbox`, "c-", "ccc-", "3 selected, 1 needs review: Approve skips them"},
		{messages + ":not([data-review]):checked", "i-", "-cc-", "2 selected, 1 needs review: Approve skips them"},
		{messages + ":not([data-review]):checked", "i-", "--c-", "1 selected, all need review: approve each from its row"},
	} {
		sel, err := json.Marshal(step.click)
		if err != nil {
			t.Fatal(err)
		}
		eval[chromedp.Void](t, b, `document.querySelector(`+string(sel)+`).click()`)
		if headers, rows, text := boxState(t, b, "input.allbox"), boxState(t, b, "input.sel"), status(); headers != step.headers || rows != step.rows || text != step.status {
			t.Fatalf("after %s: headers %q rows %q status %q", step.click, headers, rows, text)
		}
	}
	eval[chromedp.Void](t, b, `document.querySelector("#batch button[type=reset]").click()`)
	b.waitUntil(t, `document.getElementById("selstatus").textContent === ""`, 2*time.Second)
	if headers, rows := boxState(t, b, "input.allbox"), boxState(t, b, "input.sel"); headers != "--" || rows != "----" {
		t.Fatalf("after Clear: headers %q rows %q", headers, rows)
	}
}

// Approve of an action that needs review stays disabled until "I reviewed it" is ticked, in the open row and on the
// action page; Deny stays enabled. The action page has no title count.
func TestBrowserConsoleApproveWaitsForIReviewedIt(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	f.setScore(.62)
	review := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(.1)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	for _, c := range []struct{ path, form string }{{"/?refresh=off", "li.row form.ack"}, {"/a/" + review, "form.decide.ack"}} {
		b.open(t, c.path)
		// The Approve of the review form, and whether any other decision button is disabled.
		buttons := `[document.querySelector("` + c.form + ` button[value=approve]").disabled,
			Array.from(document.querySelectorAll("button[name=decision]")).some((x) => x.disabled && !(x.closest("` + c.form + `") && x.value === "approve"))].join(" ")`
		tick := `document.querySelector("` + c.form + ` input[name=confirm]").click()`
		before := eval[string](t, b, buttons)
		eval[chromedp.Void](t, b, tick)
		ticked := eval[string](t, b, buttons)
		eval[chromedp.Void](t, b, tick)
		unticked := eval[string](t, b, buttons)
		if before != "true false" || ticked != "false false" || unticked != "true false" {
			t.Errorf("%s: Approve disabled and others disabled %q, ticked %q, unticked %q", c.path, before, ticked, unticked)
		}
	}
	if got := eval[string](t, b, "document.title"); got != "Send · AgentMailGuard · Web console" {
		t.Fatalf("action page title %q", got)
	}
}

// The Rendered preview shows the HTML body, but runs none of its scripts and loads nothing: every remote reference
// of the body points at a server that counts requests.
func TestBrowserConsoleRenderedPreviewRunsNothingAndLoadsNothing(t *testing.T) {
	var mu sync.Mutex
	var remote []string
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remote = append(remote, r.URL.Path)
		mu.Unlock()
	}))
	t.Cleanup(counter.Close)
	// The frame's blocked loads and scripts are expected messages of the frame.
	f, b := browserConsole(t, everyWriteWaits, func(m string) bool {
		return strings.Contains(m, counter.URL) || strings.Contains(m, "about:srcdoc")
	})
	at := counter.URL
	body := `<p>Rendered hello</p><img src="` + at + `/img" onerror="document.body.dataset.onerror = 'yes'; alert(1)"><script src="` + at + `/script"></script>` +
		`<script>document.body.dataset.ran = "yes"; parent.document.title = "pwned"; new Image().src = "` + at + `/inline";</script>` +
		`<link rel="stylesheet" href="` + at + `/css"><iframe src="` + at + `/frame"></iframe><form action="` + at + `/form"><input type="submit"></form>`
	h, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	id := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","html":true,"body":`+string(h)+`}`)[0]
	b.open(t, "/a/"+id)
	time.Sleep(time.Second)
	if got := eval[string](t, b, "document.title"); got != "Send · AgentMailGuard · Web console" {
		t.Fatalf("title %q", got)
	}
	mu.Lock()
	loaded := slices.Clone(remote)
	mu.Unlock()
	if len(loaded) > 0 {
		t.Fatalf("the frame loaded %q", loaded)
	}
	shown, ok := frameBody(t, b)
	if !ok {
		t.Skip("the browser exposes the Rendered frame neither as a target nor in the page's frame tree")
	}
	if shown != "Rendered hello||" {
		t.Fatalf("frame body and script marks %q", shown)
	}
}

// A click in the Rendered preview follows no link, opens no window, and sends no form: a plain link, a link to a new
// window, a link to the whole window, and a submit button each point at a server that counts requests. Each click
// runs in the frame as a user gesture, so the frame has the user activation that some navigations need.
func TestBrowserConsoleRenderedPreviewFollowsNoLinkAndSendsNoForm(t *testing.T) {
	var mu sync.Mutex
	var remote []string
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remote = append(remote, r.URL.Path)
		mu.Unlock()
	}))
	t.Cleanup(counter.Close)
	// The frame's blocked navigations, windows, and forms are expected messages of the frame.
	f, b := browserConsole(t, everyWriteWaits, func(m string) bool {
		return strings.Contains(m, counter.URL) || strings.Contains(m, "about:srcdoc")
	})
	at := counter.URL
	body := `<a id="link" href="` + at + `/link">link</a><a id="window" target="_blank" href="` + at + `/window">window</a>` +
		`<a id="top" target="_top" href="` + at + `/top">top</a><form action="` + at + `/form" method="post"><input name="x" value="1"><button id="submit">send</button></form>`
	h, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	id := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","html":true,"body":`+string(h)+`}`)[0]
	for _, element := range []string{"link", "window", "top", "submit"} {
		// A fresh page for each click: a blocked navigation can leave the frame on an error page.
		b.open(t, "/a/"+id)
		// Chrome runs the frame in its own process, as a target of its own. A frame that the page can reach, such as
		// one with the same origin, is clicked from the page.
		frame, ok := frameTarget(t, b)
		click := `document.getElementById("` + element + `").click()`
		if !ok {
			frame, click = b.ctx, `(() => { const d = document.querySelector("iframe").contentDocument; if (!d) return false; d.getElementById("`+element+`").click(); return true; })()`
		}
		ctx, cancel := context.WithTimeout(frame, browserTimeout)
		clicked, err := chromedp.Run(ctx, chromedp.Evaluate[any](click, asUserGesture))
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", element, err)
		}
		if clicked == false {
			t.Skip("the browser exposes the Rendered frame neither as a target nor to the page")
		}
		time.Sleep(time.Second)
		if got := eval[string](t, b, "location.pathname"); got != "/a/"+id {
			t.Fatalf("%s: the console page went to %q", element, got)
		}
		ctx, cancel = context.WithTimeout(b.ctx, browserTimeout)
		infos, err := chromedp.Targets(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if pages := slices.DeleteFunc(infos, func(info *target.Info) bool { return info.Type != "page" }); len(pages) != 1 {
			t.Fatalf("%s: %d pages open", element, len(pages))
		}
		mu.Lock()
		loaded := slices.Clone(remote)
		mu.Unlock()
		if len(loaded) > 0 {
			t.Fatalf("%s: the frame reached %q", element, loaded)
		}
	}
}

// asUserGesture runs an evaluation as if the user started it in the page.
func asUserGesture(p *runtime.EvaluateParams) { p.UserGesture = new(true) }

// frameTarget is a browser context on the Rendered frame, which Chrome runs in its own process, as a target of its
// own. ok is false when the browser has no such target.
func frameTarget(t *testing.T, b *consoleBrowser) (context.Context, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, browserTimeout)
	defer cancel()
	infos, err := chromedp.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(infos, func(info *target.Info) bool { return info.Type == "iframe" && info.URL == "about:srcdoc" })
	if i < 0 {
		return nil, false
	}
	frame, cancelFrame := chromedp.NewContext(b.ctx, chromedp.WithTargetID(infos[i].TargetID))
	t.Cleanup(cancelFrame)
	return frame, true
}

// Below the desktop width, the tables stack each row on a few lines, so that the event of a meeting and the subject
// of a message always have room and the page never scrolls sideways. At a desktop width the columns fit.
func TestBrowserConsoleRowsKeepTheirTextAtEveryWidth(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"Quarterly planning notes","body":"b"}`)
	pend(t, f.d.s, "calendar.invite", `{"to":["a@example.test"],"subject":"Quarterly planning","body":"b","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)
	for _, c := range []struct {
		width   int64
		stacked bool
	}{{390, true}, {800, true}, {1000, true}, {1150, true}, {1160, false}, {1440, false}} {
		b.do(t, chromedp.EmulateViewport(c.width, 900))
		b.open(t, "/?refresh=off")
		got := eval[[]float64](t, b, `[
			document.querySelector('section[data-group="meetings"] li.row .ev').getBoundingClientRect().width,
			document.querySelector('section[data-group="messages"] li.row .text').getBoundingClientRect().width,
			document.documentElement.scrollWidth - window.innerWidth,
			getComputedStyle(document.querySelector(".tbl > .thead .cells")).display === "none" ? 1 : 0]`)
		if got[0] < 120 || got[1] < 120 || got[2] > 0 || (got[3] == 1) != c.stacked {
			t.Errorf("%dpx: event %.0fpx, message %.0fpx, sideways scroll %.0fpx, stacked %v (want %v)", c.width, got[0], got[1], got[2], got[3] == 1, c.stacked)
		}
	}
}

// Ticking or clearing a box moves no row on the screen, at the top of the list or scrolled to its bottom: the bulk bar
// floats at the bottom of the window with the selection count in it, and the page keeps room below its last row, so
// that the last row still scrolls above the bar and clearing the selection does not shorten the page.
func TestBrowserConsoleBulkBarMovesNoRow(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	f.setScore(.62)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	f.setScore(.1)
	for range 12 {
		pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	}
	// The rows' places on the screen, and the scroll position.
	onScreen := `[scrollY, ...Array.from(document.querySelectorAll("li.row"), (r) => r.getBoundingClientRect().top)].join(" ")`
	for _, width := range []int64{390, 1000, 1440} {
		b.do(t, chromedp.EmulateViewport(width, 900))
		for _, c := range []struct{ where, scroll, box string }{
			{"top", "scrollTo(0, 0)", `document.querySelector("input.sel")`},
			{"bottom", "scrollTo(0, document.documentElement.scrollHeight)", `Array.from(document.querySelectorAll("input.sel")).at(-1)`},
		} {
			b.open(t, "/?refresh=off")
			eval[chromedp.Void](t, b, c.scroll)
			before := eval[string](t, b, onScreen)
			eval[chromedp.Void](t, b, c.box+".click()")
			ticked := eval[string](t, b, onScreen)
			got := eval[[]float64](t, b, `(() => {
				const bar = document.querySelector(".bar").getBoundingClientRect(), count = document.querySelector(".selcount").getBoundingClientRect(), rows = document.querySelectorAll(".waiting li.row");
				scrollTo(0, document.documentElement.scrollHeight);
				const last = rows[rows.length - 1].getBoundingClientRect().bottom - document.querySelector(".bar").getBoundingClientRect().top;
				return [innerHeight - bar.bottom, last, count.top >= bar.top && count.bottom <= bar.bottom && count.height > 0 ? 1 : 0];
			})()`)
			eval[chromedp.Void](t, b, c.scroll)
			ticked2 := eval[string](t, b, onScreen)
			eval[chromedp.Void](t, b, c.box+".click()")
			cleared := eval[string](t, b, onScreen)
			// Ticking moves no row; scrolled as far as the ticked page goes, clearing moves no row either.
			if before != ticked || ticked2 != cleared || got[0] != 0 || got[1] > 0 || got[2] != 1 {
				t.Errorf("%dpx, %s: rows %q, ticked %q; ticked and scrolled %q, cleared %q; bar %.0fpx above the bottom, last row %.0fpx under the bar, count in the bar %v",
					width, c.where, before, ticked, ticked2, cleared, got[0], got[1], got[2] == 1)
			}
		}
		// With a row that needs review ticked, the count and its review warning, which CSS writes, fit in the bar.
		b.open(t, "/?refresh=off")
		eval[chromedp.Void](t, b, `document.querySelector("input.sel[data-review]").click(); document.querySelector("input.sel:not([data-review])").click()`)
		fits := eval[[]any](t, b, `(() => {
			const c = document.querySelector(".selcount"), r = c.getBoundingClientRect(), bar = document.querySelector(".bar").getBoundingClientRect();
			return [c.scrollHeight - c.clientHeight, r.top >= bar.top && r.bottom <= bar.bottom, getComputedStyle(c, "::after").content];
		})()`)
		if warning, _ := fits[2].(string); fits[0].(float64) > 0 || fits[1] != true || !strings.Contains(warning, "need review") {
			t.Errorf("%dpx: selection text overflows its box by %vpx, box in the bar %v, warning %q", width, fits[0], fits[1], fits[2])
		}
	}
}

// In an open row that needs review, Approve and Deny sit side by side in the review box, and the row buttons hide.
func TestBrowserConsoleOpenReviewRowKeepsApproveAndDenyTogether(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	f.setScore(.62)
	pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	for _, width := range []int64{390, 1440} {
		b.do(t, chromedp.EmulateViewport(width, 900))
		b.open(t, "/?refresh=off")
		got := eval[[]float64](t, b, `(() => {
			const row = document.querySelector("li.row:has(form.ack)");
			row.querySelector("details").open = true;
			const approve = row.querySelector("form.ack button[value=approve]").getBoundingClientRect(), deny = row.querySelector("form.ack button[value=deny]").getBoundingClientRect();
			return [Math.abs(approve.top - deny.top), deny.left - approve.right, getComputedStyle(row.querySelector("form.acts")).display === "none" ? 1 : 0];
		})()`)
		if got[0] != 0 || got[1] < 0 || got[1] > 16 || got[2] != 1 {
			t.Errorf("%dpx: Approve and Deny %.0fpx apart in height, %.0fpx apart in width, row buttons hidden %v", width, got[0], got[1], got[2] == 1)
		}
	}
}

// A Recents row shows its Risk at every width, beside the action and account, never on top of them, so that a
// handled action that needed review stays visible as one.
func TestBrowserConsoleRecentsKeepTheirRiskAtEveryWidth(t *testing.T) {
	f, b := browserConsole(t, everyWriteWaits, nil)
	f.setScore(.62)
	if err := f.d.s.Deny(context.Background(), pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int64{390, 1000, 1440} {
		b.do(t, chromedp.EmulateViewport(width, 900))
		b.open(t, "/?refresh=off")
		got := eval[[]any](t, b, `(() => {
			document.querySelector("details.recent").open = true;
			const row = document.querySelector(".recent li.row"), score = row.querySelector(".score").getBoundingClientRect(), meta = row.querySelector(".meta").getBoundingClientRect();
			const overlap = score.left < meta.right && meta.left < score.right && score.top < meta.bottom && meta.top < score.bottom;
			return [row.querySelector(".score").innerText.trim(), score.width, overlap];
		})()`)
		if shown, _ := got[0].(string); !strings.HasPrefix(shown, "▲0.62") || got[1].(float64) == 0 || got[2] != false {
			t.Errorf("%dpx: Recents score %q, %vpx wide, on top of the action %v", width, got[0], got[1], got[2])
		}
	}
}

// frameBody is the text of the Rendered frame and its data-ran and data-onerror marks, joined by "|". Chrome runs the
// sandboxed frame in its own process, as a target of its own; a browser that keeps it in the page's process shows it
// in the page's frame tree. ok is false when neither has the frame.
func frameBody(t *testing.T, b *consoleBrowser) (shown string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, browserTimeout)
	defer cancel()
	if frame, ok := frameTarget(t, b); ok {
		ctx, cancel := context.WithTimeout(frame, browserTimeout)
		defer cancel()
		shown, err := chromedp.Run(ctx, chromedp.Evaluate[string](`[document.body.innerText.trim(), document.body.dataset.ran, document.body.dataset.onerror].join("|")`))
		if err != nil {
			t.Fatal(err)
		}
		return shown, true
	}
	doc, err := chromedp.Call(ctx, dom.GetDocument, dom.GetDocumentParams{Depth: new(int64(-1)), Pierce: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	body := frameBodyNode(doc.Root, false)
	if body == nil {
		return "", false
	}
	var text []string
	var walk func(*cdp.Node)
	walk = func(n *cdp.Node) {
		if n.NodeType == textNode {
			text = append(text, strings.TrimSpace(n.NodeValue))
		}
		if n.NodeName != "SCRIPT" {
			for _, c := range n.Children {
				walk(c)
			}
		}
	}
	walk(body)
	attr := func(name string) string {
		for i := 0; i+1 < len(body.Attributes); i += 2 {
			if body.Attributes[i] == name {
				return body.Attributes[i+1]
			}
		}
		return ""
	}
	return strings.Join([]string{strings.TrimSpace(strings.Join(text, " ")), attr("data-ran"), attr("data-onerror")}, "|"), true
}

// textNode is the DOM nodeType of a text node.
const textNode = 3

// frameBodyNode is the BODY of the first frame document in the tree of n, inside a frame when framed is true.
func frameBodyNode(n *cdp.Node, framed bool) *cdp.Node {
	if n == nil {
		return nil
	}
	if framed && n.NodeName == "BODY" {
		return n
	}
	for _, c := range n.Children {
		if body := frameBodyNode(c, framed); body != nil {
			return body
		}
	}
	return frameBodyNode(n.ContentDocument, true)
}

// The page's policy refuses every HTML write from script, and the violation reaches the console, so that the other
// browser tests see every violation.
func TestBrowserConsoleRefusesHTMLWritesFromScript(t *testing.T) {
	_, b := browserConsole(t, "", func(m string) bool {
		return strings.Contains(m, "require-trusted-types-for") || strings.Contains(m, "This document requires 'TrustedHTML' assignment.")
	})
	b.open(t, "/?refresh=off")
	got := eval[string](t, b, `(() => { try { document.querySelector(".foot").innerHTML = "<b>x</b>"; return "written"; } catch (e) { return e.name; } })()`)
	if got != "TypeError" {
		t.Fatalf("innerHTML write: %s", got)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		b.mu.Lock()
		seen := slices.ContainsFunc(b.messages, func(m string) bool { return strings.Contains(m, "securitypolicyviolation require-trusted-types-for") })
		b.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no violation reached the console")
		}
	}
}
