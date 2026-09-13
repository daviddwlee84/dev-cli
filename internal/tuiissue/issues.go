// Package tuiissue defines bounded recovery suggestions. Displayed error text
// is never interpreted as a command or as authority to repeat a mutation.
package tuiissue

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type ActionID string

const (
	Recheck             ActionID = "recheck"
	EditDiagnostic      ActionID = "edit-diagnostic-source"
	SourceActions       ActionID = "source-actions"
	TaskRecovery        ActionID = "task-recovery"
	TryRecovery         ActionID = "try-recovery"
	RegistryPermissions ActionID = "registry-permissions"
	SSHPermissions      ActionID = "ssh-permissions"
	InstallDependency   ActionID = "install-dependency"
)

type Action struct {
	ID    ActionID
	Label string
	Tool  string
}
type Issue struct {
	ID        string
	View      string
	Source    string
	Row       string
	Revision  string
	TargetKey string
	Code      string
	Severity  string
	Summary   string
	Detail    string
	Guidance  string
	Actions   []Action
}

func New(view, source, row, code, detail string) Issue {
	sum := sha256.Sum256([]byte(strings.Join([]string{view, source, row, code}, "\x00")))
	return Issue{ID: fmt.Sprintf("%x", sum[:12]), View: view, Source: source, Row: row, Code: code, Severity: "warning", Summary: source + ": " + firstLine(detail), Detail: detail, Guidance: Guidance(view, source), Actions: []Action{{ID: Recheck, Label: "recheck local observations"}}}
}
func firstLine(value string) string { return strings.SplitN(value, "\n", 2)[0] }
func FromError(view, source, row string, err error) Issue {
	issue := New(view, source, row, "unavailable", err.Error())
	issue.Severity = "error"
	var path *machineregistry.PathError
	var executable *exec.Error
	switch {
	case errors.As(err, &path):
		issue.Code = "registry-path"
		issue.Summary = "Machine registry needs attention"
		issue.Guidance = "Inspect the exact path, owner and permissions below. Ownership problems need manual correction by the file owner or administrator. Repair previews only tighten current-user-owned paths; they never change owners, recurse, or remove the registry. Mapping-dependent actions stay unavailable until the registry is readable."
		issue.Actions = append(issue.Actions, Action{ID: RegistryPermissions, Label: "inspect registry permissions / preview repair…"})
	case errors.Is(err, machineregistry.ErrUnsafePath):
		issue.Code = "registry-path"
		issue.Actions = append(issue.Actions, Action{ID: RegistryPermissions, Label: "inspect registry permissions / preview repair…"})
	case errors.Is(err, sshhost.ErrUnsafePath):
		issue.Code = "ssh-permissions"
		issue.Guidance = "Review the exact SSH permission findings. Only current-user-owned paths with verifiable metadata can be tightened; ownership and unsupported ACL problems require manual attention."
		issue.Actions = append(issue.Actions, Action{ID: SSHPermissions, Label: "inspect SSH permissions / preview repair…"})
	case errors.As(err, &executable) && errors.Is(executable.Err, exec.ErrNotFound):
		missing := MissingDependency(view, executable.Name)
		missing.Detail = err.Error()
		return missing
	}
	return issue
}
func MissingDependency(view, tool string) Issue {
	issue := New(view, "dependency", tool, "dependency-missing", tool+" is not installed or is unavailable in PATH")
	issue.Guidance = "Install the tool using its official instructions, then recheck. Installation, service startup and account login are separate steps."
	if spec, ok := Tools[tool]; ok {
		issue.Guidance += "\n" + spec.Guide
		issue.Actions = append(issue.Actions, Action{ID: InstallDependency, Tool: tool, Label: "installation options for " + tool + "…"})
	}
	return issue
}

// Guidance is an explicit audit of recovery families, including errors from
// common overlays. Unknown messages retain details and never gain mutation retry.
func Guidance(view, source string) string {
	switch source {
	case "notes":
		return "Markdown notes are durable. Check the reported path and access first; use note search-index rebuild only for an index problem. Never delete note source files to repair search."
	case "stats":
		return "Activity history is durable. Inspect the reported database/path; do not remove stats.db as a cache repair. Reopen the heatmap to reread it; backfill is a separate explicit action."
	case "clipboard":
		return "The diagnostic remains available here. Check that this terminal has access to the desktop clipboard; select and copy the displayed text manually if it does not."
	case "editor", "configuration":
		return "Check the configured editor and the exact file in the diagnostic. Preserve any saved changes, correct syntax or access, and recheck that source. Rechecking does not repeat the editor or save."
	case "operation":
		return "This operation may have completed some steps. Review its result and refresh local observations before selecting another action. An operation is never repeated by this recheck."
	}
	switch strings.ToLower(view) {
	case "tasks":
		return "Inspect the selected task and its checkout. Use the task's inspect and recover action for missing worktrees or drift; lifecycle actions review fresh observations before applying."
	case "repos":
		return "Check the exact repository path, Git observation and runtime diagnostics. Use settings to correct discovery roots, or organize local work to review reconciliation. A failed observation does not prove the checkout is clean."
	case "fleet":
		return "Inspect the host's connection, trust identity, remote version and Herdr profile in its actions. Explicit host refresh/authentication may use the network; this local recheck only reads cached observations."
	case "try":
		return "Inspect the Try's recorded location and any unfinished move. Use restore, reassociate, or organize local work to review recovery; do not delete the catalog entry or move directories blindly."
	case "remote":
		return "Check the configured forge CLI and its account using that provider's native login/status commands. Explicit refresh queries the forge; local recheck reads the cache. A partially cloned directory is retained for inspection."
	case "skills":
		return "Inspect the exact skill file, native lock and ownership diagnostics. Open the selected source or use skill management to review a repair; native agent files remain authoritative."
	case "mcp":
		return "Inspect the exact native config and declaration coverage. Open the selected config to resolve syntax, source or ownership issues. Refresh is static and never starts an MCP server."
	case "ssh":
		return "Inspect source details, SSH configuration and permissions. Discovery is advisory; explicit connection tests establish authentication. Configure or register a host only through its reviewed actions."
	}
	return "Read the full diagnostic and inspect its exact source. Recheck only rereads observations; it does not repeat an operation or discard data."
}
