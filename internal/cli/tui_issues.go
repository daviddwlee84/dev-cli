package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/daviddwlee84/dev-cli/internal/tuiissue"
)

func tuiIssueActions(state *tuiAppState) tui.IssueActions {
	return tui.IssueActions{
		Inspect: func(ctx context.Context, view tui.View) []tuiissue.Issue {
			active := state.Current()
			tools := []string{}
			switch view {
			case tui.ViewTasks, tui.ViewRepos, tui.ViewTries:
				tools = append(tools, "git")
				backend := active.Cfg.Runtime.Backend
				if backend != "" && backend != "auto" && backend != "none" {
					tools = append(tools, backend)
				}
			case tui.ViewSSH:
				tools = append(tools, "ssh", "tailscale")
			case tui.ViewFleet:
				tools = append(tools, "ssh")
			case tui.ViewRemote:
				// Forge providers are optional; report available installation choices only
				// when neither of the ordinary providers can supply this empty inventory.
				_, gh := exec.LookPath("gh")
				_, glab := exec.LookPath("glab")
				if gh != nil && glab != nil {
					tools = append(tools, "gh", "glab")
				}
				if len(active.Cfg.Forge.AzureDevOps) > 0 {
					tools = append(tools, "az")
				}
			}
			issues := []tuiissue.Issue{}
			for _, tool := range tools {
				if _, err := exec.LookPath(tool); err != nil {
					issues = append(issues, tuiissue.MissingDependency(view.String(), tool))
				}
			}
			if view == tui.ViewSSH {
				if _, err := active.machineStore().Read(ctx); err != nil {
					issues = append(issues, tuiissue.FromError(view.String(), "machine registry", "", err))
				}
			}
			return issues
		},
		Prepare: func(ctx context.Context, issue tuiissue.Issue, action tuiissue.Action) (*tui.IssueExecution, error) {
			active := *state.Current()
			switch action.ID {
			case tuiissue.RegistryPermissions:
				service := active.machineStore()
				plan, err := service.PlanPermissions(ctx)
				if err != nil {
					return nil, err
				}
				var body strings.Builder
				body.WriteString("Registry permissions — exact metadata-only review\n\n")
				for _, change := range plan.Changes {
					fmt.Fprintf(&body, "%s: %04o → %04o\n", change.Path, change.BeforeMode.Perm(), change.AfterMode.Perm())
				}
				for _, diagnostic := range plan.Diagnostics {
					fmt.Fprintln(&body, diagnostic.Error())
				}
				if !plan.Ready() {
					body.WriteString("\nAutomatic repair is blocked. Ask the path owner or administrator to correct ownership/access on the exact reported path, then recheck. No recursive chmod or ownership changes will be attempted.")
					return &tui.IssueExecution{Preview: body.String()}, nil
				}
				if !plan.NeedsRepair() {
					body.WriteString("Permissions already satisfy the registry requirements. Recheck the source.")
					return &tui.IssueExecution{Preview: body.String()}, nil
				}
				body.WriteString("\nOnly the listed existing paths will be tightened. Completed repairs remain in place if a later path fails. The complete inspected metadata is revalidated before changes.")
				var status string
				command := &tuiRecoveryCommand{run: func() error {
					result, err := service.ApplyPermissions(ctx, plan)
					status = "Registry permissions: " + result.Status
					for _, outcome := range result.Outcomes {
						status += "\n" + outcome.Path + ": " + outcome.Status
					}
					return err
				}}
				return &tui.IssueExecution{Preview: body.String(), Command: command, Complete: func(err error) (string, error) { return status, err }}, nil
			case tuiissue.SSHPermissions:
				service, err := active.sshHosts()
				if err != nil {
					return nil, err
				}
				document, err := planSSHKeyDoctor(ctx, service, nil)
				plan := document.Plan
				if err != nil {
					return nil, err
				}
				var body strings.Builder
				body.WriteString("SSH/key permissions — exact metadata-only review\n\n")
				for _, diagnostic := range document.Diagnostics {
					fmt.Fprintf(&body, "%+v\n", diagnostic)
				}
				for _, change := range plan.Changes {
					fmt.Fprintf(&body, "%s: %04o → %04o\n", change.Path, change.BeforeMode.Perm(), change.AfterMode.Perm())
				}
				for _, diagnostic := range plan.Diagnostics {
					fmt.Fprintf(&body, "%+v\n", diagnostic)
				}
				if !document.Complete || !plan.Ready() || !plan.NeedsRepair() {
					body.WriteString("\nInspect the findings above and recheck after any manual correction. For a specific nonstandard key, use dev ssh key doctor --key <path> to restrict the reviewed scope.")
					return &tui.IssueExecution{Preview: body.String()}, nil
				}
				var status string
				command := &tuiRecoveryCommand{run: func() error {
					result, err := service.ApplyPermissions(ctx, plan)
					status = "SSH permissions: " + result.Status
					for _, outcome := range result.Outcomes {
						status += "\n" + outcome.Path + ": " + outcome.Status
					}
					return err
				}}
				return &tui.IssueExecution{Preview: body.String(), Command: command, Complete: func(err error) (string, error) { return status, err }}, nil
			case tuiissue.InstallDependency:
				return prepareTUIInstall(ctx, action.Tool, tuiInstallEnvironment())
			default:
				return nil, fmt.Errorf("unsupported recovery action %q", action.ID)
			}
		},
	}
}
func tuiInstallEnvironment() tuiissue.Environment {
	distribution := ""
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if value, ok := strings.CutPrefix(line, "ID="); ok {
					distribution = strings.Trim(value, "\"'")
				}
			}
		}
	}
	return tuiissue.Environment{OS: runtime.GOOS, Distribution: distribution, Root: os.Geteuid() == 0, LookPath: exec.LookPath}
}

