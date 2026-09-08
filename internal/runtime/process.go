package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type ProcessState string

const (
	ProcessUnknown ProcessState = "unknown"
	ProcessShell   ProcessState = "foreground-shell"
	ProcessCommand ProcessState = "foreground-command"
	ProcessCaller  ProcessState = "caller-command"
)

// ForegroundProcess deliberately omits command arguments and environment.
// Identity is a digest of the executable/argv observation, never its raw text.
type ForegroundProcess struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	CWD      string `json:"cwd"`
	Identity string `json:"identity,omitempty"`
}

type PaneProcessObservation struct {
	PaneID    string              `json:"pane_id"`
	ShellPID  int                 `json:"shell_pid"`
	GroupID   int                 `json:"group_id"`
	Processes []ForegroundProcess `json:"processes"`
	State     ProcessState        `json:"state"`
	Error     string              `json:"error,omitempty"`
}

// PaneProcessInspector reads one exact pane. Absence of the capability is not
// evidence of an idle shell; a supported but failed probe fails closed.
type PaneProcessInspector interface {
	InspectPaneProcesses(context.Context, string) (PaneProcessObservation, error)
}

func (p PaneProcessObservation) Fingerprint() string { return processDigest(p) }

func processDigest(v any) string {
	body, _ := json.Marshal(v)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// DisplayText removes terminal controls from externally supplied labels.
func DisplayText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func observePaneProcess(ctx context.Context, rt Runtime, pane Pane, callerPane string, callerPID int) Pane {
	inspector, ok := rt.(PaneProcessInspector)
	if !ok {
		return pane
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	observed, err := inspector.InspectPaneProcesses(probeCtx, pane.ID)
	if err != nil {
		observed = PaneProcessObservation{PaneID: pane.ID, State: ProcessUnknown, Error: "foreground process inspection failed"}
	} else if observed.PaneID != pane.ID {
		observed = PaneProcessObservation{PaneID: pane.ID, State: ProcessUnknown, Error: "process response does not identify the requested pane"}
	}
	observed.Processes = append([]ForegroundProcess(nil), observed.Processes...)
	sort.Slice(observed.Processes, func(i, j int) bool { return observed.Processes[i].PID < observed.Processes[j].PID })
	if observed.State == ProcessCommand && pane.ID == callerPane && pane.Agent == "" && pane.AgentSession == "" {
		if callerPID == 0 {
			callerPID = os.Getpid()
		}
		found, exact := false, true
		for _, p := range observed.Processes {
			found = found || p.PID == callerPID
			if p.PID != callerPID && p.PID != observed.ShellPID {
				exact = false
			}
		}
		if found && exact {
			observed.State = ProcessCaller
		}
	}
	pane.Process = &observed
	return pane
}

// WorkspaceProtection keeps canonical and other checkout workspaces intact,
// even when all of their current panes happen to cd into a child worktree.
func WorkspaceProtection(session Session, target string) string {
	if !session.WorkspaceIdentityObserved {
		return ""
	} // legacy backend, existing coverage policy
	if session.WorkspaceCheckout == "" {
		return "workspace checkout ownership is unknown"
	}
	checkout, err := pathx.Canonical(session.WorkspaceCheckout)
	canonicalTarget, targetErr := pathx.Canonical(target)
	if err != nil || targetErr != nil {
		return "workspace checkout identity cannot be verified"
	}
	if !session.WorkspaceLinked {
		return "parent/canonical workspace is preserved"
	}
	if checkout != canonicalTarget {
		return "workspace belongs to another checkout"
	}
	return ""
}

// PaneFingerprint includes the terminal incarnation, tab, cwd and foreground
// evidence. It is suitable for short-lived approvals, never durable task state.
func PaneFingerprint(p Pane) string { return processDigest(p) }

func WorkspaceFingerprint(s Session) string {
	panes := append([]Pane(nil), s.Panes...)
	sort.Slice(panes, func(i, j int) bool { return panes[i].ID < panes[j].ID })
	return processDigest(struct {
		Handle, Label, Checkout string
		Known, Linked           bool
		Panes                   []Pane
	}{s.Handle, s.Label, s.WorkspaceCheckout, s.WorkspaceIdentityObserved, s.WorkspaceLinked, panes})
}

// ForegroundFingerprint binds consent to precisely the non-agent programs
// whose checkout files may change. It excludes only the proven dev caller.
func ForegroundFingerprint(occupancy Occupancy) (string, error) {
	var values []string
	for _, session := range occupancy.Sessions {
		for _, pane := range session.Panes {
			p := pane.Process
			if p == nil {
				continue
			} // unsupported capability is reported separately
			if p.State == ProcessUnknown || p.Error != "" {
				return "", fmt.Errorf("pane %s foreground process observation is unknown", pane.ID)
			}
			if pane.Agent == "" && pane.AgentSession == "" && p.State == ProcessCommand {
				values = append(values, session.Runtime.Handle+":"+PaneFingerprint(pane))
			}
		}
	}
	if len(values) == 0 {
		return "", nil
	}
	sort.Strings(values)
	return processDigest(values), nil
}

func (h *Herdr) InspectPaneProcesses(ctx context.Context, paneID string) (PaneProcessObservation, error) {
	var result struct {
		Info struct {
			PaneID    string `json:"pane_id"`
			ShellPID  int    `json:"shell_pid"`
			GroupID   int    `json:"foreground_process_group_id"`
			Processes []struct {
				PID   int      `json:"pid"`
				Name  string   `json:"name"`
				CWD   string   `json:"cwd"`
				Argv0 string   `json:"argv0"`
				Argv  []string `json:"argv"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	raw, probePID, err := h.readProcessInfo(ctx, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return PaneProcessObservation{}, fmt.Errorf("inspect foreground processes in pane %s: unavailable", paneID)
	}
	var envelope herdrEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error != nil {
		return PaneProcessObservation{}, fmt.Errorf("inspect foreground processes in pane %s: invalid response", paneID)
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return PaneProcessObservation{}, fmt.Errorf("inspect foreground processes in pane %s: invalid process response", paneID)
	}
	info := result.Info
	out := PaneProcessObservation{PaneID: info.PaneID, ShellPID: info.ShellPID, GroupID: info.GroupID, State: ProcessUnknown}
	for _, p := range info.Processes {
		// Herdr observes this very query while its CLI client waits for the
		// response. Exclude only the PID we actually started, never its name.
		if probePID > 0 && p.PID == probePID {
			continue
		}
		if p.PID <= 0 || p.Name == "" {
			out.Error = "incomplete foreground process identity"
			return out, nil
		}
		out.Processes = append(out.Processes, ForegroundProcess{PID: p.PID, Name: DisplayText(p.Name), CWD: p.CWD,
			Identity: processDigest(struct {
				Exe  string
				Args []string
			}{p.Argv0, p.Argv})})
	}
	if info.ShellPID <= 0 || info.GroupID <= 0 || len(out.Processes) == 0 {
		out.Error = "foreground process identity is unavailable"
		return out, nil
	}
	out.State = ProcessCommand
	if len(out.Processes) == 1 && out.Processes[0].PID == info.ShellPID && info.GroupID == info.ShellPID {
		out.State = ProcessShell
	}
	return out, nil
}

func (h *Herdr) readProcessInfo(ctx context.Context, args ...string) ([]byte, int, error) {
	if h.processProbe != nil {
		return h.processProbe(ctx, args...)
	}
	if h.runCommand != nil {
		raw, err := h.runCommand(ctx, args...)
		return raw, 0, err
	}
	command := exec.CommandContext(ctx, h.bin, args...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	err := command.Run()
	pid := 0
	if command.Process != nil {
		pid = command.Process.Pid
	}
	return stdout.Bytes(), pid, err
}
