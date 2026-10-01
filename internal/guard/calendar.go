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
	if end.Sub(start) > 90*24*time.Hour+time.Hour {
		return nil, GuardError("calendar range: at most 90 days")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := g.calendar.List(ctx, start, end)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
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
