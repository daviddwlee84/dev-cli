//go:build !windows

package handoff

import (
	"bytes"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestOpenStaysInTerminalForegroundProcessGroup(t *testing.T) {
	var out bytes.Buffer
	_, err := Run(t.Context(), Spec{
		Mode: ModeOpen,
		Launcher: Launcher{
			Command: []string{"sh", "-c", `ps -o pgid= -p $$`, PromptPlaceholder},
			Input:   TransportArgv,
		},
		Prompt: "unused", Dir: t.TempDir(), In: strings.NewReader(""), Out: &out, Err: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("parse child process group %q: %v", out.String(), err)
	}
	if childGroup != syscall.Getpgrp() {
		t.Fatalf("open child process group = %d, terminal group = %d; interactive stdin would stop with SIGTTIN", childGroup, syscall.Getpgrp())
	}
}
