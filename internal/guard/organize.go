package guard

import (
	"context"
	"slices"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/content"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/rules"
)

type Warning struct {
	Rule    string `json:"rule"`
	Problem string `json:"problem"`
}
type ReportError struct {
	ID    *string `json:"id"`
	Error string  `json:"error"`
}
type MovePlan struct {
	Rule   string  `json:"rule"`
	To     *string `json:"to"`
	After  *string `json:"after,omitempty"`
	Result *string `json:"result,omitempty"`
}
type Plan struct {
	ID              string             `json:"id"`
	Move            *MovePlan          `json:"move"`
	Categories      []string           `json:"categories"`
	CategoryResults *map[string]string `json:"category_results,omitempty"`
}
type Report struct {
	Since    string                           `json:"since"`
	Until    *string                          `json:"until,omitempty"`
	Counts   map[string]map[model.Verdict]int `json:"counts"`
	Warnings []Warning                        `json:"warnings"`
	Errors   []ReportError                    `json:"errors"`
	Planned  []Plan                           `json:"planned"`
}

func stamp(at time.Time) string {
	layout := "2006-01-02T15:04:05Z"
	if at.Nanosecond() != 0 {
		layout = "2006-01-02T15:04:05.000000Z"
		if at.Nanosecond()%1000 != 0 {
			layout = "2006-01-02T15:04:05.000000000Z"
		}
	}
	return at.UTC().Format(layout)
}
func noWork(since time.Time, until *time.Time) Report {
	report := Report{Since: stamp(since), Counts: map[string]map[model.Verdict]int{}, Warnings: []Warning{}, Errors: []ReportError{}, Planned: []Plan{}}
	if until != nil {
		report.Until = pointer(stamp(*until))
	}
	for _, name := range []string{"screened", "classified", "cached", "applied", "tagged", "skipped", "failed"} {
		report.Counts[name] = map[model.Verdict]int{model.Green: 0, model.Yellow: 0, model.Sealed: 0, model.Red: 0, model.Error: 0}
	}
	return report
}
func (report *Report) count(name string, row *cache.Row) {
	v := model.Error
	if row != nil {
		v = row.Verdict
	}
	report.Counts[name][v]++
}
func resolve(r rules.Rules, folders []model.Folder, categories []string) (map[string]*string, []Warning) {
	names := map[string]string{}
	for _, f := range folders {
		names[f.ID] = content.CleanSubject(f.Name)
	}
	resolved := map[string]*string{}
	warnings := []Warning{}
	for _, rule := range r.Rules {
		var target *string
		if rule.Kind == rules.Category {
			if slices.Contains(categories, rule.Target) {
				target = pointer(rule.Target)
			}
		} else if rule.Target == "archive" || rule.Target == "trash" {
			target = pointer(rule.Target)
		} else if name, ok := names[rule.Target]; ok {
			target = pointer(name)
		}
		resolved[rule.Name] = target
		if target == nil {
			warnings = append(warnings, Warning{rule.Name, string(rule.Kind) + " not found"})
		}
	}
	return resolved, warnings
}
func plan(row *cache.Row, r rules.Rules, resolved map[string]*string, now, received time.Time) (*Plan, error) {
	if row == nil || row.Verdict != model.Green || row.Organization == nil {
		return nil, nil
	}
	line := Plan{ID: row.MessageID, Categories: []string{}}
	for _, rule := range r.Rules {
		if rule.Kind == rules.Category && row.Organization.Categories[rule.Name] >= .8 {
			line.Categories = append(line.Categories, rule.Target)
		}
		if rule.Kind == rules.Folder && row.Organization.Folder != nil && row.Organization.Folder.Choice == rule.Name {
			if rule.MinAgeHours < 0 || rule.MinAgeHours > (time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Unix()-received.Unix())/3600 {
				return nil, GuardError("OverflowError")
			}
			ready := time.Unix(received.Unix()+rule.MinAgeHours*3600, int64(received.Nanosecond())).UTC()
			line.Move = &MovePlan{Rule: rule.Name, To: resolved[rule.Name]}
			if now.Before(ready) {
				line.Move.After = pointer(stamp(ready))
			}
		}
	}
	if line.Move == nil && len(line.Categories) == 0 {
		return nil, nil
	}
	return &line, nil
}
func (g *Guard) markApplied(ctx context.Context, id string, at time.Time, noop bool) error {
	if err := acquire(ctx, g.security); err != nil {
		return err
	}
	defer func() { <-g.security }()
	if noop {
		rows, err := g.cached(ctx, []string{id})
		if err != nil {
			return err
		}
		if row, ok := rows[id]; !ok || row.Verdict != model.Green || row.Organization == nil {
			return GuardError("VerdictChanged")
		}
	}
	return g.store.MarkOrganizationApplied(ctx, g.account, id, at)
}

