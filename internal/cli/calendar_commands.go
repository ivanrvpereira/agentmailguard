package cli

import (
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/spf13/cobra"
)

const wallClock = "2006-01-02 15:04"

// now is the client clock; its location is the client time zone for every calendar command.
func (r *runner) now() time.Time {
	if r.opts.Now == nil {
		return time.Now()
	}
	return r.opts.Now()
}

// localStamp parses a local wall-clock time. The round trip refuses input that Go would normalize,
// such as a time inside a DST gap or a single-digit hour.
func localStamp(v string, loc *time.Location) (time.Time, bool) {
	t, e := time.ParseInLocation(wallClock, v, loc)
	return t, e == nil && t.Format(wallClock) == v && model.CalendarTimeValid(t)
}

// meetingEnd resolves exactly one of --end and --duration against start.
func meetingEnd(c *cobra.Command, start time.Time, end string, duration time.Duration, loc *time.Location) (time.Time, bool) {
	if c.Flags().Changed("end") == c.Flags().Changed("duration") {
		return time.Time{}, false
	}
	until, ok := start.Add(duration), duration > 0 && model.CalendarTimeValid(start.Add(duration))
	if c.Flags().Changed("end") {
		until, ok = localStamp(end, loc)
	}
	return until, ok && until.After(start)
}

// calendarAvailabilityCmd mirrors the server limits, which are Microsoft Graph's: 1-20 distinct addresses and a window
// shorter than 62 days.
func calendarAvailabilityCmd(r *runner) *cobra.Command {
	var to []string
	var start, end string
	c := r.leaf("availability", "Show 30-minute M365 or fake free/busy slots; read-only, sends nothing", 0, 0, func(c *cobra.Command, _ []string) error {
		loc := r.now().Location()
		from, ok := localStamp(start, loc)
		until, ok2 := localStamp(end, loc)
		if !ok || !ok2 || !model.ValidAvailabilityRequest(to, from, until) {
			return errUsage
		}
		return r.call(c, "calendar.availability", map[string]any{"to": to, "start": from.Format(time.RFC3339), "end": until.Format(time.RFC3339)}, false)
	})
	c.Long = `Show the free/busy status of each address in 30-minute slots from --start to --end.
This command is read-only: it sends nothing and reserves nothing.
Slot i starts at start + 30*i minutes. The last slot ends at end.
Statuses: free, tentative, busy, out_of_office, unknown. M365 reports working elsewhere as free.
unknown means that no data was available, for example because of sharing permissions.
A slot is free for the meeting only when every address is free.`
	c.Flags().StringArrayVar(&to, "to", nil, "participant address; repeatable, at most 20")
	c.Flags().StringVar(&start, "start", "", "local start as 'YYYY-MM-DD HH:MM'")
	c.Flags().StringVar(&end, "end", "", "local end as 'YYYY-MM-DD HH:MM'; less than 62 days after the start")
	return c
}

func calendarCancelCmd(r *runner) *cobra.Command {
	comment := ""
	c := r.leaf("cancel ID", "Cancel your own meeting and notify the attendees (write policy applies)", 1, 1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"id": args[0]}
		optional(c, p, "comment", &comment)
		return r.call(c, "calendar.cancel", p, false)
	})
	c.Flags().StringVar(&comment, "comment", "", "message sent with the cancellation; refused on Gmail, which sends no cancellation message")
	return c
}

func calendarRespondCmd(r *runner) *cobra.Command {
	response, comment, noNotify := "", "", false
	c := r.leaf("respond ID", "Send your response to a meeting invitation (write policy applies)", 1, 1, func(c *cobra.Command, args []string) error {
		if !(model.EventResponse{Response: model.MeetingResponse(response), Comment: comment, NoNotify: noNotify}).Valid() {
			return errUsage
		}
		p := map[string]any{"id": args[0], "response": response}
		optional(c, p, "comment", &comment)
		if noNotify {
			p["no_notify"] = true
		}
		return r.call(c, "calendar.respond", p, false)
	})
	c.Flags().StringVar(&response, "response", "", "accept, tentative, or decline")
	c.Flags().StringVar(&comment, "comment", "", "message sent to the organizer with the response; not with --no-notify")
	c.Flags().BoolVar(&noNotify, "no-notify", false, "record the response without notifying the organizer; refused on Gmail, which always notifies")
	return c
}

