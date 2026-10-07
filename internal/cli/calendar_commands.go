package cli

import (
	"time"

	"github.com/spf13/cobra"
)

const wallClock = "2006-01-02 15:04"

// location is the client time zone; calendar list and the write commands share it.
func (r *runner) location() *time.Location {
	if r.opts.Now == nil {
		r.opts.Now = time.Now
	}
	return r.opts.Now().Location()
}

// localStamp parses a local wall-clock time. The round trip refuses input that Go would normalize,
// such as a time inside a DST gap or a single-digit hour.
func localStamp(v string, loc *time.Location) (time.Time, bool) {
	t, e := time.ParseInLocation(wallClock, v, loc)
	return t, e == nil && t.Format(wallClock) == v && inRange(t)
}

// inRange matches the years the server accepts, in local time and in UTC.
func inRange(t time.Time) bool {
	return t.Year() >= 1 && t.Year() <= 9999 && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999
}

// meetingEnd resolves exactly one of --end and --duration against start.
func meetingEnd(c *cobra.Command, start time.Time, end string, duration time.Duration, loc *time.Location) (time.Time, bool) {
	if c.Flags().Changed("end") == c.Flags().Changed("duration") {
		return time.Time{}, false
	}
	until, ok := start.Add(duration), duration > 0 && inRange(start.Add(duration))
	if c.Flags().Changed("end") {
		until, ok = localStamp(end, loc)
	}
	return until, ok && until.After(start)
}

func calendarCancelCmd(r *runner) *cobra.Command {
	comment := ""
	c := r.leaf("cancel ID", "Cancel your own meeting and notify the attendees now", 1, 1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"id": args[0]}
		optional(c, p, "comment", &comment)
		return r.call(c, "calendar.cancel", p, false)
	})
	c.Flags().StringVar(&comment, "comment", "", "message sent with the cancellation")
	return c
}

func calendarRespondCmd(r *runner) *cobra.Command {
	response, comment := "", ""
	c := r.leaf("respond ID", "Send your response to a meeting invitation now", 1, 1, func(c *cobra.Command, args []string) error {
		if response != "accept" && response != "tentative" && response != "decline" {
			return errUsage
		}
		p := map[string]any{"id": args[0], "response": response}
		optional(c, p, "comment", &comment)
		return r.call(c, "calendar.respond", p, false)
	})
	c.Flags().StringVar(&response, "response", "", "accept, tentative, or decline")
	c.Flags().StringVar(&comment, "comment", "", "message sent to the organizer with the response")
	return c
}

func calendarUpdateCmd(r *runner) *cobra.Command {
	var add, remove []string
	var subject, location, start, end string
	var duration time.Duration
	c := r.leaf("update ID", "Change your own meeting and send the update now", 1, 1, func(c *cobra.Command, args []string) error {
		p := map[string]any{"id": args[0]}
		optional(c, p, "subject", &subject)
		optional(c, p, "location", &location)
		loc, endSet, durationSet := r.location(), c.Flags().Changed("end"), c.Flags().Changed("duration")
		if endSet && durationSet {
			return errUsage
		}
		if c.Flags().Changed("start") {
			from, ok := localStamp(start, loc)
			// With neither --end nor --duration, the server keeps the current duration.
			if ok && (endSet || durationSet) {
				_, ok = meetingEnd(c, from, end, duration, loc)
			}
			if !ok {
				return errUsage
			}
			p["start"] = from.Format(time.RFC3339)
		}
		if endSet {
			until, ok := localStamp(end, loc)
			if !ok {
				return errUsage
			}
			p["end"] = until.Format(time.RFC3339)
		}
		if durationSet {
			if duration <= 0 {
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
		if len(p) == 1 {
			return errUsage
		}
		return r.call(c, "calendar.update", p, false)
	})
	c.Flags().StringVar(&subject, "subject", "", "new meeting subject")
	c.Flags().StringVar(&location, "location", "", "new meeting location")
	c.Flags().StringVar(&start, "start", "", "new local start as 'YYYY-MM-DD HH:MM'; keeps the duration without --end or --duration")
	c.Flags().StringVar(&end, "end", "", "new local end as 'YYYY-MM-DD HH:MM'")
	c.Flags().DurationVar(&duration, "duration", 0, "new meeting length, for example 30m or 1h30m")
	c.Flags().StringArrayVar(&add, "add-attendee", nil, "attendee address to add; repeatable")
	c.Flags().StringArrayVar(&remove, "remove-attendee", nil, "attendee address to remove; repeatable")
	return c
}

func calendarInviteCmd(r *runner) *cobra.Command {
	var to []string
	var subject, body, location, start, end string
	var duration time.Duration
	teams := false
	c := r.leaf("invite", "Create a meeting and send the invitation now", 0, 0, func(c *cobra.Command, _ []string) error {
		if len(to) == 0 || !c.Flags().Changed("subject") || !c.Flags().Changed("start") {
			return errUsage
		}
		loc := r.location()
		from, ok := localStamp(start, loc)
		if !ok {
			return errUsage
		}
		until, ok := meetingEnd(c, from, end, duration, loc)
		if !ok {
			return errUsage
		}
		p := map[string]any{"to": to, "subject": subject, "body": body, "start": from.Format(time.RFC3339), "end": until.Format(time.RFC3339)}
		optional(c, p, "location", &location)
		if teams {
			p["teams"] = true
		}
		return r.call(c, "calendar.invite", p, false)
	})
	c.Flags().StringArrayVar(&to, "to", nil, "attendee address; repeatable")
	c.Flags().StringVar(&subject, "subject", "", "meeting subject")
	c.Flags().StringVar(&body, "body", "", "meeting body text")
	c.Flags().StringVar(&location, "location", "", "meeting location")
	c.Flags().StringVar(&start, "start", "", "local start as 'YYYY-MM-DD HH:MM'")
	c.Flags().StringVar(&end, "end", "", "local end as 'YYYY-MM-DD HH:MM'")
	c.Flags().DurationVar(&duration, "duration", 0, "meeting length, for example 30m or 1h30m")
	c.Flags().BoolVar(&teams, "teams", false, "add a Microsoft Teams meeting link")
	return c
}
