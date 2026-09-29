package guard

import (
	"context"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/rules"
	"github.com/ivanrvpereira/agentmailguard-go/internal/screening"
	"github.com/ivanrvpereira/agentmailguard-go/internal/views"
)

// Classified is internal organizer state, never an agent-facing view.
type Classified struct {
	ID                   string
	Row                  *cache.Row
	Screened, Classified bool
	Error                *string
}

func (g *Guard) Classify(ctx context.Context, ids []string, r rules.Rules) ([]Classified, error) {
	asked := screening.OrganizationQuestions(r)
	if asked == nil {
		return []Classified{}, nil
	}
	rows, err := g.cached(ctx, ids)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	out := make([]Classified, len(ids))
	err = parallelItems(ctx, len(ids), func(i int) error {
		id := ids[i]
		var row *cache.Row
		if saved, ok := rows[id]; ok {
			row = &saved
		}
		out[i] = g.classifyOne(ctx, id, row, asked)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (g *Guard) classifyOne(ctx context.Context, id string, row *cache.Row, asked *model.OrganizationQuestions) Classified {
	result := Classified{ID: id, Row: row}
	security := row == nil || row.Verdict == model.Error
	organize := row == nil || row.Organization == nil
	if !security && !organize {
		return result
	}
	fetched, err := g.fetch(ctx, []string{id}, true)
	if err != nil {
		result.Error = pointer("fetch " + safeCode(err))
		return result
	}
	m := fetched[id]
	if m.IsDraft {
		result.Error = pointer("draft")
		return result
	}
	result.Screened = security
	reload := func() {
		rows, err := g.cached(ctx, []string{id})
		if err != nil {
			result.Error = pointer("cache " + safeCode(err))
			return
		}
		if saved, ok := rows[id]; ok {
			result.Row = &saved
		}
	}
	put := func(s model.Screening) bool {
		if _, err := g.put(ctx, m, s); err != nil {
			result.Error = pointer("cache " + safeCode(err))
			return false
		}
		return true
	}
	rendered, err := render(m.Body, m.ContentType)
	if err != nil {
		result.Error = pointer("render ValueError")
		if put(failure("render ValueError")) {
			reload()
		}
		return result
	}
	questions := asked
	if !organize {
		questions = nil
	}
	classified, err := g.screener.Classify(ctx, views.ScreeningState(m, rendered), security, questions)
	if ctx.Err() != nil {
		result.Error = pointer(safeCode(ctx.Err()))
		return result
	}
	if err != nil {
		result.Error = pointer(safeCode(err))
		if put(failure(safeCode(err))) {
			reload()
		}
		return result
	}
	if security && classified.Screening == nil {
		classified.Screening = pointer(failure("ValidationError"))
	}
	if classified.Screening != nil {
		s := *classified.Screening
		if !security && s.Verdict != model.Error {
			s = failure("ValidationError")
		}
		if !put(s) {
			return result
		}
	}
	result.Error = safeText(classified.Error)
	if organize {
		if classified.Organization != nil {
			code := organizationError(*classified.Organization, asked)
			if code != "" {
				result.Error = pointer(code)
			} else if err := g.store.SaveOrganization(ctx, g.account, id, *classified.Organization); err != nil {
				result.Error = pointer("cache " + safeCode(err))
			} else {
				result.Classified = true
			}
		} else if result.Error == nil {
			result.Error = pointer("ValidationError")
		}
	}
	reload()
	return result
}
func organizationError(o model.Organization, asked *model.OrganizationQuestions) string {
	if (model.Organization{Folder: o.Folder}).Validate() != nil {
		return "invalid answer folder"
	}
	if (model.Organization{Categories: o.Categories}).Validate() != nil {
		return "invalid answer category"
	}
	if asked.Folder != nil {
		if o.Folder == nil || len(o.Folder.Probabilities) != len(asked.Folder.Criteria) {
			return "invalid answer folder"
		}
		for name := range asked.Folder.Criteria {
			if _, ok := o.Folder.Probabilities[name]; !ok {
				return "invalid answer folder"
			}
		}
	} else if o.Folder != nil {
		return "invalid answer folder"
	}
	if len(o.Categories) != len(asked.Categories) {
		return "invalid answer category"
	}
	for name := range asked.Categories {
		if _, ok := o.Categories[name]; !ok {
			return "invalid answer category"
		}
	}
	return ""
}