func calendarUpdateCmd(r *runner) *cobra.Command {
	var add, remove []string
	var subject, location, body, start, end string
	var duration time.Duration
	online := false
	c := r.leaf("update ID", "Change your own meeting and send the update (write policy applies)", 1, 1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"id": args[0]}
		optional(c, p, "subject", &subject)
		optional(c, p, "location", &location)
		optional(c, p, "body", &body)
		loc, startSet, endSet, durationSet := r.now().Location(), c.Flags().Changed("start"), c.Flags().Changed("end"), c.Flags().Changed("duration")
		if endSet && durationSet {
			return errUsage
		}
		// With neither --end nor --duration, the server keeps the current duration.
		var from time.Time
		if startSet {
			var ok bool
			if from, ok = localStamp(start, loc); !ok {
				return errUsage
			}
			p["start"] = from.Format(time.RFC3339)
		}
		if endSet {
			until, ok := localStamp(end, loc)
			if !ok || startSet && !until.After(from) {
				return errUsage
			}
			p["end"] = until.Format(time.RFC3339)
		}
		if durationSet {
			if duration <= 0 || startSet && !model.CalendarTimeValid(from.Add(duration)) {
				return errUsage
			}
			p["duration"] = duration.String()
		}
		if len(add) > 0 {
			p["add"] = add
		}
		if len(remove) > 0 {
			p["remove"] = remove
		}
		if c.Flags().Changed("online") {
			// Updates only add an online meeting; removing an existing meeting link is not supported.
			if !online {
				return errUsage
			}
			p["online"] = true
		}
		if len(p) == 1 {
			return errUsage
		}
		return r.call(c, "calendar.update", p, false)
	})
	c.Flags().StringVar(&subject, "subject", "", "new meeting subject")
	c.Flags().StringVar(&location, "location", "", "new meeting location")
	c.Flags().StringVar(&body, "body", "", "new meeting body text; replaces the whole body")
	c.Flags().StringVar(&start, "start", "", "new local start as 'YYYY-MM-DD HH:MM'; keeps the duration without --end or --duration")
	c.Flags().StringVar(&end, "end", "", "new local end as 'YYYY-MM-DD HH:MM'")
	c.Flags().DurationVar(&duration, "duration", 0, "new meeting length, for example 30m or 1h30m")
	c.Flags().StringArrayVar(&add, "add-attendee", nil, "attendee address to add; repeatable")
	c.Flags().StringArrayVar(&remove, "remove-attendee", nil, "attendee address to remove; repeatable")
	c.Flags().BoolVar(&online, "online", false, "add an online meeting link (Teams on Microsoft 365, Google Meet on Gmail) if the event has none; cannot remove one")
	return c
}

func calendarInviteCmd(r *runner) *cobra.Command {
	var to, optionalTo []string
	var subject, body, location, start, end string
	var duration time.Duration
	online := true
	c := r.leaf("invite", "Create a meeting, with an online meeting link by default, and send the invitation (write policy applies)", 0, 0, func(c *cobra.Command, _ []string) error {
		if len(to) == 0 || !c.Flags().Changed("subject") || !c.Flags().Changed("start") {
			return errUsage
		}
		loc := r.now().Location()
		from, ok := localStamp(start, loc)
		if !ok {
			return errUsage
		}
		until, ok := meetingEnd(c, from, end, duration, loc)
		if !ok {
			return errUsage
		}
		p := map[string]any{"to": to, "subject": subject, "body": body, "start": from.Format(time.RFC3339), "end": until.Format(time.RFC3339)}
		if len(optionalTo) > 0 {
			p["optional"] = optionalTo
		}
		optional(c, p, "location", &location)
		p["online"] = online
		return r.call(c, "calendar.invite", p, false)
	})
	c.Flags().StringArrayVar(&to, "to", nil, "attendee address; repeatable")
	c.Flags().StringArrayVar(&optionalTo, "optional", nil, "optional attendee address, who also gets the invitation; repeatable, not also in --to")
	c.Flags().StringVar(&subject, "subject", "", "meeting subject")
	c.Flags().StringVar(&body, "body", "", "meeting body text")
	c.Flags().StringVar(&location, "location", "", "meeting location")
	c.Flags().StringVar(&start, "start", "", "local start as 'YYYY-MM-DD HH:MM'")
	c.Flags().StringVar(&end, "end", "", "local end as 'YYYY-MM-DD HH:MM'")
	c.Flags().DurationVar(&duration, "duration", 0, "meeting length, for example 30m or 1h30m")
	negated(c, "online", "add an online meeting link (Teams on Microsoft 365, Google Meet on Gmail); use --no-online for none", &online)
	return c
}
