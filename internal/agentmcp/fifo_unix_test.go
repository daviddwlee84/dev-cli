//go:build !windows

package agentmcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMCPScanRejectsFIFOConfigBeforeOpen(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() {
		result, _ := NewScanner(testOptions(home, t.TempDir())).Scan(context.Background(), nil)
		done <- result
	}()
	select {
	case result := <-done:
		if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticNotRegular {
			t.Fatalf("FIFO diagnostics = %+v", result.Diagnostics)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP scan blocked opening a FIFO")
	}
}
