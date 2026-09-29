package guard

import (
	"context"
	"slices"

	"github.com/ivanrvpereira/agentmailguard-go/internal/audit"
	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func checkIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > 50 {
		return GuardError("give between 1 and 50 message IDs")
	}
	return nil
}
func (g *Guard) acquire(ctx context.Context) error { return acquire(ctx, g.mutation) }
func acquire(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *Guard) release() { <-g.mutation }

// Normalize at the external boundary: unsolicited IDs never enter the audit, and duplicates cannot prove success.
func normalize(ids []string, results []model.MutationResult, organizer bool) []model.MutationResult {
	counts := map[string]int{}
	byID := map[string]model.MutationResult{}
	for _, r := range results {
		counts[r.ID]++
		byID[r.ID] = r
	}
	out := make([]model.MutationResult, 0, len(ids))
	for _, id := range ids {
		r := byID[id]
		r.ID = id
		if counts[id] != 1 {
			r.OK = false
			r.Error = pointer("failed")
		}
		if r.OK {
			r.Error = nil
		} else if organizer || r.Error == nil {
			r.Error = pointer("failed")
		} else {
			r.Error = safeText(r.Error)
		}
		out = append(out, r)
	}
	return out
}
func (g *Guard) audited(ctx context.Context, op audit.Operation, ids []string, opts audit.Options, call func() ([]model.MutationResult, error)) ([]model.MutationResult, error) {
	results, err := call()
	if err != nil {
		if opts.Rule != nil {
			failed := make([]model.MutationResult, 0, len(ids))
			for _, id := range ids {
				failed = append(failed, model.MutationResult{ID: id, Error: pointer(safeCode(err))})
			}
			if e := g.log.Record(context.WithoutCancel(ctx), g.account, op, failed, opts); e != nil {
				return nil, GuardError("AuditError")
			}
		}
		// Only these fixed refusals are public prose; external GuardError text is not trusted.
		if opts.Rule == nil && ctx.Err() == nil {
			switch err {
			case GuardError("move refused: the destination must be a folder ID from `folders`"),
				GuardError("flag state must be one of flagged, complete, none"),
				GuardError("categorize needs at least one category to add or remove"),
				GuardError("categorize refused: not in the category list"):
				return nil, err
			}
		}
		return nil, safeErr(ctx, err)
	}
	results = normalize(ids, results, opts.Rule != nil)
	// Once a provider has returned a write result, cancellation must not erase its audit record.
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, results, opts); err != nil {
		return nil, GuardError("AuditError")
	}
	return results, nil
}
func (g *Guard) mutationCall(ctx context.Context, op audit.Operation, ids []string, opts audit.Options) ([]model.MutationResult, error) {
	if err := checkIDs(ids); err != nil {
		return nil, err
	}
	return g.audited(ctx, op, ids, opts, func() ([]model.MutationResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var call func([]string) ([]model.MutationResult, error)
		switch op {
		case audit.Archive:
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Archive(ctx, ids) }
		case audit.Trash:
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Trash(ctx, ids) }
		case audit.Move:
			folders, err := g.mailbox.Folders(ctx)
			if err != nil {
				return nil, err
			}
			found := false
			for _, f := range folders {
				found = found || f.ID == *opts.Folder
			}
			if !found {
				return nil, GuardError("move refused: the destination must be a folder ID from `folders`")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Move(ctx, ids, *opts.Folder) }
		case audit.Read, audit.Unread:
			call = func(ids []string) ([]model.MutationResult, error) {
				return g.mailbox.MarkRead(ctx, ids, op == audit.Read)
			}
		case audit.Flag:
			if opts.Flag == nil || (*opts.Flag != model.Flagged && *opts.Flag != model.Complete && *opts.Flag != model.NoFlag) {
				return nil, GuardError("flag state must be one of flagged, complete, none")
			}
			call = func(ids []string) ([]model.MutationResult, error) { return g.mailbox.Flag(ctx, ids, *opts.Flag) }
		case audit.Categorize:
			if len(opts.Add)+len(opts.Remove) == 0 {
				return nil, GuardError("categorize needs at least one category to add or remove")
			}
			categories, err := g.mailbox.Categories(ctx)
			if err != nil {
				return nil, err
			}
			for _, names := range [][]string{opts.Add, opts.Remove} {
				for _, name := range names {
					if !slices.Contains(categories, name) {
						return nil, GuardError("categorize refused: not in the category list")
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			call = func(ids []string) ([]model.MutationResult, error) {
				return g.mailbox.Categorize(ctx, ids, opts.Add, opts.Remove)
			}
		default:
			return nil, GuardError("invalid operation")
		}
		if opts.Rule == nil {
			return call(ids)
		}
		return g.authorizedWrite(ctx, ids, *opts.Rule, call)
	})
}

// Validation above may wait on provider/auth permits. Only the final verdict check and write hold the security gate.
// The caller already owns mutation; security writers never acquire mutation in the reverse order.
func (g *Guard) authorizedWrite(ctx context.Context, ids []string, rule string, call func([]string) ([]model.MutationResult, error)) ([]model.MutationResult, error) {
	if err := acquire(ctx, g.security); err != nil {
		return nil, err
	}
	defer func() { <-g.security }()
	rows, err := g.cached(ctx, ids)
	if err != nil {
		return nil, err
	}
	verdict := model.Green
	switch rule {
	case "security:yellow":
		verdict = model.Yellow
	case "security:red":
		verdict = model.Red
	case "security:sealed":
		verdict = model.Sealed
	}
	allowed := []string{}
	results := []model.MutationResult{}
	for _, id := range ids {
		if row, ok := rows[id]; ok && row.Verdict == verdict {
			allowed = append(allowed, id)
		} else {
			results = append(results, model.MutationResult{ID: id, Error: pointer("failed")})
		}
	}
	if len(allowed) != 0 {
		written, err := call(allowed)
		if err != nil {
			return nil, err
		}
		results = append(results, normalize(allowed, written, true)...)
	}
	return results, nil
}
func (g *Guard) manual(ctx context.Context, op audit.Operation, ids []string, opts audit.Options) ([]model.MutationResult, error) {
	if err := checkIDs(ids); err != nil {
		return nil, err
	}
	if err := g.acquire(ctx); err != nil {
		return nil, err
	}
	defer g.release()
	return g.mutationCall(ctx, op, ids, opts)
}
func (g *Guard) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Archive, ids, audit.Options{})
}
func (g *Guard) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Trash, ids, audit.Options{})
}
func (g *Guard) Move(ctx context.Context, ids []string, dest string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Move, ids, audit.Options{Folder: &dest})
}
func (g *Guard) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	op := audit.Unread
	if read {
		op = audit.Read
	}
	return g.manual(ctx, op, ids, audit.Options{})
}
func (g *Guard) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Flag, ids, audit.Options{Flag: &state})
}
func (g *Guard) Categorize(ctx context.Context, ids, add, remove []string) ([]model.MutationResult, error) {
	return g.manual(ctx, audit.Categorize, ids, audit.Options{Add: append([]string{}, add...), Remove: append([]string{}, remove...)})
}
func (g *Guard) draft(ctx context.Context, op audit.Operation, source string, call func() (string, error)) (string, error) {
	if err := g.acquire(ctx); err != nil {
		return "", err
	}
	defer g.release()
	id, err := call()
	if err != nil {
		return "", safeErr(ctx, err)
	}
	if id == "" {
		return "", GuardError("ValueError")
	}
	opts := audit.Options{}
	if op == audit.DraftNew {
		source = id
	} else {
		opts.DraftID = &id
	}
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, []model.MutationResult{{ID: source, OK: true}}, opts); err != nil {
		return "", GuardError("AuditError")
	}
	return id, nil
}
func (g *Guard) DraftNew(ctx context.Context, draft model.NewDraft) (string, error) {
	return g.draft(ctx, audit.DraftNew, "", func() (string, error) { return g.mailbox.DraftNew(ctx, draft) })
}
func (g *Guard) DraftReply(ctx context.Context, id, comment string) (string, error) {
	return g.draft(ctx, audit.DraftReply, id, func() (string, error) { return g.mailbox.DraftReply(ctx, id, comment) })
}
func (g *Guard) DraftForward(ctx context.Context, id string, to []string) (string, error) {
	return g.draft(ctx, audit.DraftForward, id, func() (string, error) { return g.mailbox.DraftForward(ctx, id, to) })
}
func (g *Guard) DraftUpdate(ctx context.Context, id, body string) (string, error) {
	return g.draft(ctx, audit.DraftUpdate, id, func() (string, error) { return g.mailbox.DraftUpdate(ctx, id, body) })
}
