// Package picker selects one value from a caller-provided list. It can hand
// the terminal to a line-oriented external picker such as fzf, and otherwise
// falls back to dev's built-in Bubble Tea interface.
package picker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode"
)

// Item is one selectable value. Value is opaque caller data and is never sent
// to an external picker; Label and Description are display-only.
type Item struct {
	Value       string
	Label       string
	Description string
}

// ErrCanceled reports that the user left a picker without selecting an item.
var ErrCanceled = errors.New("picker canceled")

// Request describes one selection interaction.
type Request struct {
	Prompt string
	Items  []Item
}

// Result contains the selected original item.
type Result struct {
	Item Item
}

// Selector owns the streams and optional external command for one application.
type Selector struct {
	in       io.Reader
	out      io.Writer
	errOut   io.Writer
	command  []string
	lookPath func(string) (string, error)
}

// New constructs a selector. command is an argv vector, not a shell command;
// an empty vector always selects the built-in backend.
func New(in io.Reader, out, errOut io.Writer, command []string) *Selector {
	return &Selector{
		in:       in,
		out:      out,
		errOut:   errOut,
		command:  append([]string(nil), command...),
		lookPath: exec.LookPath,
	}
}

// Select uses the configured external picker when it is installed and the
// built-in picker otherwise.
func (s *Selector) Select(ctx context.Context, request Request) (Result, error) {
	if len(request.Items) == 0 {
		return Result{}, nil
	}
	if len(s.command) == 0 {
		return s.selectBuiltin(ctx, request)
	}
	path, err := s.lookPath(s.command[0])
	if err != nil {
		return s.selectBuiltin(ctx, request)
	}
	return s.selectExternal(ctx, path, request)
}

func (s *Selector) selectExternal(ctx context.Context, path string, request Request) (Result, error) {
	lines, items := renderCandidates(request.Items)
	args := make([]string, len(s.command)-1)
	for index, arg := range s.command[1:] {
		args[index] = strings.ReplaceAll(arg, "{prompt}", request.Prompt)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var input strings.Builder
	inputSize := len(lines)
	for _, line := range lines {
		inputSize += len(line)
	}
	input.Grow(inputSize)
	for _, line := range lines {
		input.WriteString(line)
		input.WriteByte('\n')
	}
	cmd.Stdin = strings.NewReader(input.String())
	if s.errOut == nil {
		cmd.Stderr = io.Discard
	} else {
		cmd.Stderr = s.errOut
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return Result{}, ErrCanceled
		}
		return Result{}, fmt.Errorf("external picker: %w", err)
	}

	selected, err := selectedLine(stdout.String())
	if err != nil {
		return Result{}, err
	}
	item, ok := items[selected]
	if !ok {
		return Result{}, fmt.Errorf("external picker returned an unknown selection")
	}
	return Result{Item: item}, nil
}

func selectedLine(output string) (string, error) {
	output = strings.TrimSuffix(output, "\n")
	output = strings.TrimSuffix(output, "\r")
	if output == "" || strings.ContainsAny(output, "\r\n\x00") {
		return "", errors.New("external picker returned malformed output")
	}
	return output, nil
}

func renderCandidates(items []Item) ([]string, map[string]Item) {
	lines := make([]string, 0, len(items))
	byLine := make(map[string]Item, len(items))
	nextOrdinal := make(map[string]int, len(items))
	for _, item := range items {
		label := cleanDisplay(item.Label)
		if label == "" {
			label = "(unnamed)"
		}
		base := label
		if description := cleanDisplay(item.Description); description != "" {
			base += "\t" + description
		}
		ordinal := nextOrdinal[base]
		if ordinal == 0 {
			ordinal = 1
		}
		line := base
		for {
			if ordinal > 1 {
				line = fmt.Sprintf("%s\t[%d]", base, ordinal)
			}
			ordinal++
			if _, exists := byLine[line]; !exists {
				nextOrdinal[base] = ordinal
				break
			}
		}
		lines = append(lines, line)
		byLine[line] = item
	}
	return lines, byLine
}

func cleanDisplay(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsControl), " ")
}
