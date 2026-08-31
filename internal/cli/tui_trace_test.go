package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/perftrace"
)

func TestTraceFromEnvironmentRequiresNewAbsoluteFile(t *testing.T) {
	t.Setenv(tuiTraceEnv, "")
	trace, path, err := traceFromEnvironment()
	if err != nil || trace != nil || path != "" {
		t.Fatalf("unset trace = %v, %q, %v", trace, path, err)
	}

	t.Setenv(tuiTraceEnv, "trace.json")
	if _, _, err := traceFromEnvironment(); err == nil {
		t.Fatal("relative trace path was accepted")
	}

	existing := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tuiTraceEnv, existing)
	if _, _, err := traceFromEnvironment(); err == nil {
		t.Fatal("existing trace path was accepted")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}

	wanted := filepath.Join(t.TempDir(), "new.json")
	t.Setenv(tuiTraceEnv, wanted)
	trace, path, err = traceFromEnvironment()
	if err != nil || trace == nil || path != wanted {
		t.Fatalf("new trace = %v, %q, %v", trace, path, err)
	}
}

func TestFinishTraceDoesNotConsumePathForNonTUICommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	app := &App{
		Err: &bytes.Buffer{}, trace: perftrace.New(10), tracePath: path, traceOnce: &sync.Once{},
	}
	app.finishTrace()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("non-TUI command wrote trace: %v", err)
	}
}

func TestFinishTraceWritesOnceAndIgnoresLateEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	trace := perftrace.New(10)
	trace.Mark(perftrace.CLIExecuteBegin, perftrace.Fields{})
	var stderr bytes.Buffer
	app := &App{Err: &stderr, trace: trace, tracePath: path, traceOnce: &sync.Once{}, traceTUI: true}

	app.finishTrace()
	trace.Mark(perftrace.TUIProducerRepos, perftrace.Fields{})
	app.finishTrace()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), string(perftrace.CLIExecuteBegin)) != 1 ||
		strings.Contains(string(data), string(perftrace.TUIProducerRepos)) {
		t.Fatalf("unexpected frozen trace:\n%s", data)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestFinishTraceFailureIsNonFatalAndReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app := &App{
		Err: &stderr, trace: perftrace.New(10), tracePath: path, traceOnce: &sync.Once{}, traceTUI: true,
	}
	app.finishTrace()
	if !strings.Contains(stderr.String(), "performance trace") {
		t.Fatalf("missing warning: %q", stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing trace changed: %q, %v", data, err)
	}
}
