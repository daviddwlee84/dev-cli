//go:build !windows

package agentskill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestMutationCommandSerializesProviderProcesses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := t.TempDir()
	active := filepath.Join(state, "active")
	overlap := filepath.Join(state, "overlap")
	script := filepath.Join(state, "provider")
	writeExecutable(t, script, "#!/bin/sh\nif test -e \""+active+"\"; then printf overlap > \""+overlap+"\"; fi\nprintf active > \""+active+"\"\nsleep 0.15\nrm -f \""+active+"\"\n")

	commands := []*MutationCommand{
		{Command: exec.Command(script), ctx: context.Background()},
		{Command: exec.Command(script), ctx: context.Background()},
	}
	var wg sync.WaitGroup
	errors := make(chan error, len(commands))
	for _, command := range commands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- command.Run()
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(overlap); !os.IsNotExist(err) {
		t.Fatalf("provider processes overlapped: %v", err)
	}
}
