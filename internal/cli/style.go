package cli

import (
	"image/color"
	"io"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

// Basic ANSI colors survive every profile above NoTTY, so a 16-color terminal shows the same meaning.
var (
	labelStyle   = lipgloss.NewStyle().Bold(true)
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	warningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
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

// colorEnv lists the display-only variables colorprofile reads for a profile. TMUX is left out on
// purpose: colorprofile runs `tmux info` when it is set, and this client starts no subprocess.
// TTY_FORCE is left out too: it would claim a terminal for a pipe or a file.
var colorEnv = []string{"TERM", "COLORTERM", "CLICOLOR"}

// colorized returns w with color kept, downgraded, or removed. NO_COLOR with any value removes color,
// ahead of every other variable; CLICOLOR_FORCE with any value except 0 keeps it even when w is not a
// terminal. Otherwise the writer decides, so pipes and buffers receive plain text. Both rules are
// spelled out here because colorprofile reads these two variables as booleans instead.
func colorized(w io.Writer, lookup func(string) string) io.Writer {
	profile := colorprofile.NoTTY
	if lookup("NO_COLOR") == "" {
		env := make([]string, 0, len(colorEnv)+1)
		for _, name := range colorEnv {
			if value := lookup(name); value != "" {
				env = append(env, name+"="+value)
			}
		}
		if value := lookup("CLICOLOR_FORCE"); value != "" && value != "0" {
			env = append(env, "CLICOLOR_FORCE=1")
		}
		profile = colorprofile.Detect(w, env)
	}
	return &colorprofile.Writer{Forward: w, Profile: profile}
}

// terminalSafe removes escape sequences and control characters from server text before it reaches a
// terminal that honors them. The server strips them from mail already; this guards the client against
// a different server on an unprotected remote listener. Line breaks and tabs stay.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.Is(unicode.Cc, r) {
			return r
		}
		return -1
	}, ansi.Strip(s))
}

// styleText colors the label column and the verdict word of the text view. views.ToText stays the
// source of truth for the content: only the labels above the body change, so removing the sequences
// restores the text byte for byte. The body and the links are never styled; they come from mail.
func styleText(text string, v model.Verdict) string {
	lines := strings.Split(terminalSafe(text), "\n")
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
		lines[i] = labelStyle.Render(name+":") + value
	}
	return strings.Join(lines, "\n")
}

// Sender and subject cells are cut so a row fits a usual terminal next to a 70-character message ID.
const senderWidth, subjectWidth = 24, 48

// cell makes server text safe for the terminal and cuts it to width with an ellipsis.
func cell(s string, width int) string { return ansi.Truncate(terminalSafe(s), width, "\u2026") }

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
		t.Row(localTime(m.ReceivedAt), string(m.Verdict), markers(m), cell(sender(m), senderWidth), cell(subject(m), subjectWidth), terminalSafe(m.ID))
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
