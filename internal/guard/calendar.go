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
	v, _, err := g.event(ctx, event, categories, model.EventFilter{})
	return v, err
}
func (g *Guard) CalendarList(ctx context.Context, start, end time.Time, filter model.EventFilter) ([]views.CalendarView, error) {
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
	matched := make([]bool, len(events))
	err = parallelItems(ctx, len(events), func(i int) error {
		if events[i].ID == "" {
			return GuardError("ValueError")
		}
		var err error
		out[i], matched[i], err = g.event(ctx, events[i], categories, filter)
		return err
	})
	if err != nil {
		return nil, err
	}
	kept := []views.CalendarView{}
	for i, v := range out {
		if matched[i] {
			kept = append(kept, v)
		}
	}
	return kept, nil
}

// event renders e and screens it when it matches filter. An event that does not match is never screened:
// it gives matched false and no view.
func (g *Guard) event(ctx context.Context, e model.Event, categories []string, filter model.EventFilter) (v views.CalendarView, matched bool, err error) {
	if err := ctx.Err(); err != nil {
		return views.CalendarView{}, false, err
	}
	var r *content.Rendered
	body, rendered := "", true
	if !e.IsPrivate {
		out, err := render(e.Body, e.ContentType)
		if rendered = err == nil; rendered {
			r, body = &out, out.Markdown
		}
	}
	if !filter.Match(e, body) {
		return views.CalendarView{}, false, nil
	}
	if !rendered {
		v, err = views.EventView(e, failure("render ValueError"), nil, categories)
		return v, true, err
	}
	s, err := g.screener.Screen(ctx, views.EventScreeningState(e, r))
	if ctx.Err() != nil {
		return views.CalendarView{}, false, ctx.Err()
	}
	if err != nil {
		s = failure(safeCode(err))
	}
	v, err = views.EventView(e, validScreen(s), r, categories)
	return v, true, err
}
