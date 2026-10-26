package cli

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/term"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

// verdictColor maps a verdict to its meaning: safe, caution, refused, account security, or failure.
func verdictColor(v model.Verdict) color.Color {
	switch v {
	case model.Green:
		return lipgloss.Color("2")
	case model.Yellow:
		return lipgloss.Color("3")
	case model.Sealed:
		return lipgloss.Color("5")
	default: // red and error
		return lipgloss.Color("1")
	}
}

// styleText colors the label column and the verdict word of the text view. views.ToText stays the
// source of truth for the content: only the labels above the body change, so removing the sequences
// restores the text byte for byte. The body and the links are never styled; they come from mail.
func styleText(text string, v model.Verdict) string {
	lines := strings.Split(term.Safe(text), "\n")
	for i, line := range lines {
		if line == "" {
			break // the body and the link list follow the first blank line
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(name, " \t") {
			continue
		}
		if name == "Verdict" {
			value = strings.Replace(value, string(v), lipgloss.NewStyle().Foreground(verdictColor(v)).Render(string(v)), 1)
		}
		lines[i] = term.Label.Render(name+":") + value
	}
	return strings.Join(lines, "\n")
}

// Sender and subject cells are cut so a row fits a usual terminal next to a 70-character message ID.
const senderWidth, subjectWidth = 24, 48

// cell makes server text safe for the terminal and cuts it to width with an ellipsis.
func cell(s string, width int) string { return ansi.Truncate(term.Safe(s), width, "\u2026") }

// summaryTable renders the verdict view of a message list. It reads no field the verdict view omits,
// so a red, sealed, or error row has no sender name and no subject to show.
func summaryTable(items []views.MailSummary) string {
	t := table.New().Headers("Received", "Verdict", "Flags", "From", "Subject", "ID")
	t.StyleFunc(func(row, col int) lipgloss.Style {
		style := lipgloss.NewStyle().Padding(0, 1)
		switch {
		case row == table.HeaderRow:
			return style.Bold(true)
		case col == 1 && row >= 0 && row < len(items):
			return style.Foreground(verdictColor(items[row].Verdict))
		}
		return style
	})
	for _, m := range items {
		t.Row(localTime(m.ReceivedAt), string(m.Verdict), markers(m), cell(sender(m), senderWidth), cell(subject(m), subjectWidth), term.Safe(m.ID))
	}
	return t.Render()
}

// localTime shows the received time in the local zone; an unparsable value is shown as it arrived.
func localTime(received string) string {
	if t, err := time.Parse(time.RFC3339Nano, received); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return received
}

// markers shows the unread dot and the follow-up flag.
func markers(m views.MailSummary) string {
	parts := []string{}
	if !m.IsRead {
		parts = append(parts, "\u2022")
	}
	if m.Flag == model.Flagged {
		parts = append(parts, "\u2691")
	}
	return strings.Join(parts, " ")
}

// sender prefers the display name the verdict view exposes and falls back to the address.
func sender(m views.MailSummary) string {
	if m.Sender == nil {
		return ""
	}
	if m.Sender.Name != nil && *m.Sender.Name != "" {
		return *m.Sender.Name
	}
	return m.Sender.Address
}

func subject(m views.MailSummary) string {
	if m.Subject == nil {
		return ""
	}
	return *m.Subject
}
