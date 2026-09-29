package guard

import (
	"context"
	"slices"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/audit"
	"github.com/ivanrvpereira/agentmailguard-go/internal/cache"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func (g *Guard) markSecurity(ctx context.Context, id string, verdict model.Verdict, confirmed bool) error {
	if err := acquire(ctx, g.security); err != nil {
		return err
	}
	defer func() { <-g.security }()
	if !confirmed {
		rows, err := g.cached(ctx, []string{id})
		if err != nil {
			return err
		}
		if row, ok := rows[id]; !ok || row.Verdict != verdict {
			return GuardError("VerdictChanged")
		}
	}
	return g.store.MarkSecurityApplied(ctx, g.account, id, verdict)
}

func (g *Guard) tag(ctx context.Context, ids []string, rows map[string]*cache.Row, scanFailed map[string]bool, categories []string, report *Report) map[string]bool {
	broken := map[string]bool{}
	tags := []struct {
		verdict model.Verdict
		name    string
	}{{model.Yellow, "Yellow"}, {model.Red, "Red"}, {model.Sealed, "Sealed"}}
	for _, tag := range tags {
		pending := []string{}
		for _, id := range ids {
			row := rows[id]
			if !scanFailed[id] && row != nil && row.Verdict == tag.verdict && (row.SecurityAppliedVerdict == nil || *row.SecurityAppliedVerdict != tag.verdict) {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 {
			continue
		}
		rule := "security:" + string(tag.verdict)
		if !slices.Contains(categories, tag.name) {
			report.Warnings = append(report.Warnings, Warning{rule, "category not found"})
			continue
		}
		remove := []string{}
		for _, other := range tags {
			if other.name != tag.name && slices.Contains(categories, other.name) {
				remove = append(remove, other.name)
			}
		}
		there, write := []string{}, []string{}
		codes := map[string]string{}
		errorOrder := []string{}
		fail := func(id, code string) {
			if _, exists := codes[id]; !exists {
				errorOrder = append(errorOrder, id)
			}
			codes[id] = code
		}
		for _, id := range pending {
			fetched, err := g.fetch(ctx, []string{id}, false)
			if err != nil {
				fail(id, "fetch "+safeCode(err))
				continue
			}
			m := fetched[id]
			if m.IsDraft {
				fail(id, "draft")
				continue
			}
			correct := slices.Contains(m.Categories, tag.name)
			for _, name := range remove {
				correct = correct && !slices.Contains(m.Categories, name)
			}
			if correct {
				there = append(there, id)
			} else {
				write = append(write, id)
			}
		}
		confirmed := map[string]bool{}
		outcome := g.batches(ctx, audit.Categorize, write, audit.Options{Rule: &rule, Add: []string{tag.name}, Remove: remove})
		for _, id := range write {
			if outcome[id] == "ok" {
				report.count("tagged", rows[id])
				there = append(there, id)
				confirmed[id] = true
			} else {
				fail(id, outcome[id])
			}
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		for _, id := range there {
			if err := g.markSecurity(persistCtx, id, tag.verdict, confirmed[id]); err != nil {
				fail(id, "cache "+safeCode(err))
			}
		}
		cancel()
		for _, id := range errorOrder {
			report.Errors = append(report.Errors, ReportError{pointer(id), "tag " + codes[id]})
			broken[id] = true
		}
	}
	return broken
}
