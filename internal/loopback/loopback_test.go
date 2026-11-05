package loopback_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/loopback"
)

const state = "synthetic-state"

// Each pasted address differs from the redirect of the sign-in in one part, and the callback answers 400 to the
// same request; the last paste matches and ends the wait.
func TestWaitAcceptsOnlyTheRedirectOfThisSignIn(t *testing.T) {
	c, err := loopback.Listen("localhost", state)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	port := strings.TrimPrefix(c.Host, "localhost:")
	good := "http://" + c.Host + "/?state=" + state + "&code=synthetic-code"
	wrong := []string{
		"http://127.0.0.1:" + port + "/?state=" + state + "&code=synthetic-code",
		"http://localhost:1/?state=" + state + "&code=synthetic-code",
		"http://" + c.Host + "/other?state=" + state + "&code=synthetic-code",
		"http://" + c.Host + "/?state=other&code=synthetic-code",
		"http://" + c.Host + "/?code=synthetic-code",
		"http://" + c.Host + "/?state=" + state + "&state=" + state + "&code=synthetic-code",
		"https://" + c.Host + "/?state=" + state + "&code=synthetic-code",
		"http://user@" + c.Host + "/?state=" + state + "&code=synthetic-code",
	}
	for _, address := range wrong[:6] {
		if status := get(t, c, "GET", address); status != 400 {
			t.Errorf("callback accepted %s: %d", address, status)
		}
	}
	if status := get(t, c, "POST", "http://"+c.Host+"/?state="+state+"&code=synthetic-post"); status != 400 {
		t.Errorf("callback accepted a POST: %d", status)
	}
	var retries []bool
	read := func(_ context.Context, retry bool) (string, error) {
		retries = append(retries, retry)
		if len(retries) <= len(wrong) {
			return wrong[len(retries)-1], nil
		}
		return "  " + good + "\n", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := c.Wait(ctx, "https://login.example.test/", func(context.Context, string) error { return errors.New("no browser") }, read)
	if err != nil || code != "synthetic-code" || len(retries) != len(wrong)+1 || retries[0] || !retries[1] {
		t.Fatal(code, err, retries)
	}
}

// A redirect of this sign-in with a provider error or without exactly one code ends the wait with ErrDenied.
func TestWaitEndsOnADeniedRedirect(t *testing.T) {
	for _, query := range []string{"&error=access_denied&code=synthetic-code", "&error=&error=access_denied&code=synthetic-code", "", "&code=", "&code=a&code=b"} {
		c, err := loopback.Listen("127.0.0.1", state)
		if err != nil {
			t.Fatal(err)
		}
		read := func(context.Context, bool) (string, error) {
			return "http://" + c.Host + "/?state=" + state + query, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := c.Wait(ctx, "", func(context.Context, string) error { return nil }, read); !errors.Is(err, loopback.ErrDenied) {
			t.Errorf("%q: %v", query, err)
		}
		cancel()
		c.Close()
	}
}

// Wait stops the browser and the reader and waits for them, so neither writes to the terminal after it returns.
func TestWaitStopsTheBrowserAndTheReader(t *testing.T) {
	c, err := loopback.Listen("localhost", state)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var opened, read bool
	open := func(ctx context.Context, uri string) error {
		if status := get(t, c, "GET", "http://"+c.Host+"/?state="+state+"&code=synthetic-code"); status != 200 {
			t.Errorf("callback refused: %d", status)
		}
		<-ctx.Done()
		opened = true
		return nil
	}
	reader := func(ctx context.Context, _ bool) (string, error) {
		<-ctx.Done()
		read = true
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code, err := c.Wait(ctx, "", open, reader); err != nil || code != "synthetic-code" {
		t.Fatal(code, err)
	}
	if !opened || !read {
		t.Fatal("Wait returned while the browser or the reader still ran")
	}
}

// get sends address to the callback as a browser would: to the listener, with the address's host in the Host
// header, and returns the status, or 0 when the request fails. It runs in the browser goroutine too, so it never
// calls t.Fatal.
func get(t *testing.T, c *loopback.Callback, method, address string) int {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Error(err)
		return 0
	}
	_, port, _ := strings.Cut(c.Host, ":")
	req, _ := http.NewRequest(method, "http://127.0.0.1:"+port+u.RequestURI(), nil)
	req.Host = u.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
