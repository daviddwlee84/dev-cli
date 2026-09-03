//go:build !windows

package agentskill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	bundledskill "github.com/daviddwlee84/dev-cli/internal/skill"
	"golang.org/x/sys/unix"
)

func TestBundledIntegrityRejectsFIFOWithoutBlocking(t *testing.T) {
	isolateAgentEnvironment(t)
	root := initRepository(t)
	directory := filepath.Join(root, ".agents", "skills", bundledskill.Name)
	if _, err := bundledskill.Install(directory, false); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(directory, "references", "commands.md")
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan Result, 1)
	go func() {
		result, _ := Inventory(context.Background(), root, ListOptions{Project: true})
		done <- result
	}()
	select {
	case result := <-done:
		row := skillsByName(result.Skills)[bundledskill.Name]
		if row.Integrity != IntegrityDrifted {
			t.Fatalf("bundled FIFO integrity = %+v", row)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bundled integrity blocked opening a FIFO")
	}
}

func TestNativeSkillScanRejectsFIFOsBeforeOpen(t *testing.T) {
	isolateAgentEnvironment(t)
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "lock",
			setup: func(t *testing.T, root string) {
				if err := unix.Mkfifo(filepath.Join(root, "skills-lock.json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "skill frontmatter",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".agents", "skills", "fifo")
				mustMkdir(t, dir)
				if err := unix.Mkfifo(filepath.Join(dir, "SKILL.md"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initRepository(t)
			test.setup(t, root)
			done := make(chan Result, 1)
			go func() {
				result, _ := Inventory(context.Background(), root, ListOptions{Project: true})
				done <- result
			}()
			select {
			case result := <-done:
				if len(result.Diagnostics) == 0 {
					t.Fatalf("FIFO produced no diagnostic: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("native skill scan blocked opening a FIFO")
			}
		})
	}
}
