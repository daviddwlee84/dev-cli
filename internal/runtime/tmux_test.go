package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTmuxOpenReportsCreationAndReuseWithoutPane(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasErr     error
		displayDir string
		created    bool
		calls      [][]string
	}{
		{name: "reuse", displayDir: "/repo", calls: [][]string{
			{"has-session", "-t", "=child-task"},
			{"display-message", "-p", "-t", "=child-task", "#{session_path}"},
		}},
		{name: "create", hasErr: errors.New("missing"), created: true, calls: [][]string{
			{"has-session", "-t", "=child-task"},
			{"new-session", "-d", "-s", "child-task", "-c", "/repo"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			tm := NewTmux()
			tm.runCommand = func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				if len(calls) == 1 {
					return "", tc.hasErr
				}
				if tc.displayDir != "" {
					return tc.displayDir, nil
				}
				return "", nil
			}
			got, err := tm.Open(context.Background(), "/repo", "child task")
			if err != nil {
				t.Fatal(err)
			}
			if got.Handle != "child-task" || got.Surface != "session" || !got.Opened || got.Created != tc.created || got.RootPaneID != "" {
				t.Fatalf("Open = %+v", got)
			}
			if !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("calls = %v, want %v", calls, tc.calls)
			}
		})
	}
}

func TestTmuxOpenRejectsSameNameAtDifferentDirectory(t *testing.T) {
	tm := NewTmux()
	call := 0
	tm.runCommand = func(_ context.Context, args ...string) (string, error) {
		call++
		if call == 1 {
			return "", nil
		}
		return "/other/repo", nil
	}
	got, err := tm.Open(context.Background(), "/repo", "child task")
	if err == nil || got != (OpenResult{}) {
		t.Fatalf("mismatched tmux reuse = %+v, %v", got, err)
	}
}

func TestTmuxActivateInsideUsesSwitchClient(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux.sock,1,0")
	var calls [][]string
	tm := NewTmux()
	tm.runCommand = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}
	if err := tm.Activate(context.Background(), "child-task"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"switch-client", "-t", "=child-task"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestTmuxActivateOutsideAttaches(t *testing.T) {
	t.Setenv("TMUX", "")
	record := filepath.Join(t.TempDir(), "record")
	script := filepath.Join(t.TempDir(), "tmux")
	body := "#!/bin/sh\nprintf '%s' \"$*\" > \"$DEV_TEST_RECORD\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_TEST_RECORD", record)
	tm := NewTmux()
	tm.bin = script
	if err := tm.Activate(context.Background(), "child-task"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil || string(got) != "attach-session -t =child-task" {
		t.Fatalf("outside attach record = %q, %v", got, err)
	}
}
