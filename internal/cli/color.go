package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	colorAuto   = "auto"
	colorAlways = "always"
	colorNever  = "never"

	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

type cliStyle struct {
	enabled bool
}

func validateColorMode(mode string) error {
	switch mode {
	case "", colorAuto, colorAlways, colorNever:
		return nil
	default:
		return fmt.Errorf("invalid --color value %q: use auto, always, or never", mode)
	}
}

func styleForWriter(w io.Writer, mode string) cliStyle {
	switch mode {
	case colorAlways:
		return cliStyle{enabled: true}
	case colorNever:
		return cliStyle{}
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return cliStyle{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return cliStyle{}
	}
	info, err := f.Stat()
	return cliStyle{enabled: err == nil && info.Mode()&os.ModeCharDevice != 0}
}

func (s cliStyle) paint(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func (s cliStyle) title(text string) string   { return s.paint(ansiBold+ansiCyan, text) }
func (s cliStyle) header(text string) string  { return s.paint(ansiBold+ansiCyan, text) }
func (s cliStyle) prompt(text string) string  { return s.paint(ansiBold+ansiCyan, text) }
func (s cliStyle) label(text string) string   { return s.paint(ansiDim, text) }
func (s cliStyle) dim(text string) string     { return s.paint(ansiDim, text) }
func (s cliStyle) success(text string) string { return s.paint(ansiGreen, text) }
func (s cliStyle) warning(text string) string { return s.paint(ansiYellow, text) }
func (s cliStyle) danger(text string) string  { return s.paint(ansiBold+ansiRed, text) }
func (s cliStyle) review(text string) string  { return s.paint(ansiMagenta, text) }

// strong and code are the two roles the Markdown renderer needs. They are named
// for what they mark rather than for a colour so the palette stays in one file.
func (s cliStyle) strong(text string) string { return s.paint(ansiBold, text) }
func (s cliStyle) code(text string) string   { return s.paint(ansiCyan, text) }

func (s cliStyle) taskState(text string) string {
	return s.taskStateFor(text, text)
}

func (s cliStyle) taskStateFor(state, text string) string {
	switch strings.ToLower(state) {
	case "hot", "done":
		return s.success(text)
	case "warm", "cold", "parked":
		return s.warning(text)
	default:
		return text
	}
}

// hostState colors a fleet host by what it means for the answer: a reachable
// host is green, a degraded-but-usable one is yellow, and a host whose data
// dev could not obtain at all is red.
func (s cliStyle) hostState(text string) string {
	switch strings.ToLower(text) {
	case "ok":
		return s.success(text)
	case "stale", "no-dev":
		return s.warning(text)
	case "unreachable", "timeout", "incompatible", "invalid-response":
		return s.danger(text)
	default:
		return text
	}
}

// updateState colors a skill row by whether it needs attention. The values are
// the short display forms shortUpdate produces, not the raw enum.
func (s cliStyle) updateState(text string) string {
	switch strings.ToLower(text) {
	case "current":
		return s.success(text)
	case "update":
		return s.warning(text)
	case "missing", "failed":
		return s.danger(text)
	default: // unchecked, unknown
		return s.dim(text)
	}
}

// artifactState colors an intent by whether it still blocks integration.
func (s cliStyle) artifactState(text string) string {
	switch strings.ToLower(text) {
	case "finalized":
		return s.success(text)
	case "armed", "finalizing":
		return s.warning(text)
	case "failed":
		return s.danger(text)
	case "discarded":
		return s.dim(text)
	default:
		return text
	}
}

func colorModeFromArgs(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--color=") {
			return strings.TrimPrefix(arg, "--color=")
		}
		if arg == "--color" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return colorAuto
}

func (s cliStyle) git(text string) string {
	lower := strings.ToLower(text)
	switch {
	case text == "—", text == "", lower == "local":
		return s.dim(text)
	case lower == "clean":
		return s.success(text)
	case text == "?", strings.Contains(text, "="), strings.Contains(lower, "conflict"), strings.Contains(lower, "error"):
		return s.danger(text)
	case strings.Contains(lower, "dirty"), strings.Contains(lower, "ahead"),
		strings.Contains(lower, "behind"), strings.Contains(lower, "no checkout"),
		strings.Contains(lower, "prunable"), strings.Contains(lower, "locked"),
		strings.Contains(text, "!"), strings.Contains(text, "?"):
		return s.warning(text)
	default:
		return text
	}
}

func (a *App) outStyle() cliStyle { return styleForWriter(a.Out, a.colorMode) }
func (a *App) errStyle() cliStyle { return styleForWriter(a.Err, a.colorMode) }

func (a *App) newTable(header ...string) *Table {
	return &Table{head: header, style: a.outStyle()}
}

func renderCobraHelp(body string, style cliStyle) string {
	if !style.enabled {
		return body
	}
	headings := map[string]bool{
		"Usage:": true, "Aliases:": true, "Examples:": true,
		"Available Commands:": true, "Flags:": true, "Global Flags:": true,
		"Additional help topics:": true,
	}
	// Only the names a reader can type are colored — command names and flag
	// specs. Descriptions stay plain so the eye has somewhere to rest, and the
	// column padding cobra already computed is untouched, since a terminal
	// gives an escape sequence no width.
	section := ""
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if headings[trimmed] {
			section = trimmed
			lines[i] = style.header(line)
			continue
		}
		if trimmed == "" {
			section = ""
			continue
		}
		switch section {
		case "Available Commands:", "Additional help topics:":
			lines[i] = paintFirstColumn(line, style)
		case "Flags:", "Global Flags:":
			lines[i] = paintFirstColumn(line, style)
		}
	}
	return strings.Join(lines, "\n")
}

// paintFirstColumn colors the name column of a cobra help row: everything from
// the first non-space character up to the gap before its description.
func paintFirstColumn(line string, style cliStyle) string {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 || indent >= len(line) {
		return line
	}
	rest := line[indent:]
	gap := strings.Index(rest, "  ")
	if gap <= 0 {
		return line[:indent] + style.code(rest)
	}
	return line[:indent] + style.code(rest[:gap]) + rest[gap:]
}
