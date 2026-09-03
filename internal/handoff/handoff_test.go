package handoff

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidateLaunchers(t *testing.T) {
	for name, tc := range map[string]struct {
		mode     Mode
		launcher Launcher
		wantErr  string
	}{
		"run stdin":                 {ModeRun, Launcher{Command: []string{"agent"}, Input: TransportStdin}, ""},
		"open file":                 {ModeOpen, Launcher{Command: []string{"agent", PromptFilePlaceholder}, Input: TransportFile}, ""},
		"open argv":                 {ModeOpen, Launcher{Command: []string{"agent", PromptPlaceholder}, Input: TransportArgv}, ""},
		"open stdin":                {ModeOpen, Launcher{Command: []string{"agent"}, Input: TransportStdin}, "reserved for the conversation"},
		"open timeout":              {ModeOpen, Launcher{Command: []string{"agent", PromptFilePlaceholder}, Input: TransportFile, Timeout: time.Minute}, "does not support a timeout"},
		"both commands":             {ModeRun, Launcher{Command: []string{"agent"}, Shell: "agent", Input: TransportStdin}, "not both"},
		"empty executable":          {ModeRun, Launcher{Command: []string{" "}, Input: TransportStdin}, "command[0]"},
		"file missing placeholder":  {ModeRun, Launcher{Command: []string{"agent"}, Input: TransportFile}, "exactly one"},
		"file embedded placeholder": {ModeRun, Launcher{Command: []string{"agent", "--file=" + PromptFilePlaceholder}, Input: TransportFile}, "whole supported"},
		"unknown placeholder":       {ModeRun, Launcher{Command: []string{"agent", "{{unknown}}"}, Input: TransportStdin}, "whole supported"},
		"argv shell":                {ModeRun, Launcher{Shell: "agent", Input: TransportArgv}, "requires command"},
		"argv missing placeholder":  {ModeRun, Launcher{Command: []string{"agent"}, Input: TransportArgv}, "exactly one"},
		"shell placeholder":         {ModeRun, Launcher{Shell: "agent " + PromptFilePlaceholder, Input: TransportFile}, "static text"},
		"shell file env":            {ModeOpen, Launcher{Shell: `agent "$DEV_PROMPT_FILE"`, Input: TransportFile}, ""},
		"shell file no env":         {ModeOpen, Launcher{Shell: "agent", Input: TransportFile}, "must reference"},
		"rc direct argv":            {ModeRun, Launcher{Command: []string{"agent"}, Input: TransportStdin, LoadShellRC: true}, "only with shell"},
	} {
		t.Run(name, func(t *testing.T) {
			err := Validate(tc.mode, tc.launcher)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell")
	}
}

func TestRunTransportsPromptAndUsesRequestedDirectory(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		launcher Launcher
		want     string
	}{
		"stdin": {
			Launcher{Command: []string{"sh", "-c", `printf '%s|' "$PWD"; cat`}, Input: TransportStdin},
			dir + "|hello",
		},
		"argv": {
			Launcher{Command: []string{"sh", "-c", `printf '%s|' "$PWD"; printf '%s' "$1"`, "_", PromptPlaceholder}, Input: TransportArgv},
			dir + "|hello",
		},
		"file": {
			Launcher{Command: []string{"sh", "-c", `printf '%s|' "$PWD"; cat "$1"`, "_", PromptFilePlaceholder}, Input: TransportFile},
			dir + "|hello",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := Run(t.Context(), Spec{
				Mode: ModeRun, Launcher: tc.launcher, Prompt: "hello", Dir: dir,
				In: strings.NewReader("user input must not be read"), Out: &out, Err: &out,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunFileTransportGetsEOFOnStdinAndCleansPrivateFile(t *testing.T) {
	requireShell(t)
	var out bytes.Buffer
	_, err := Run(t.Context(), Spec{
		Mode: ModeRun,
		Launcher: Launcher{
			Command: []string{"sh", "-c", `printf '%s|' "$(stat -f %Lp "$1" 2>/dev/null || stat -c %a "$1")"; if read x; then printf read; else printf eof; fi; printf '|%s|' "$1"; cat "$1"`, "_", PromptFilePlaceholder},
			Input:   TransportFile,
		},
		Prompt: "payload", Dir: t.TempDir(), In: strings.NewReader("do not read"), Out: &out, Err: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(out.String(), "|", 4)
	if len(parts) != 4 || parts[0] != "600" || parts[1] != "eof" || parts[3] != "payload" {
		t.Fatalf("unexpected output %q", out.String())
	}
	if _, err := os.Stat(parts[2]); !os.IsNotExist(err) {
		t.Errorf("prompt file still exists: %s (%v)", parts[2], err)
	}
}

func TestOpenKeepsStdinForConversation(t *testing.T) {
	requireShell(t)
	var out bytes.Buffer
	_, err := Run(t.Context(), Spec{
		Mode: ModeOpen,
		Launcher: Launcher{
			Command: []string{"sh", "-c", `cat "$1"; printf '|'; cat`, "_", PromptFilePlaceholder},
			Input:   TransportFile,
		},
		Prompt: "initial", Dir: t.TempDir(), In: strings.NewReader("answer"), Out: &out, Err: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "initial|answer" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunTimeoutAndDryRun(t *testing.T) {
	requireShell(t)
	launcher := Launcher{Command: []string{"sh", "-c", "sleep 1", PromptFilePlaceholder}, Input: TransportFile, Timeout: 20 * time.Millisecond}
	start := time.Now()
	_, err := Run(context.Background(), Spec{Mode: ModeRun, Launcher: launcher, Prompt: "x", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("timeout err = %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("timeout did not stop the child promptly")
	}

	preview, err := Run(t.Context(), Spec{Mode: ModeRun, Launcher: launcher, Prompt: "secret", Dir: t.TempDir(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(preview.Command, " "); strings.Contains(got, "secret") || !strings.Contains(got, "<temporary-prompt-file>") {
		t.Errorf("unsafe preview: %q", got)
	}
}

func TestTimeoutKillsLauncherDescendants(t *testing.T) {
	requireShell(t)
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	launcher := Launcher{
		Command: []string{"sh", "-c", `(sleep 0.2; touch "$1") & wait`, "_", marker},
		Input:   TransportStdin, Timeout: 20 * time.Millisecond,
	}
	if _, err := Run(t.Context(), Spec{Mode: ModeRun, Launcher: launcher, Prompt: "x", Dir: t.TempDir()}); err == nil {
		t.Fatal("expected timeout")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("descendant continued after handoff timeout")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestFileShellReceivesPromptPathOnlyThroughEnvironment(t *testing.T) {
	requireShell(t)
	var out bytes.Buffer
	_, err := Run(t.Context(), Spec{
		Mode:     ModeOpen,
		Launcher: Launcher{Shell: `cat "$DEV_PROMPT_FILE"`, Input: TransportFile},
		Prompt:   "from env", Dir: filepath.Clean(t.TempDir()), In: strings.NewReader(""), Out: &out, Err: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "from env" {
		t.Fatalf("output = %q", out.String())
	}
}
