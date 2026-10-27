package server

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Approval errors. Their text is fixed and safe to show.
var (
	ErrApprovalNotFound = errors.New("approval not found")
	ErrAlreadyHandled   = errors.New("already handled")
	ErrUnavailable      = errors.New("daemon unavailable")
)

// Approve claims a stored action and runs it once through the handler of the original call, without a
// second policy check. It returns ErrApprovalNotFound or ErrAlreadyHandled when nothing runs, and the
// safe code of a run that failed. There are no retries.
func (s *Server) Approve(ctx context.Context, id string) error {
	ctx, done, err := s.admit(ctx)
	if err != nil {
		return err
	}
	defer done()
	item, err := s.cache.ClaimPending(ctx, id, s.options.Now())
	if err != nil {
		return pendingError(err)
	}
	// An action runs only after its approved record. The record also limits the run to policy operations.
	if err := s.audit.Approval(context.WithoutCancel(ctx), item.Account, item.Op, "approved", item.ID); err != nil {
		return s.finish(ctx, item, nil, guard.GuardError("AuditError"))
	}
	result, err := s.runOn(guard.WithApproval(ctx), item.Op, item.Account, item.Args)
	return s.finish(ctx, item, storedResult(result), err)
}

// storedResult is the part of a run result that the pending store keeps: provider IDs, never mail content. A
// draft result, also of an update, keeps only its draft and thread IDs.
func storedResult(result any) any {
	switch d := result.(type) {
	case newDraftDTO:
		return d.draftIDsDTO
	case updatedDraftDTO:
		return d.draftIDsDTO
	}
	return result
}

// errAccountGone is the code of an approved action whose account is no longer configured. It never runs on
// another account: actions are stored with the account key, the email.
const errAccountGone = guard.GuardError("UnknownAccountError")

// runOn runs an account operation on the account with the key, on the current state.
func (s *Server) runOn(ctx context.Context, op, key string, params json.RawMessage) (any, error) {
	st := s.current.Load()
	if st == nil {
		return nil, ErrUnavailable
	}
	o, ok := operations[op]
	if !ok || o.decode == nil {
		return nil, errInvalid
	}
	if st.accounts[key] == nil {
		return nil, errAccountGone
	}
	return s.accountCall(ctx, st, o, request{Operation: op, key: key, Params: params})
}

// finish stores the outcome of a run: done with the JSON result, or failed with a safe code. The run is over,
// so the outcome and its record do not depend on the caller staying. A failure returns its code as the error.
func (s *Server) finish(ctx context.Context, item cache.Pending, result any, err error) error {
	ctx = context.WithoutCancel(ctx)
	code := ""
	var data []byte
	if err != nil {
		code = guard.SafeCode(err)
	} else if code = failedResult(result); code == "" {
		if data, err = json.Marshal(result); err != nil {
			code = "ValueError"
		}
	}
	state, record := cache.StateDone, "done"
	if code != "" {
		state, record, data = cache.StateFailed, "failed", []byte(code)
	}
	if err := s.cache.FinishPending(ctx, item.ID, state, string(data), s.options.Now()); err != nil {
		return errors.New("approval unavailable")
	}
	if err := s.audit.Approval(ctx, item.Account, item.Op, record, item.ID); err != nil {
		return guard.GuardError("AuditError")
	}
	if code != "" {
		return guard.GuardError(code)
	}
	return nil
}

// failedResult gives the safe code of a result that reports a failure without an error: a trash result
// whose one ID is not ok. An approved run is never held again, so a pending result fails closed.
func failedResult(result any) string {
	switch v := result.(type) {
	case pendingDTO:
		return "Error"
	case []mutationDTO:
		for _, r := range v {
			if !r.OK {
				if r.Error == nil || *r.Error == "" || model.ValidateFailureText(r.Error) != nil {
					return "Error"
				}
				return *r.Error
			}
		}
	}
	return ""
}

// Deny refuses a stored action. Nothing runs.
func (s *Server) Deny(ctx context.Context, id string) error {
	ctx, done, err := s.admit(ctx)
	if err != nil {
		return err
	}
	defer done()
	item, err := s.cache.DenyPending(ctx, id, s.options.Now())
	if err != nil {
		return pendingError(err)
	}
	// The item is denied; its record must not depend on the caller staying.
	return s.audit.Approval(context.WithoutCancel(ctx), item.Account, item.Op, "denied", item.ID)
}

var approvalIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// approvalStatusDTO is the public state of a stored action. It never carries the stored arguments, the
// account, or a failure code; Result is the stored result of a done action.
type approvalStatusDTO struct {
	State  cache.PendingState `json:"state"`
	Result json.RawMessage    `json:"result,omitempty"`
}

// approvalStatus reports the state of one stored action. It is not account-scoped.
func approvalStatus(ctx context.Context, s *Server, _ *state, params json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if strict(params, &p, "id") != nil || !approvalIDPattern.MatchString(p.ID) {
		return nil, errInvalid
	}
	item, err := s.cache.GetPending(ctx, p.ID)
	if err != nil {
		return nil, pendingError(err)
	}
	out := approvalStatusDTO{State: item.State}
	if item.State == cache.StateDone && item.Result != nil {
		out.Result = json.RawMessage(*item.Result)
	}
	return out, nil
}

// pendingError maps the pending store errors to the approval errors.
func pendingError(err error) error {
	switch {
	case errors.Is(err, cache.ErrPendingNotFound):
		return ErrApprovalNotFound
	case errors.Is(err, cache.ErrPendingHandled):
		return ErrAlreadyHandled
	}
	return err
}
