// Package views builds explicit verdict-limited output. Never serialize model.Message or model.Event.
package views

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type Address struct {
	Name    *string `json:"name,omitempty"`
	Address string  `json:"address"`
}
type MailSummary struct {
	ID          string             `json:"id"`
	ThreadID    string             `json:"thread_id,omitempty"`
	Verdict     model.Verdict      `json:"verdict"`
	ReceivedAt  string             `json:"received_at"`
	Auth        map[string]*string `json:"auth"`
	IsRead      bool               `json:"is_read"`
	Flag        model.FlagState    `json:"flag"`
	Categories  []string           `json:"categories"`
	Sender      *Address           `json:"sender,omitempty"`
	Subject     *string            `json:"subject,omitempty"`
	To          *[]Address         `json:"to,omitempty"`
	CC          *[]Address         `json:"cc,omitempty"`
	Attachments *[]string          `json:"attachments,omitempty"`
	Reason      *string            `json:"reason,omitempty"`
	Error       *string            `json:"error,omitempty"`
}

// ThreadMessage shows the folders of a message at every verdict: they are folder and label IDs, not message text.
type ThreadMessage struct {
	MailSummary
	Folders []string `json:"folders"`
}

// Thread is a conversation, oldest first. NewestReceivedAt is the received_at of its newest message; Older reports
// that the conversation has older messages that were left out.
type Thread struct {
	ThreadID         string          `json:"thread_id"`
	Messages         []ThreadMessage `json:"messages"`
	NewestReceivedAt string          `json:"newest_received_at"`
	Older            bool            `json:"older"`
}
type MailView struct {
	MailSummary
	Body      *string            `json:"body,omitempty"`
	Links     *map[string]string `json:"links,omitempty"`
	Truncated *bool              `json:"truncated,omitempty"`
}

func address(a model.Address) Address {
	return Address{new(content.CleanSubject(a.Name)), content.CleanSubject(a.Address)}
}
func addresses(as []model.Address) []Address {
	result := make([]Address, 0, len(as))
	for _, a := range as {
		result = append(result, address(a))
	}
	return result
}
func cleanStrings(ss []string) []string {
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		result = append(result, content.CleanSubject(s))
	}
	return result
}
func listed(ss, allowed []string) []string {
	result := []string{}
	set := map[string]bool{}
	for _, s := range allowed {
		set[s] = true
	}
	for _, s := range ss {
		if set[s] {
			result = append(result, s)
		}
	}
	return result
}
func auth(a model.AuthResults) map[string]*string {
	return map[string]*string{"spf": a.SPF, "dkim": a.DKIM, "dmarc": a.DMARC, "compauth": a.CompAuth, "compauth_reason": a.CompAuthReason}
}
func verdict(v model.Verdict) model.Verdict {
	switch v {
	case model.Green, model.Yellow, model.Sealed, model.Red:
		return v
	default:
		return model.Error
	}
}
func reason(v model.Verdict) *string {
	switch v {
	case model.Yellow, model.Red:
		return new("injection")
	case model.Sealed:
		return new("account_security")
	case model.Error:
		return new("screening_error")
	}
	return nil
}
func safeError(s *string) *string {
	if model.ValidateFailureText(s) != nil {
		return new("Error")
	}
	return s
}
func iso(t time.Time) string {
	layout := "2006-01-02T15:04:05-07:00"
	if t.Nanosecond() != 0 {
		layout = "2006-01-02T15:04:05.000000-07:00"
	}
	return t.Format(layout)
}

func Summary(m model.Message, row cache.Row, categories []string) MailSummary {
	v := verdict(row.Verdict)
	result := MailSummary{ID: m.ID, ThreadID: m.ThreadID, Verdict: v, ReceivedAt: iso(m.ReceivedAt), Auth: auth(row.Auth), IsRead: m.IsRead, Flag: m.Flag, Categories: listed(m.Categories, categories), Reason: reason(v)}
	switch v {
	case model.Green, model.Yellow:
		result.Sender = new(address(m.Sender))
		result.Subject = new(content.CleanSubject(m.Subject))
		result.To = new(addresses(m.To))
		result.CC = new(addresses(m.CC))
		result.Attachments = new(cleanStrings(m.Attachments))
	case model.Red, model.Sealed:
		result.Sender = &Address{Address: content.CleanSubject(m.Sender.Address)}
	case model.Error:
		result.Error = safeError(row.Error)
	}
	return result
}

