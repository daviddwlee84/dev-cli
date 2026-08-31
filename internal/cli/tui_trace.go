package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/perftrace"
)

const tuiTraceEnv = "DEV_TUI_TRACE"

func traceFromEnvironment() (*perftrace.Recorder, string, error) {
	path := os.Getenv(tuiTraceEnv)
	if path == "" {
		return nil, "", nil
	}
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("%s must name an absolute, non-existing file", tuiTraceEnv)
	}
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return nil, "", fmt.Errorf("%s already exists: %s", tuiTraceEnv, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("inspect %s: %w", tuiTraceEnv, err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("inspect %s parent: %w", tuiTraceEnv, err)
	}
	if !parent.IsDir() {
		return nil, "", fmt.Errorf("%s parent is not a directory", tuiTraceEnv)
	}
	return perftrace.New(perftrace.DefaultLimit), path, nil
}

// finishTrace freezes background producers before writing the one-shot trace.
// It is safe to call both immediately after Bubble Tea and from Execute's defer.
func (a *App) finishTrace() {
	if a == nil {
		return
	}
	if a.traceOnce == nil {
		a.traceOnce = &sync.Once{}
	}
	a.traceOnce.Do(func() {
		if a.trace == nil || a.tracePath == "" || !a.traceTUI {
			return
		}
		if err := perftrace.WriteNew(a.tracePath, a.trace.Freeze()); err != nil {
			writer := a.Err
			if writer == nil {
				writer = os.Stderr
			}
			fmt.Fprintf(writer, "dev: warning: performance trace: %v\n", err)
		}
	})
}
