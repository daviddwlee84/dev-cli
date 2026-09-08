package retire

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func inspectForegroundActivity(result *Inspection, session runtime.OccupancySession, opts Options) {
	for _, pane := range session.Panes {
		process := pane.Process
		if process == nil || process.State == runtime.ProcessUnknown || process.Error != "" {
			result.Blockers = append(result.Blockers, fmt.Sprintf("pane %s foreground processes are unknown; inspect or close it independently", pane.ID))
		}
		if pane.Agent != "" || pane.AgentSession != "" {
			appendAgentStatusBlocker(result, session.Runtime.Handle, pane.AgentStatus, opts.CloseUnknown)
			continue
		}
		if process != nil && process.State == runtime.ProcessCommand {
			result.ProcessSessions = append(result.ProcessSessions, session.Runtime.Handle)
			if opts.ProcessClosures[pane.ID] != runtime.PaneFingerprint(pane) {
				result.Blockers = append(result.Blockers, fmt.Sprintf("pane %s has foreground programs; exact interactive termination approval is required", pane.ID))
			}
		}
	}
}

// ProcessClosuresFor returns only the exact non-agent program panes shown in
// this workspace. The returned map is an independent, short-lived approval.
func ProcessClosuresFor(session Session) map[string]string {
	approved := make(map[string]string)
	for _, pane := range session.Panes {
		if pane.Agent == "" && pane.AgentSession == "" && pane.Process != nil && pane.Process.State == runtime.ProcessCommand {
			approved[pane.ID] = runtime.PaneFingerprint(pane)
		}
	}
	return approved
}
