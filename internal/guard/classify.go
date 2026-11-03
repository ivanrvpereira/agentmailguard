package guard

import (
	"context"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
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
	// A scanned message may have moved to a closed folder since the scan; one lookup serves every message.
	closed, err := g.closedFolders(ctx)
	if err != nil {
		return nil, err
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
		out[i] = g.classifyOne(ctx, id, row, asked, closed)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// classifyOne refuses a fetched message in a closed folder with the code "closed", before screening and without its
// cached row. A message with nothing to ask is not fetched; a write on it still gets the closed check of mutationCall.
func (g *Guard) classifyOne(ctx context.Context, id string, row *cache.Row, asked *model.OrganizationQuestions, closed map[string]model.Bin) Classified {
	result := Classified{ID: id, Row: row}
	security := row == nil || row.Verdict == model.Error
	organize := row == nil || row.Organization == nil
	if !security && !organize {
		return result
	}
	fetched, err := g.fetch(ctx, []string{id}, true)
	if err != nil {
		result.Error = new("fetch " + safeCode(err))
		return result
	}
	m := fetched[id]
	if messageClosed(m, closed) != nil {
		return Classified{ID: id, Error: new("closed")}
	}
	if m.IsDraft {
		result.Error = new("draft")
		return result
	}
	result.Screened = security
	reload := func() {
		rows, err := g.cached(ctx, []string{id})
		if err != nil {
			result.Error = new("cache " + safeCode(err))
			return
		}
		if saved, ok := rows[id]; ok {
			result.Row = &saved
		}
	}
	put := func(s model.Screening) bool {
		if _, err := g.put(ctx, m, s); err != nil {
			result.Error = new("cache " + safeCode(err))
			return false
		}
		return true
	}
	rendered, err := render(m.Body, m.ContentType)
	if err != nil {
		result.Error = new("render ValueError")
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
		result.Error = new(safeCode(ctx.Err()))
		return result
	}
	if err != nil {
		result.Error = new(safeCode(err))
		if put(failure(safeCode(err))) {
			reload()
		}
		return result
	}
	if security && classified.Screening == nil {
		classified.Screening = new(failure("ValidationError"))
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
				result.Error = new(code)
			} else if err := g.store.SaveOrganization(ctx, g.account, id, *classified.Organization); err != nil {
				result.Error = new("cache " + safeCode(err))
			} else {
				result.Classified = true
			}
		} else if result.Error == nil {
			result.Error = new("ValidationError")
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
