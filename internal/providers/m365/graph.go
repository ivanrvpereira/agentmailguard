package m365

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const invalidPage = model.MailboxError("Graph error InvalidPage")
const invalidMetadata = model.MailboxError("Graph error InvalidMetadata")
const requestFailed = model.MailboxError("Graph error RequestFailed")

type object = map[string]json.RawMessage

func text(d object, k string) string    { var s string; _ = json.Unmarshal(d[k], &s); return s }
func obj(d object, k string) object     { var o object; _ = json.Unmarshal(d[k], &o); return o }
func array(d object, k string) []object { var a []object; _ = json.Unmarshal(d[k], &a); return a }
func stringsAt(d object, k string) []string {
	var a []string
	_ = json.Unmarshal(d[k], &a)
	if a == nil {
		return []string{}
	}
	return a
}
func boolean(d object, k string) bool { var b bool; _ = json.Unmarshal(d[k], &b); return b }
func (c *Client) endpoint(path string, q url.Values) string {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// validURL allows the signed-in user's resources under /me, and the Microsoft Search endpoint, which searches only
// the signed-in user's items.
func (c *Client) validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	base, err := url.Parse(c.base)
	if err != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil && u.Fragment == "" && (u.EscapedPath() == base.EscapedPath()+"/me" || strings.HasPrefix(u.EscapedPath(), base.EscapedPath()+"/me/") || u.EscapedPath() == base.EscapedPath()+"/search/query" && u.RawQuery == "")
}
func (c *Client) request(ctx context.Context, method, raw string, body any, calendar bool) (object, error) {
	if !c.validURL(raw) {
		return nil, invalidPage
	}
	token, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	return c.requestToken(ctx, method, raw, body, calendar, token, c.credential)
}
func (c *Client) requestToken(ctx context.Context, method, raw string, body any, calendar bool, token credentials.OAuth, refresh func(context.Context) (credentials.OAuth, error)) (object, error) {
	if !c.validURL(raw) {
		return nil, invalidPage
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, requestFailed
		}
	}
	for {
		select {
		case c.limit <- struct{}{}:
		case <-ctx.Done():
			return nil, requestFailed
		}
		if ctx.Err() != nil {
			<-c.limit
			return nil, requestFailed
		}
		if c.now().Before(token.ExpiresAt) {
			break
		}
		// Login holds authGate while fetching its profile: never wait for auth with an API slot.
		<-c.limit
		if refresh == nil {
			return nil, authError
		}
		token, err = refresh(ctx)
		if err != nil {
			return nil, err
		}
	}
	defer func() { <-c.limit }()
	req, err := http.NewRequestWithContext(ctx, method, raw, bytes.NewReader(encoded))
	if err != nil {
		return nil, requestFailed
	}
	// A stale keep-alive must not replay writes in net/http.Transport.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	prefer := `IdType="ImmutableId"`
	if calendar {
		prefer += `, outlook.timezone="UTC"`
	}
	req.Header.Set("Prefer", prefer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, requestFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode < 100 || resp.StatusCode > 599 {
		return nil, requestFailed
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, model.MailboxError(fmt.Sprintf("Graph error %d", resp.StatusCode))
	}
	if resp.StatusCode == http.StatusNoContent {
		return object{}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, requestFailed
	}
	if len(data) == 0 {
		return object{}, nil
	}
	var out object
	if json.Unmarshal(data, &out) != nil || out == nil {
		return nil, invalidPage
	}
	return out, nil
}
func (c *Client) get(ctx context.Context, raw string, calendar bool) (object, error) {
	return c.request(ctx, http.MethodGet, raw, nil, calendar)
}
func pageItems(d object) ([]object, error) {
	var entries []object
	if len(d["value"]) == 0 || string(d["value"]) == "null" || json.Unmarshal(d["value"], &entries) != nil {
		return nil, invalidPage
	}
	return entries, nil
}

// A nextLink may change the query only, never the origin or resource path.
func (c *Client) next(d object, first string) (string, error) {
	raw, ok := d["@odata.nextLink"]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var link string
	if json.Unmarshal(raw, &link) != nil || link == "" || !c.validURL(link) {
		return "", invalidPage
	}
	u, _ := url.Parse(link)
	start, _ := url.Parse(first)
	if u.EscapedPath() != start.EscapedPath() {
		return "", invalidPage
	}
	return link, nil
}
func (c *Client) all(ctx context.Context, first string, calendar bool) ([]object, error) {
	var found []object
	seen := map[string]bool{}
	for link := first; link != ""; {
		if seen[link] || len(seen) >= 10000 {
			return nil, invalidPage
		}
		seen[link] = true
		d, err := c.get(ctx, link, calendar)
		if err != nil {
			return nil, err
		}
		items, err := pageItems(d)
		if err != nil {
			return nil, err
		}
		found = append(found, items...)
		link, err = c.next(d, first)
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}
