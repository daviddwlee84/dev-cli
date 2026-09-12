package tui

import (
	"io"

	"github.com/charmbracelet/x/term"
)

// Flush typeahead only while ownership is transferring, before a new reader
// or frame exists. This never drops a future key or uses a timing window.
func flushTerminalInput(input io.Reader) error {
	file, ok := input.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(file.Fd()) {
		return nil
	}
	return flushTerminalFD(file.Fd())
}
