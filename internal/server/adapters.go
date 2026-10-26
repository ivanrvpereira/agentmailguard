package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Adapters can execute inside provider/Guard workers, outside the HTTP goroutine's recovery.
// Catch a synchronous external-seam panic where it occurs, without formatting its value.
func recoverAdapter(err *error) {
	if recover() != nil {
		*err = errors.New("AdapterError")
	}
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

func (s safeScreener) CheckAction(ctx context.Context, operation, effect string, state map[string]any) (v model.ActionCheck, err error) {
	defer recoverAdapter(&err)
	return s.screen.CheckAction(ctx, operation, effect, state)
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
