package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func bindRetireCaller(intent *retireHandoffIntent, preview retire.Inspection) {
	for _, session := range preview.Sessions {
		for _, pane := range session.Panes {
			if pane.Process != nil && pane.Process.State == runtime.ProcessCaller {
				intent.CallerPaneID = pane.ID
				intent.CallerPID = os.Getpid()
				intent.CallerShellPID = pane.Process.ShellPID
				process := *pane.Process
				process.State = runtime.ProcessCommand
				intent.CallerProcessFingerprint = process.Fingerprint()
			}
		}
	}
}

// Only process evidence of the proven launching dev command is normalized.
// The caller pane/tab/terminal, cwd, workspace and all other programs remain
// bound to the original preview.
func retireHandoffFingerprint(preview retire.Inspection, caller string) string {
	if caller == "" {
		return preview.Fingerprint()
	}
	preview.Sessions = append([]retire.Session(nil), preview.Sessions...)
	strip := func(panes []runtime.Pane) []runtime.Pane {
		out := append([]runtime.Pane(nil), panes...)
		for i := range out {
			if out[i].ID == caller {
				out[i].Process = nil
			}
		}
		return out
	}
	for i := range preview.Sessions {
		s := &preview.Sessions[i]
		s.Panes = strip(s.Panes)
		s.Runtime.Panes = strip(s.Runtime.Panes)
	}
	return preview.Fingerprint()
}

func awaitRetireCaller(ctx context.Context, rt runtime.Runtime, target string, intent retireHandoffIntent) (retire.Inspection, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		preview, err := retire.InspectForExternalCoordinator(ctx, rt, target, retire.Options{CloseUnknown: intent.CloseUnknown, ProcessClosures: intent.ProcessClosures})
		if err != nil {
			return preview, err
		}
		if intent.CallerPaneID == "" {
			return preview, nil
		}
		found, waiting := false, false
		for _, session := range preview.Sessions {
			for _, pane := range session.Panes {
				if pane.ID != intent.CallerPaneID {
					continue
				}
				found = true
				p := pane.Process
				if p == nil || p.Error != "" || p.ShellPID != intent.CallerShellPID || pane.Agent != "" || pane.AgentSession != "" {
					return preview, fmt.Errorf("retirement handoff is stale: caller pane identity changed")
				}
				if p.State == runtime.ProcessShell {
					continue
				}
				if p.State != runtime.ProcessCommand || p.Fingerprint() != intent.CallerProcessFingerprint {
					return preview, fmt.Errorf("retirement handoff is stale: a different program occupies the caller pane")
				}
				hasCaller := false
				for _, process := range p.Processes {
					hasCaller = hasCaller || process.PID == intent.CallerPID
				}
				if !hasCaller {
					return preview, fmt.Errorf("retirement handoff is stale: original caller PID is absent")
				}
				waiting = true
			}
		}
		if !found {
			return preview, fmt.Errorf("retirement handoff is stale: caller pane disappeared")
		}
		if !waiting {
			return preview, nil
		}
		if time.Now().After(deadline) {
			return preview, fmt.Errorf("original dev command has not left its pane; cleanup was not attempted")
		}
		select {
		case <-ctx.Done():
			return preview, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