type tuiInstallHooks struct {
	Run    func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
	Verify func(context.Context, string, []string) error
}

func prepareTUIInstall(ctx context.Context, tool string, environment tuiissue.Environment) (*tui.IssueExecution, error) {
	return prepareTUIInstallWithHooks(ctx, tool, environment, tuiInstallHooks{
		Run: func(ctx context.Context, path string, args []string, in io.Reader, out, errOut io.Writer) error {
			child := exec.CommandContext(ctx, path, args...)
			child.Stdin, child.Stdout, child.Stderr = in, out, errOut
			return child.Run()
		},
		Verify: func(ctx context.Context, path string, args []string) error {
			return exec.CommandContext(ctx, path, args...).Run()
		},
	})
}
func prepareTUIInstallWithHooks(ctx context.Context, tool string, environment tuiissue.Environment, hooks tuiInstallHooks) (*tui.IssueExecution, error) {
	plan, err := tuiissue.PlanInstall(tool, environment)
	if err != nil {
		return nil, err
	}
	execution := &tui.IssueExecution{Preview: plan.Preview()}
	if !plan.Ready() {
		return execution, nil
	}
	command := &tuiRecoveryCommand{}
	command.run = func() error {
		if err := plan.Revalidate(); err != nil {
			return err
		}
		return hooks.Run(ctx, plan.Executable(), plan.Argv[1:], command.in, command.out, command.errOut)
	}
	execution.Command = command
	execution.Complete = func(runErr error) (string, error) {
		if runErr != nil {
			return "Installation did not complete; no retry was started", runErr
		}
		path, err := environment.LookPath(tool)
		if err != nil {
			return "Installer returned, but the executable is still unavailable in this terminal", err
		}
		spec := tuiissue.Tools[tool]
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if len(spec.VersionArgs) > 0 {
			if err := hooks.Verify(checkCtx, path, spec.VersionArgs); err != nil {
				return "Executable was found, but its version check failed", err
			}
		}
		if tool == "ssh" {
			if err := hooks.Verify(checkCtx, path, []string{"-Q", "cipher"}); err != nil {
				return "OpenSSH executable was found, but its capability query failed", err
			}
		}
		if len(spec.VersionArgs) == 0 {
			return "Installed " + tool + " and found its executable. Run the relevant explicit diagnostic to verify operation.", nil
		}
		return "Installed " + tool + " and verified its executable. Service startup and account login remain separate actions.", nil
	}
	return execution, nil
}

// tuiRecoveryCommand restores native terminal I/O only after the user approves
// the concrete preview. Package-manager output is not interpreted as data.
type tuiRecoveryCommand struct {
	run         func() error
	in          io.Reader
	out, errOut io.Writer
}

func (c *tuiRecoveryCommand) SetStdin(value io.Reader)  { c.in = value }
func (c *tuiRecoveryCommand) SetStdout(value io.Writer) { c.out = value }
func (c *tuiRecoveryCommand) SetStderr(value io.Writer) { c.errOut = value }
func (c *tuiRecoveryCommand) Run() error {
	if c.run == nil {
		return errors.New("missing recovery operation")
	}
	return c.run()
}

func tuiSkillIssues(diagnostics []agentskill.Diagnostic) []tuiissue.Issue {
	issues := make([]tuiissue.Issue, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		issue := tuiissue.New(tui.ViewSkills.String(), "native inventory", diagnostic.Path, string(diagnostic.Kind), diagnostic.Path+"\n"+diagnostic.Message)
		if diagnostic.Path != "" {
			issue.Actions = append(issue.Actions, tuiissue.Action{ID: tuiissue.EditDiagnostic, Label: "open this diagnostic's source file…"})
		}
		issues = append(issues, issue)
	}
	return issues
}
func tuiMCPIssues(diagnostics []agentmcp.Diagnostic) []tuiissue.Issue {
	issues := make([]tuiissue.Issue, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		issue := tuiissue.New(tui.ViewMCP.String(), "native inventory", diagnostic.ConfigPath, string(diagnostic.Code), diagnostic.ConfigPath+"\n"+diagnostic.Message)
		if diagnostic.ConfigPath != "" {
			issue.Actions = append(issue.Actions, tuiissue.Action{ID: tuiissue.EditDiagnostic, Label: "open this diagnostic's source file…"})
		}
		issues = append(issues, issue)
	}
	return issues
}
