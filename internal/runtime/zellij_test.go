package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type zellijCall struct {
	args []string
	out  string
	err  error
}

func scriptedZellij(t *testing.T, calls ...zellijCall) *Zellij {
	t.Helper()
	index := 0
	z := NewZellij()
	z.runCommand = func(_ context.Context, args ...string) (string, error) {
		t.Helper()
		if index >= len(calls) {
			t.Fatalf("unexpected zellij call: %v", args)
		}
		call := calls[index]
		index++
		if !reflect.DeepEqual(args, call.args) {
			t.Fatalf("zellij call %d = %v, want %v", index, args, call.args)
		}
		return call.out, call.err
	}
	t.Cleanup(func() {
		if index != len(calls) {
			t.Errorf("used %d zellij calls, want %d", index, len(calls))
		}
	})
	return z
}

func TestZellijListReadsNativeLayoutCWD(t *testing.T) {
	z := scriptedZellij(t,
		zellijCall{args: []string{"list-sessions", "--short", "--no-formatting"}, out: "alpha\nbeta"},
		zellijCall{args: []string{"--session", "alpha", "action", "dump-layout"}, out: "layout {\n    cwd \"/repo one\"\n}"},
		zellijCall{args: []string{"--session", "beta", "action", "dump-layout"}, out: "layout {\n    cwd \"/repo-two\"\n}"},
	)
	got, err := z.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{
		{Handle: "alpha", Label: "alpha", Dirs: []string{"/repo one"}},
		{Handle: "beta", Label: "beta", Dirs: []string{"/repo-two"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %+v, want %+v", got, want)
	}
}

func TestZellijOpenCreatesBackgroundSessionAtDirectory(t *testing.T) {
	z := scriptedZellij(t,
		zellijCall{args: []string{"list-sessions", "--short", "--no-formatting"}, err: errors.New("No active zellij sessions found")},
		zellijCall{args: []string{"attach", "--create-background", "child-task", "options", "--default-cwd", "/repo"}},
	)
	got, err := z.Open(context.Background(), "/repo", "child task")
	if err != nil {
		t.Fatal(err)
	}
	want := OpenResult{Handle: "child-task", Surface: "session", Opened: true, Created: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Open = %+v, want %+v", got, want)
	}
}

func TestZellijOpenReusesOnlySameDirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cwd     string
		wantErr bool
	}{
		{name: "same", cwd: "/repo"},
		{name: "different", cwd: "/other", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			z := scriptedZellij(t,
				zellijCall{args: []string{"list-sessions", "--short", "--no-formatting"}, out: "child-task"},
				zellijCall{args: []string{"--session", "child-task", "action", "dump-layout"}, out: "layout {\n cwd \"" + tc.cwd + "\"\n}"},
			)
			got, err := z.Open(context.Background(), "/repo", "child task")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "already exists") || got != (OpenResult{}) {
					t.Fatalf("Open = %+v, %v", got, err)
				}
				return
			}
			if err != nil || got.Handle != "child-task" || got.Created {
				t.Fatalf("Open = %+v, %v", got, err)
			}
		})
	}
}

func TestZellijActivateInsideUsesSwitchSession(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	z := scriptedZellij(t, zellijCall{args: []string{"action", "switch-session", "child-task"}})
	if err := z.Activate(context.Background(), "child-task"); err != nil {
		t.Fatal(err)
	}
}

func TestZellijActivateInsideExplainsOldVersion(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "current")
	z := scriptedZellij(t, zellijCall{
		args: []string{"action", "switch-session", "child-task"}, err: errors.New("unknown subcommand"),
	})
	if err := z.Activate(context.Background(), "child-task"); err == nil || !strings.Contains(err.Error(), "0.44") {
		t.Fatalf("Activate error = %v", err)
	}
}

func TestZellijActivateOutsideAttaches(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	record := filepath.Join(t.TempDir(), "record")
	script := filepath.Join(t.TempDir(), "zellij")
	body := "#!/bin/sh\nprintf '%s' \"$*\" > \"$DEV_TEST_RECORD\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_TEST_RECORD", record)
	z := NewZellij()
	z.bin = script
	if err := z.Activate(context.Background(), "child-task"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil || string(got) != "attach child-task" {
		t.Fatalf("outside attach record = %q, %v", got, err)
	}
}
