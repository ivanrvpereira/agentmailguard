// Package term holds the terminal output that amg and amgd share: message lines with colored prefixes, the color
// decision for a writer, and the filter that makes untrusted text safe for a terminal.
package term

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Basic ANSI colors survive every profile above NoTTY, so a 16-color terminal shows the same meaning.
var (
	Label   = lipgloss.NewStyle().Bold(true)
	Error   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	Warning = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	Success = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
)

// colorEnv lists the display-only variables colorprofile reads for a profile. TMUX is left out on
// purpose: colorprofile runs `tmux info` when it is set, and this client starts no subprocess.
// TTY_FORCE is left out too: it would claim a terminal for a pipe or a file.
var colorEnv = []string{"TERM", "COLORTERM", "CLICOLOR"}

// Colorized returns w with color kept, downgraded, or removed. NO_COLOR with any value removes color,
// ahead of every other variable; CLICOLOR_FORCE with any value except 0 keeps it even when w is not a
// terminal. Otherwise the writer decides, so pipes and buffers receive plain text. Both rules are
// spelled out here because colorprofile reads these two variables as booleans instead.
func Colorized(w io.Writer, lookup func(string) string) io.Writer {
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

// Safe removes escape sequences and control characters from text before it reaches a terminal that honors them.
// Line breaks and tabs stay.
func Safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.Is(unicode.Cc, r) {
			return r
		}
		return -1
	}, ansi.Strip(s))
}

// ErrorLine writes one message line; the prefix may carry color, the words never change.
func ErrorLine(w io.Writer, message string) {
	fmt.Fprintln(w, Error.Render("error:")+" "+message)
}

// Usage reports a rejected command line and returns exit code 2.
func Usage(w io.Writer) int { ErrorLine(w, "invalid command usage; use --help"); return 2 }

// Failure reports a failed command and returns exit code 1.
func Failure(w io.Writer, message string) int { ErrorLine(w, message); return 1 }

// Output writes text to stdout and returns code, or reports a failed write.
func Output(stdout, stderr io.Writer, text string, code int) int {
	if _, err := io.WriteString(stdout, text); err != nil {
		return Failure(stderr, "output failed")
	}
	return code
}

// WarnExtraScopes warns about grants that the daemon does not use. Scope labels come from provider tokens, so
// each one is quoted.
func WarnExtraScopes(w io.Writer, extra []string) {
	quoted := make([]string, len(extra))
	note := ""
	for i, s := range extra {
		quoted[i] = strconv.Quote(s)
		if strings.HasPrefix(s, "role:") {
			note = " role: entries are application permissions."
		}
	}
	fmt.Fprintf(w, "%s token grants extra scopes the daemon does not use: %s.%s Remove the extra consent to reduce exposure.\n", Warning.Render("warning:"), strings.Join(quoted, ", "), note)
}
