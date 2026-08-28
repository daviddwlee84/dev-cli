package runtime

import (
	"context"
	"errors"
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
