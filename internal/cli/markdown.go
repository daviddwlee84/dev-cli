package cli

import (
	"os"
	"strings"
)

// ANSI codes, applied only when stdout is a terminal. Piping `dev help` into a
// file or a pager should yield plain markdown.
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
)

// renderMarkdown applies light terminal styling to a help page. This is
// deliberately not a full markdown renderer: the pages are written to read
// well as plain text, and a heavyweight renderer would reflow the command
// examples that need to stay copy-pasteable.
func renderMarkdown(body string) string {
	if !colorEnabled() {
		return body
	}
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inCode = !inCode
			b.WriteString(ansiDim + line + ansiReset)
		case inCode:
			b.WriteString(ansiCyan + line + ansiReset)
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString(ansiBold + line + ansiReset)
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// colorEnabled honours the NO_COLOR convention and only styles a real tty.
func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
