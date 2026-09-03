//go:build !windows

package safefile

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenRegularRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "source")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		file, _, err := OpenRegular(fifo)
		if file != nil {
			_ = file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("OpenRegular FIFO error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenRegular blocked on FIFO")
	}
}
