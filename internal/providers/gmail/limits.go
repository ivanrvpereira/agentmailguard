package gmail

import "github.com/ivanrvpereira/agentmailguard/internal/model"

// limits are Gmail's: a star has no completed state, Google sends no cancellation message, and Google Calendar always
// notifies the organizer of a response (https://support.google.com/calendar/answer/37135).
var limits = model.Limits{NoFlagComplete: true, NoCancelComment: true, NoSilentResponse: true}

func (*mailbox) Limits() model.Limits  { return limits }
func (*calendar) Limits() model.Limits { return limits }
