package guard

import (
	"context"
	"slices"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

// The caller already owns the account mutation gate. Internal organizer actions must not acquire it again.
func (g *Guard) batches(ctx context.Context, op audit.Operation, ids []string, opts audit.Options) map[string]string {
	outcome := map[string]string{}
	for start := 0; start < len(ids); start += 50 {
		if err := ctx.Err(); err != nil {
			for _, id := range ids[start:] {
				outcome[id] = safeCode(err)
			}
			break
		}
		chunk := ids[start:min(start+50, len(ids))]
		results, err := g.mutationCall(ctx, op, chunk, opts)
		if err != nil {
			for _, id := range chunk {
				outcome[id] = safeCode(err)
			}
			continue
		}
		for _, r := range results {
			code := "failed"
			if r.OK {
				code = "ok"
			} else if r.Error != nil && (*r.Error == audit.Pending || *r.Error == string(refused(keyTrash))) {
				// Provider results are normalized to failed for rules, so only policy results carry these codes.
				code = *r.Error
			}
			outcome[r.ID] = code
		}
	}
	return outcome
}
func (g *Guard) act(ctx context.Context, rule rules.Rule, ids []string) map[string]string {
	op := audit.Move
	opts := audit.Options{Rule: &rule.Name}
	if rule.Kind == rules.Category {
		op = audit.Categorize
		opts.Add = []string{rule.Target}
		opts.Remove = []string{}
	} else if rule.Target == "archive" {
		op = audit.Archive
	} else if rule.Target == "trash" {
		op = audit.Trash
	} else {
		opts.Folder = &rule.Target
	}
	return g.batches(ctx, op, ids, opts)
}
func (g *Guard) apply(ctx context.Context, lines []*Plan, r rules.Rules, resolved map[string]*string) (map[string]bool, map[string]bool) {
	added := map[string]map[string]string{}
	broken := map[string]bool{}
	for _, line := range lines {
		added[line.ID] = map[string]string{}
	}
	for _, rule := range r.Rules {
		if rule.Kind != rules.Category {
			continue
		}
		ids := []string{}
		for _, line := range lines {
			if slices.Contains(line.Categories, rule.Target) {
				ids = append(ids, line.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		outcomes := map[string]string{}
		if resolved[rule.Name] == nil {
			for _, id := range ids {
				outcomes[id] = "category not found"
			}
		} else {
			outcomes = g.act(ctx, rule, ids)
		}
		for id, code := range outcomes {
			added[id][rule.Target] = code
			if code != "ok" && code != "category not found" {
				broken[id] = true
			}
		}
	}
	moved := map[string]string{}
	ready := map[string][]string{}
	for _, line := range lines {
		if line.Move == nil {
			continue
		}
		id := line.ID
		move := line.Move
		blocked := false
		for _, code := range added[id] {
			blocked = blocked || code != "ok"
		}
		switch {
		case move.To == nil:
			moved[id] = "folder not found"
		case move.After != nil:
			moved[id] = "too young"
		case blocked:
			moved[id] = "blocked by category"
		default:
			ready[move.Rule] = append(ready[move.Rule], id)
		}
	}
	for _, rule := range r.Rules {
		if ids := ready[rule.Name]; len(ids) > 0 {
			for id, code := range g.act(ctx, rule, ids) {
				moved[id] = code
				if code != "ok" {
					broken[id] = true
				}
			}
		}
	}
	done := map[string]bool{}
	for _, line := range lines {
		results := added[line.ID]
		line.CategoryResults = &results
		complete := true
		for _, code := range results {
			complete = complete && code == "ok"
		}
		if line.Move != nil {
			line.Move.Result = pointer(moved[line.ID])
			complete = complete && moved[line.ID] == "ok"
		}
		if complete {
			done[line.ID] = true
		}
	}
	return done, broken
}