func (g *Guard) Organize(ctx context.Context, since time.Time, r rules.Rules, now time.Time, apply bool, until *time.Time) (Report, error) {
	report := noWork(since, until)
	if until != nil && !until.After(since) {
		return report, GuardError("organize: until must be after since")
	}
	if len(r.Rules) == 0 {
		return report, nil
	}
	if apply {
		if err := g.acquire(ctx); err != nil {
			return Report{}, err
		}
		defer g.release()
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	folders, err := g.mailbox.Folders(ctx)
	if err != nil {
		return Report{}, GuardError("organize " + safeCode(err))
	}
	categories, err := g.mailbox.Categories(ctx)
	if err != nil {
		return Report{}, GuardError("organize " + safeCode(err))
	}
	scan, err := g.mailbox.ScanInbox(ctx, since, until)
	if err != nil {
		return Report{}, GuardError("organize " + safeCode(err))
	}
	resolved, warnings := resolve(r, folders, categories)
	report.Warnings = warnings
	received := map[string]time.Time{}
	ids := []string{}
	for _, entry := range scan.Messages {
		if entry.ReceivedAt.Before(since) || (until != nil && !entry.ReceivedAt.Before(*until)) {
			continue
		}
		if _, exists := received[entry.ID]; !exists {
			ids = append(ids, entry.ID)
		}
		received[entry.ID] = entry.ReceivedAt
	}
	lookup := append([]string{}, ids...)
	scanFailed := map[string]bool{}
	for _, f := range scan.Failures {
		report.Errors = append(report.Errors, ReportError{ID: f.ID, Error: *safeText(&f.Error)})
		if f.ID != nil {
			scanFailed[*f.ID] = true
			lookup = append(lookup, *f.ID)
		}
	}
	rows, err := g.cached(ctx, lookup)
	if err != nil {
		return Report{}, safeErr(ctx, err)
	}
	rowFor := func(id string) *cache.Row {
		if row, ok := rows[id]; ok {
			return &row
		}
		return nil
	}
	failed := map[string]*cache.Row{}
	for id := range scanFailed {
		failed[id] = rowFor(id)
	}
	todo := []string{}
	retag := map[string]*cache.Row{}
	for _, id := range ids {
		row := rowFor(id)
		if row == nil || row.OrganizationAppliedAt == nil {
			todo = append(todo, id)
		} else if apply && hidden(row.Verdict) && (row.SecurityAppliedVerdict == nil || *row.SecurityAppliedVerdict != row.Verdict) {
			retag[id] = row
		} else {
			report.count("skipped", row)
		}
	}
	final := map[string]*cache.Row{}
	noop := map[string]bool{}
	results, err := g.Classify(ctx, todo, r)
	if err != nil {
		return Report{}, err
	}
	for _, result := range results {
		row := result.Row
		final[result.ID] = row
		if _, exists := failed[result.ID]; exists {
			failed[result.ID] = row
		}
		if result.Screened {
			report.count("screened", row)
		}
		if result.Classified {
			report.count("classified", row)
		}
		code := result.Error
		if code == nil && row != nil && row.Verdict == model.Error {
			code = row.Error
		}
		if code != nil {
			report.Errors = append(report.Errors, ReportError{pointer(result.ID), *code})
			failed[result.ID] = row
			continue
		}
		if !result.Screened && !result.Classified {
			report.count("cached", row)
		}
		line, err := plan(row, r, resolved, now, received[result.ID])
		if err != nil {
			report.Errors = append(report.Errors, ReportError{pointer(result.ID), "plan " + safeCode(err)})
			failed[result.ID] = row
		} else if line != nil {
			report.Planned = append(report.Planned, *line)
		} else if row != nil && row.Verdict == model.Green && row.Organization != nil {
			noop[result.ID] = true
		}
	}
	if apply {
		lines := []*Plan{}
		for i := range report.Planned {
			line := &report.Planned[i]
			if _, bad := failed[line.ID]; bad {
				results := map[string]string{}
				for _, category := range line.Categories {
					results[category] = "scan failed"
				}
				line.CategoryResults = &results
				if line.Move != nil {
					line.Move.Result = pointer("scan failed")
				}
			} else {
				lines = append(lines, line)
			}
		}
		done, broken := g.apply(ctx, lines, r, resolved)
		for id := range noop {
			if _, bad := failed[id]; !bad && ctx.Err() == nil {
				done[id] = true
			}
		}
		for id, row := range retag {
			final[id] = row
		}
		if ctx.Err() == nil {
			for id := range g.tag(ctx, ids, final, scanFailed, categories, &report) {
				broken[id] = true
			}
		}
		// Confirmed final writes must survive caller cancellation; never infer completion from partial success.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, id := range ids {
			if done[id] {
				if err := g.markApplied(persistCtx, id, now, noop[id]); err != nil {
					report.Errors = append(report.Errors, ReportError{pointer(id), "cache " + safeCode(err)})
					broken[id] = true
				}
			}
		}
		for id, row := range final {
			if broken[id] {
				failed[id] = row
			} else if done[id] {
				report.count("applied", row)
			} else if _, bad := failed[id]; !bad {
				report.count("skipped", row)
			}
		}
	}
	for _, row := range failed {
		report.count("failed", row)
	}
	return report, ctx.Err()
}