// ThreadView pairs each message, oldest first, with its summary at the same index.
func ThreadView(thread model.ThreadID, messages []model.Message, summaries []MailSummary, older bool) Thread {
	result := Thread{ThreadID: thread, Messages: make([]ThreadMessage, 0, len(messages)), Older: older}
	for i, m := range messages {
		result.Messages = append(result.Messages, ThreadMessage{summaries[i], append([]string{}, m.Folders...)})
		result.NewestReceivedAt = iso(m.ReceivedAt)
	}
	return result
}
func View(m model.Message, row cache.Row, categories []string, rendered *content.Rendered) (MailView, error) {
	result := MailView{MailSummary: Summary(m, row, categories)}
	if result.Verdict == model.Green {
		if rendered == nil {
			return MailView{}, errors.New("green view needs the rendered body")
		}
		result.Body = new(rendered.Markdown)
		result.Links = new(rendered.Domains())
		result.Truncated = new(rendered.Truncated)
	}
	return result, nil
}

// ScreeningState matches the source's explicit headers, authentication, body and links. Subjects are never cut.
func ScreeningState(m model.Message, r content.Rendered) map[string]any {
	authentication := map[string]*string{}
	if m.Auth != nil {
		authentication = auth(*m.Auth)
	}
	return map[string]any{"sender": address(m.Sender), "subject": content.CleanSubject(m.Subject), "to": addresses(m.To), "cc": addresses(m.CC), "attachments": cleanStrings(m.Attachments), "auth": authentication, "body": r.Markdown, "links": r.Domains()}
}

// ToText uses only the verdict view, so pretty output cannot recover hidden model fields.
func ToText(v MailView) string {
	people := func(as []Address) string {
		parts := []string{}
		for _, a := range as {
			p := a.Address
			if a.Name != nil && *a.Name != "" {
				p = *a.Name + " <" + p + ">"
			}
			parts = append(parts, p)
		}
		return strings.Join(parts, ", ")
	}
	var b strings.Builder
	row := func(name, value string) {
		if value != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%-13s%s", name+":", value)
		}
	}
	if v.Sender != nil {
		row("From", people([]Address{*v.Sender}))
	}
	if v.To != nil {
		row("To", people(*v.To))
	}
	if v.CC != nil {
		row("Cc", people(*v.CC))
	}
	if v.Subject != nil {
		row("Subject", *v.Subject)
	}
	date := v.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, date); err == nil {
		date = t.Local().Format("2006-01-02 15:04 MST")
	}
	row("Date", date)
	label := string(v.Verdict)
	if v.Reason != nil {
		label += " (" + *v.Reason + ")"
	}
	row("Verdict", label)
	if v.Error != nil {
		row("Error", *v.Error)
	}
	parts := []string{}
	for _, key := range []string{"spf", "dkim", "dmarc", "compauth", "compauth_reason"} {
		if value := v.Auth[key]; value != nil && *value != "" {
			parts = append(parts, key+"="+*value)
		}
	}
	row("Auth", strings.Join(parts, " "))
	read := "no"
	if v.IsRead {
		read = "yes"
	}
	row("Read", read)
	row("Flag", string(v.Flag))
	row("Categories", strings.Join(v.Categories, ", "))
	if v.Attachments != nil {
		row("Attachments", strings.Join(*v.Attachments, ", "))
	}
	row("ID", v.ID)
	row("Thread", v.ThreadID)
	if v.Body != nil {
		b.WriteString("\n\n" + *v.Body)
		if v.Truncated != nil && *v.Truncated {
			b.WriteString("\n\n(body truncated)")
		}
	}
	if v.Links != nil && len(*v.Links) > 0 {
		ids := make([]string, 0, len(*v.Links))
		for id := range *v.Links {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, _ := strconv.Atoi(strings.TrimPrefix(ids[i], "L"))
			c, _ := strconv.Atoi(strings.TrimPrefix(ids[j], "L"))
			return a < c
		})
		b.WriteString("\n")
		for _, id := range ids {
			fmt.Fprintf(&b, "\n[%s] %s", id, (*v.Links)[id])
		}
	}
	return b.String()
}
