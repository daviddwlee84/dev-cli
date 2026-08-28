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
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if headings[strings.TrimSpace(line)] {
			lines[i] = style.header(line)
		}
	}
	return strings.Join(lines, "\n")
}
