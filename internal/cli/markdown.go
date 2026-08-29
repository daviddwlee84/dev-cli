package cli

import (
	"strings"
)

// renderMarkdown applies light terminal styling to a Markdown document: help
// pages, and the journal and summary reports. This is
// deliberately not a full markdown renderer: the pages are written to read
// well as plain text, and a heavyweight renderer would reflow the command
// examples that need to stay copy-pasteable.
func renderMarkdown(body string, style cliStyle) string {
	if !style.enabled {
		return body
	}
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inCode = !inCode
			b.WriteString(style.dim(line))
		case inCode:
			b.WriteString(style.code(line))
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString(style.strong(line))
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
