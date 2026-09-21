package lockx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWithDirCheckedRejectsBeforeOperationAndReleasesLock(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	rejected := errors.New("policy rejected lock")
	checked, operations := false, 0
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := WithDirChecked(ctx, directory, "fixture", func(file *os.File) error {
		checked = true
		held, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		named, err := os.Stat(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !held.Mode().IsRegular() || !os.SameFile(held, named) {
			t.Fatal("callback did not receive the actual named lock file")
		}
		return rejected
	}, func() error { operations++; return nil })
	if !errors.Is(err, rejected) || !checked || operations != 0 {
		t.Fatalf("rejection=%v checked=%v operations=%d", err, checked, operations)
	}
	// A failed policy must release its lock before returning. A deadline turns a
	// regression into a bounded failure rather than a hanging test process.
	nextCtx, nextCancel := context.WithTimeout(t.Context(), time.Second)
	defer nextCancel()
	if err := WithDir(nextCtx, directory, "next", func() error { operations++; return nil }); err != nil {
		t.Fatalf("failed policy retained the lock: %v", err)
	}
	if operations != 1 {
		t.Fatalf("subsequent operation count=%d", operations)
	}
}
