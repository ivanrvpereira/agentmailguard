package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

// Adapters can execute inside provider/Guard workers, outside the HTTP goroutine's recovery.
// Catch a synchronous external-seam panic where it occurs, without formatting its value.
func recoverAdapter(err *error) {
	if recover() != nil {
		*err = errors.New("AdapterError")
	}
}

type safeStore struct{ store credentials.Store }

func (s safeStore) LoadOAuth(ctx context.Context, p, a string) (v credentials.OAuth, err error) {
	defer recoverAdapter(&err)
	if s.store == nil {
		return v, credentials.ErrUnavailable
	}
	return s.store.LoadOAuth(ctx, p, a)
}
func (s safeStore) SaveOAuth(ctx context.Context, p, a string, v credentials.OAuth) (err error) {
	defer recoverAdapter(&err)
	if s.store == nil {
		return credentials.ErrUnavailable
	}
	return s.store.SaveOAuth(ctx, p, a, v)
}
func (s safeStore) LoadOpenRouterKey(ctx context.Context) (v string, err error) {
	defer recoverAdapter(&err)
	if s.store == nil {
		return "", credentials.ErrUnavailable
	}
	return s.store.LoadOpenRouterKey(ctx)
}
func (s safeStore) SaveOpenRouterKey(ctx context.Context, v string) (err error) {
	defer recoverAdapter(&err)
	if s.store == nil {
		return credentials.ErrUnavailable
	}
	return s.store.SaveOpenRouterKey(ctx, v)
}

type safeScreener struct{ screen model.Screener }

func (s safeScreener) Screen(ctx context.Context, state map[string]any) (v model.Screening, err error) {
	defer recoverAdapter(&err)
	return s.screen.Screen(ctx, state)
}
func (s safeScreener) Classify(ctx context.Context, state map[string]any, security bool, q *model.OrganizationQuestions) (v model.Classification, err error) {
	defer recoverAdapter(&err)
	return s.screen.Classify(ctx, state, security, q)
}

type safeTransport struct{ transport http.RoundTripper }

func (s safeTransport) RoundTrip(r *http.Request) (v *http.Response, err error) {
	defer recoverAdapter(&err)
	return s.transport.RoundTrip(r)
}
func safeHTTP(client *http.Client) *http.Client {
	h := http.Client{}
	if client != nil {
		h = *client
	}
	transport := h.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	h.Transport = safeTransport{transport}
	return &h
}
