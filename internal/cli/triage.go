package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/daviddwlee84/dev-cli/internal/triage"
	"github.com/spf13/cobra"
)

func newTriageCmd(app *App) *cobra.Command {
	var opts triage.Options
	var report, jsonOut bool
	cmd := &cobra.Command{Use: "triage", Short: "Find forgotten local work and preview batches of safe actions", Long: `Inspect ordinary repositories and Tries, including every local branch and registered checkout.

Startup and local refresh never fetch, reconcile Try records, or start runtimes.
A terminal opens the triage interface; --report, --json, or redirected output
produces a read-only report. Remote comparisons use cached tracking refs.

Batch actions require selection, exact preview, and a separate approval.
Commit, rebase, first publication, and whole-repository eviction remain individual
decisions. Ignored files block linked-checkout removal unless their exact directory
was explicitly declared disposable for this clone. Try Trash preserves the whole
directory; confirmed missing Tries can be forgotten only without durable references.
REPOS and TRY selections open a scoped triage view reusing dashboard metadata.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if opts.Kind != "all" && opts.Kind != "repo" && opts.Kind != "try" {
			return errors.New("--kind must be all, repo, or try")
		}
		if opts.StaleDays < 1 {
			return errors.New("--stale-days must be positive")
		}
		s, err := newTriageService(app)
		if err != nil {
			return err
		}
		if report || jsonOut || !app.interactive() {
			r, err := s.Collect(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(r)
			}
			return triage.WriteReport(app.Out, r)
		}
		_, err = runTriageUI(cmd.Context(), app, s, opts)
		return err
	}}
	f := cmd.Flags()
	f.BoolVar(&report, "report", false, "print a read-only text report instead of opening the interface")
	f.BoolVar(&jsonOut, "json", false, "print the additive schema-v1 triage report")
	f.StringVar(&opts.Kind, "kind", "all", "limit items to all, repo, or try")
	f.StringArrayVar(&opts.Roots, "root", nil, "additional local discovery root (repeatable)")
	f.IntVar(&opts.StaleDays, "stale-days", 14, "days without observed activity before an item is an idle candidate")
	f.BoolVar(&opts.All, "all", false, "include archived and other historical catalog locations")
	return cmd
}

func newTriageService(app *App) (*triage.Service, error) {
	return newTriageServiceWithCoverage(app, false)
}

func newTriageServiceWithCoverage(app *App, allAvailable bool) (*triage.Service, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	backends := []runtime.Runtime{}
	backend := app.Cfg.Runtime.Backend
	if app.runtimeOverride != "" {
		backend = app.runtimeOverride
	}
	if !app.noRuntime && backend != "none" {
		if app.runtimeInstance != nil {
			backends = append(backends, app.runtimeInstance)
		}
		if app.runtimeInstance == nil || allAvailable {
			for _, rt := range runtime.All() {
				if rt.Name() != "none" && rt.Available() && (app.runtimeInstance == nil || app.runtimeInstance.Name() != rt.Name()) {
					backends = append(backends, app.runtimeNamed(rt.Name()))
				}
			}
		}
	}
	lifecycle := taskflow.LifecycleConfig{Config: app.Cfg, Tasks: app.Tasks, Artifacts: artifactStore(app), DefaultRuntime: app.Runtime, NamedRuntime: app.runtimeNamed, Host: config.Hostname(), CWD: cwd}
	return triage.New(triage.Config{Config: app.Cfg, Tasks: app.Tasks, Catalog: app.Catalog, Runtimes: backends, Host: config.Hostname(), CWD: cwd, Lifecycle: lifecycle}), nil
}

// The parent Bubble Tea releases its terminal before any native workflow,
// shell, or runtime activation. Returning triggers a fresh local generation.
type triageProcess struct {
	app  App
	item triage.Item
	kind string
}

func (p *triageProcess) SetStdin(in io.Reader)   { p.app.In = in }
func (p *triageProcess) SetStdout(out io.Writer) { p.app.Out = out }
func (p *triageProcess) SetStderr(out io.Writer) { p.app.Err = out }
func (p *triageProcess) Run() error {
	ctx := context.Background()
	i := p.item
	if i.Scope != "directory" && i.Scope != "task" {
		g, e := gitx.Discover(ctx, i.Path)
		if e != nil {
			return e
		}
		common, e := pathx.Canonical(g.GitCommonDir)
		if e != nil || common != i.RepositoryID {
			return errors.New("selected repository identity changed")
		}
	}
	if p.kind == "runtime" {
		if len(i.Runtimes) != 1 {
			return errors.New("select a checkout with exactly one observed runtime; inspect mixed workspaces individually")
		}
		link := i.Runtimes[0]
		rt := p.app.runtimeNamed(link.Backend)
		sessions, e := rt.List(ctx)
		if e != nil {
			return e
		}
		for _, session := range sessions {
			if session.Handle == link.Handle && session.Covers(i.Path) {
				return activateRuntime(ctx, rt, link.Handle)
			}
		}
		return errors.New("runtime no longer covers selected checkout")
	}
	if p.kind == "shell" {
		return p.shell()
	}
	if i.Kind == "try" {
		return p.tryWorkflow()
	}
	deps := defaultFlowCommandDeps()
	launch, e := resolveFlowLaunch(ctx, &p.app, i.RepositoryPath, deps)
	if e != nil {
		return e
	}
	if launch.repository != nil {
		launch.repository.row.FocusTarget = i.Path
		row := launch.repository.row
		launch.preselected = &row
	}
	return runFlow(&p.app, launch, deps.runProgram)
}
func (p *triageProcess) shell() error {
	if info, e := os.Stat(p.item.Path); e != nil || !info.IsDir() {
		return errors.New("selected directory unavailable")
	}
	fmt.Fprintf(p.app.Out, "Selected %s\n", triage.SafeText(p.item.Path))
	if p.item.Branch != nil {
		fmt.Fprintf(p.app.Out, "Branch to inspect: %s (the shell does not switch branches)\n", triage.SafeText(p.item.Branch.Ref))
	}
	fmt.Fprintln(p.app.Out, "Use your Git/editor tools here; exit the shell to return and refresh triage.")
	cmd := exec.Command(shellPath(), "-i")
	cmd.Dir = p.item.Path
	cmd.Stdin = p.app.In
	cmd.Stdout = p.app.Out
	cmd.Stderr = p.app.Err
	return cmd.Run()
}
func (p *triageProcess) tryWorkflow() error {
	i := p.item
	if i.CatalogID == "" {
		fmt.Fprintln(p.app.Out, "This observed Try has no catalog identity yet. Use dev tries list to explicitly reconcile it before lifecycle operations.")
		return p.shell()
	}
	fmt.Fprintf(p.app.Out, "Try %s · %s\nChoose shell, archive, graduate, trash, or back: ", triage.SafeText(i.Name), triage.SafeText(i.Path))
	reader := bufio.NewReader(p.app.In)
	line, e := reader.ReadString('\n')
	if e != nil {
		return e
	}
	var cmd *cobra.Command
	switch strings.TrimSpace(line) {
	case "shell":
		return p.shell()
	case "archive":
		cmd = newTriesArchiveCmd(&p.app)
	case "graduate":
		cmd = newGraduateCmdWithUse(&p.app, "graduate [try]")
	case "trash":
		cmd = newTriesDeleteCmd(&p.app)
	case "back", "":
		return nil
	default:
		return errors.New("unknown Try action")
	}
	// Verify the retained catalog identity before the explicit native action.
	entry, e := p.app.Catalog.Get(i.CatalogID)
	if e != nil {
		return e
	}
	location, ok := entry.LocationFor(config.Hostname())
	if !ok || location.CurrentPath != i.Path || entry.MoveIntent != nil {
		return errors.New("Try identity or location changed")
	}
	cmd.SetContext(context.Background())
	cmd.SetIn(reader)
	cmd.SetOut(p.app.Out)
	cmd.SetErr(p.app.Err)
	p.app.In = reader
	args := []string{i.CatalogID}
	if cmd.Args != nil {
		if e := cmd.Args(cmd, args); e != nil {
			return e
		}
	}
	return cmd.RunE(cmd, args)
}
