//go:build windows

package agentinterop

import (
	"errors"
	"os/exec"
)

// The regular Windows launcher uses a Job Object. A detached diagnostic probe
// has no supported suspended-child job handoff yet; do not risk orphaning it.
func prepareCheckProcess(*exec.Cmd) (func(), error) {
	return func() {}, errors.New("stdio connection checks are unsupported on Windows; use the native client")
}
func attachCheckProcess(*exec.Cmd) error { return nil }
