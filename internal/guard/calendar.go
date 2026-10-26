package guard

import (
	"context"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

func (g *Guard) CalendarGet(ctx context.Context, id string) (views.CalendarView, error) {
	if err := ctx.Err(); err != nil {
		return views.CalendarView{}, err
	}
	event, err := g.calendar.Get(ctx, id)
	if err != nil {
		return views.CalendarView{}, safeErr(ctx, err)
	}
	if event.ID != id {
		return views.CalendarView{}, GuardError("ValueError")
	}
	categories, err := g.Categories(ctx)
	if err != nil {
		return views.CalendarView{}, err
	}
	return g.event(ctx, event, categories)
}
func (g *Guard) CalendarList(ctx context.Context, start, end time.Time) ([]views.CalendarView, error) {
	if !end.After(start) {
		return nil, GuardError("calendar range: the end must be after the start")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := g.calendar.List(ctx, start, end)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	return g.events(ctx, events)
}

// CalendarSearch screens each event that the provider's search finds for text, as CalendarGet screens one event,
// in the provider's order.
func (g *Guard) CalendarSearch(ctx context.Context, text string) ([]views.CalendarView, error) {
	if text == "" {
		return nil, GuardError("calendar search needs text")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := g.calendar.Search(ctx, text)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.ID] {
			return nil, GuardError("ValueError")
		}
		seen[e.ID] = true
	}
	return g.events(ctx, events)
}

// events screens each event in parallel and keeps the order. An event without an ID fails the whole result.
func (g *Guard) events(ctx context.Context, events []model.Event) ([]views.CalendarView, error) {
	categories, err := g.Categories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]views.CalendarView, len(events))
	err = parallelItems(ctx, len(events), func(i int) error {
		if events[i].ID == "" {
			return GuardError("ValueError")
		}
		v, err := g.event(ctx, events[i], categories)
		out[i] = v
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (g *Guard) event(ctx context.Context, e model.Event, categories []string) (views.CalendarView, error) {
	if err := ctx.Err(); err != nil {
		return views.CalendarView{}, err
	}
	var r *content.Rendered
	if !e.IsPrivate {
		rendered, err := render(e.Body, e.ContentType)
		if err != nil {
			return views.EventView(e, failure("render ValueError"), nil, categories)
		}
		r = &rendered
	}
	s, err := g.screener.Screen(ctx, views.EventScreeningState(e, r))
	if ctx.Err() != nil {
		return views.CalendarView{}, ctx.Err()
	}
	if err != nil {
		s = failure(safeCode(err))
	}
	return views.EventView(e, validScreen(s), r, categories)
}
