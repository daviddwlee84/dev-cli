package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/spf13/cobra"
)

type skillManageRequest struct {
	Refs     []string
	All      bool
	Selected *agentskill.Skill
	Scope    string
	Action   string
}
type skillManageRun struct {
	Receipt agentskill.ManageReceipt
	Targets []agenttarget.Target
	Global  bool
	Mutated bool
}

func newSkillManageCmd(app *App) *cobra.Command {
	var repoRef string
	var all bool
	cmd := &cobra.Command{Use: "manage", Short: "Check, update or restore skills with a scoped wizard", Long: `Manage one skill, project skills, global skills, or several repositories.

The wizard selects scopes and targets, checks Git sources explicitly, previews
changes, then asks before invoking the globally installed skills executable.
Restore from lock and node_modules sync are separate single-project actions.
Listing and update checks do not require Node or skills. No npx fallback runs.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && repoRef != "" {
				return errors.New("choose --repo or --all")
			}
			if !app.canPick() {
				return errors.New("skill manage requires an interactive terminal; use skill list --check or skill update for automation")
			}
			request := skillManageRequest{All: all}
			if repoRef != "" {
				request.Refs = []string{repoRef}
			}
			_, err := runSkillManage(cmd.Context(), app, request)
			if errors.Is(err, errPromptCanceled) || errors.Is(err, picker.ErrCanceled) {
				fmt.Fprintln(app.Out, "Canceled.")
				return nil
			}
			return err
		}}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "start with one repository or exact checkout")
	cmd.Flags().BoolVar(&all, "all", false, "choose among configured repositories with skills-lock.json")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	return cmd
}

func skillPick(ctx context.Context, app *App, prompt string, items []picker.Item, multi bool, selected []string) ([]picker.Item, error) {
	result, attempted, err := app.pick(ctx, picker.Request{Prompt: prompt, Items: items, Multi: multi, Selected: selected})
	if err != nil {
		return nil, err
	}
	if !attempted {
		return nil, errors.New("no matching targets or interactive picker unavailable")
	}
	if multi {
		if len(result.Items) == 0 {
			return nil, errPromptCanceled
		}
		return result.Items, nil
	}
	if result.Item.Value == "" {
		return nil, errPromptCanceled
	}
	return []picker.Item{result.Item}, nil
}

func runSkillManage(ctx context.Context, app *App, req skillManageRequest) (skillManageRun, error) {
	var run skillManageRun
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	cwd, err := os.Getwd()
	if err != nil {
		return run, err
	}
	var targets []agenttarget.Target
	if req.Selected != nil && req.Selected.Scope == agentskill.ScopeProject {
		req.Refs = []string{req.Selected.ScopeRoot}
		req.Scope = "project"
	}
	if req.Selected != nil && req.Selected.Scope == agentskill.ScopeGlobal {
		req.Scope = "global"
	}
	for _, ref := range req.Refs {
		target, e := agenttarget.ResolveRepository(ctx, app.Cfg.DiscoveryRoots(), ref)
		if e != nil {
			return run, e
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 && !req.All && req.Scope != "global" {
		target, e := agenttarget.Current(ctx, cwd)
		if e != nil {
			return run, e
		}
		targets = []agenttarget.Target{target}
	}
	scope := req.Scope
	if scope == "" {
		label := "Current project"
		if len(targets) > 0 {
			label = targets[0].RepoDisplay
		}
		choices := []picker.Item{{Value: "project", Label: "Project skills", Description: label}, {Value: "both", Label: "Project + global skills", Description: "Global scope is handled once"}, {Value: "global", Label: "Global skills"}, {Value: "repos", Label: "Multiple repositories", Description: "Choose repositories containing skills-lock.json"}}
		if req.All || len(targets) > 1 {
			choices = []picker.Item{{Value: "repos", Label: "Selected repository pool"}, {Value: "repos-global", Label: "Repository pool + global skills"}}
		}
		picked, e := skillPick(ctx, app, "Skills scope", choices, false, nil)
		if e != nil {
			return run, e
		}
		scope = picked[0].Value
	}
	if scope == "repos" || scope == "repos-global" {
		if req.All || len(req.Refs) == 0 {
			targets, err = agenttarget.All(ctx, app.Cfg.DiscoveryRoots())
			if err != nil {
				return run, err
			}
		}
		var items []picker.Item
		byKey := map[string]agenttarget.Target{}
		for _, target := range agenttarget.Dedupe(targets) {
			path := filepath.Join(target.CheckoutRoot, "skills-lock.json")
			if _, e := os.Lstat(path); e != nil {
				continue
			}
			byKey[target.Key()] = target
			items = append(items, picker.Item{Value: target.Key(), Label: target.RepoDisplay, Description: config.Contract(target.CheckoutRoot)})
		}
		if len(items) == 0 {
			fmt.Fprintln(app.Out, "No repositories with skills-lock.json found.")
			return run, nil
		}
		picked, e := skillPick(ctx, app, "Repositories", items, true, nil)
		if e != nil {
			return run, e
		}
		targets = nil
		for _, item := range picked {
			targets = append(targets, byKey[item.Value])
		}
	}
	global := scope == "global" || scope == "both" || scope == "repos-global"
	project := scope != "global"
	if !project {
		targets = nil
	}
	run.Targets, run.Global = targets, global
	inventoryResult, err := inventory.CollectAgentSkills(ctx, targets, inventory.AgentSkillOptions{Project: project, Global: global})
	if err != nil {
		return run, err
	}
	renderSkillDiagnostics(app, inventoryResult.Diagnostics)
	rows := inventoryResult.Skills
	if req.Selected != nil {
		filtered := []agentskill.Skill{}
		for _, row := range rows {
			if row.Name == req.Selected.Name && row.Scope == req.Selected.Scope && row.ScopeRoot == req.Selected.ScopeRoot {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	root := cwd
	if len(targets) == 1 {
		root = targets[0].CheckoutRoot
	}
	if scope == "global" {
		root, _ = os.UserHomeDir()
	}
	dependencyRoot := root
	if scope == "global" {
		dependencyRoot = ""
	}
	dependency := agentskill.MutationProviderStatusFor(dependencyRoot)
	fmt.Fprintf(app.Out, "skills provider: %s\n", dependency.Detail)
	action := req.Action
	if action == "" {
		choices := []picker.Item{{Value: "check", Label: "Check for updates", Description: "Read-only network comparison"}, {Value: "update", Label: "Update selected skills", Description: "Check first, then preview confirmed updates"}}
		if len(targets) == 1 && !global && req.Selected != nil && req.Selected.Presence == agentskill.PresenceMissing {
			choices = append(choices, picker.Item{Value: "experimental_install", Label: "Restore this project from lock", Description: "All recorded project skills; sources/ref may be updated"})
		}
		if len(targets) == 1 && !global && req.Selected == nil {
			choices = append(choices, picker.Item{Value: "experimental_install", Label: "Restore from skills-lock.json", Description: "Re-resolve sources; may update the lock"}, picker.Item{Value: "experimental_sync", Label: "Sync skills from node_modules", Description: "Uses installed project dependencies"})
		}
		picked, e := skillPick(ctx, app, "Skills action", choices, false, nil)
		if e != nil {
			return run, e
		}
		action = picked[0].Value
	}
	var ops []agentskill.ManageOperation
	if action == "check" || action == "update" {
		if len(rows) == 0 {
			fmt.Fprintln(app.Out, "No skills found in this scope.")
			return run, nil
		}
		fmt.Fprintln(app.Out, "Checking selected sources…")
		rows = agentskill.CheckUpdates(ctx, rows)
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if err := renderSkillTable(app, rows, targets, project, global); err != nil {
			return run, err
		}
		if action == "check" {
			return run, nil
		}
		var items []picker.Item
		var selected []string
		for i, row := range rows {
			value := strconv.Itoa(i)
			description := string(row.Scope) + " · " + config.Contract(row.ScopeRoot) + " · " + shortUpdate(row.UpdateStatus)
			items = append(items, picker.Item{Value: value, Label: row.Name, Description: description})
			if row.UpdateStatus == agentskill.UpdateAvailable && row.Presence == agentskill.PresencePresent {
				selected = append(selected, value)
			}
		}
		if len(selected) == 0 {
			fmt.Fprintln(app.Out, "No confirmed updates. Missing installations can be restored from lock; inspect unknown or failed checks individually.")
			return run, nil
		}
		picked, e := skillPick(ctx, app, "Skills to update", items, true, selected)
		if e != nil {
			return run, e
		}
		chosen := make([]agentskill.Skill, 0, len(picked))
		for _, item := range picked {
			i, e := strconv.Atoi(item.Value)
			if e != nil || i < 0 || i >= len(rows) {
				return run, errors.New("invalid skill selection")
			}
			chosen = append(chosen, rows[i])
		}
		ops = agentskill.PrepareUpdates(ctx, chosen)
	} else {
		if len(targets) != 1 || global {
			return run, errors.New("native restore/sync requires exactly one project")
		}
		var agents []string
		if action == "experimental_sync" {
			var choices []picker.Item
			for _, definition := range agentskill.Registry() {
				if definition.ProjectSkillsDir != "" {
					choices = append(choices, picker.Item{Value: definition.ID, Label: definition.DisplayName, Description: definition.ProjectSkillsDir})
				}
			}
			picked, e := skillPick(ctx, app, "Project agents to receive dependency skills", choices, true, nil)
			if e != nil {
				return run, e
			}
			for _, item := range picked {
				agents = append(agents, item.Value)
			}
		}
		ops = []agentskill.ManageOperation{agentskill.PrepareNative(ctx, targets[0].CheckoutRoot, action, agents)}
	}
	agentskill.SortManagement(ops)
	ready := 0
	for _, op := range ops {
		fmt.Fprintf(app.Out, "\n%s · %s\n  %s\n", config.Contract(op.Root), op.Scope, op.CommandLabel())
		fmt.Fprintln(app.Out, "  skills: "+strings.Join(op.Names, ", "))
		if op.Blocked != "" {
			fmt.Fprintln(app.Out, "  blocked: "+gitx.SafeDiagnosticText(op.Blocked))
		} else {
			ready++
		}
	}
	if ready == 0 {
		fmt.Fprintln(app.Out, "No eligible operations.")
		return run, nil
	}
	if action == "experimental_install" {
		fmt.Fprintln(app.Out, "Sources/ref are resolved again into .agents/skills; the resulting lock may change. This is native restoration, not exact historical hash reproduction.")
	}
	confirmed, err := newPrompter(app).confirm(fmt.Sprintf("Run %d skills operation(s)", ready), false)
	if err != nil {
		return run, err
	}
	if !confirmed {
		return run, errPromptCanceled
	}
	run.Mutated = true
	run.Receipt = agentskill.ApplyManagement(ctx, ops, app.Out)
	path, saveErr := agentskill.SaveManageReceipt(app.Cfg.StateDir(), run.Receipt)
	if action == "update" && ctx.Err() == nil {
		after, readErr := inventory.CollectAgentSkills(ctx, targets, inventory.AgentSkillOptions{Project: project, Global: global})
		if readErr == nil {
			var affected []agentskill.Skill
			for _, row := range after.Skills {
				for _, op := range ops {
					if op.Blocked != "" || op.Root != row.ScopeRoot || op.Scope != row.Scope {
						continue
					}
					for _, name := range op.Names {
						if row.Lock != nil && row.Lock.Name == name {
							affected = append(affected, row)
						}
					}
				}
			}
			agentskill.CheckUpdates(ctx, affected)
		}
	}
	for _, outcome := range run.Receipt.Outcomes {
		fmt.Fprintf(app.Out, "[%s] %s · %s\n", outcome.Status, config.Contract(outcome.Root), outcome.Skill)
		if outcome.Detail != "" {
			fmt.Fprintln(app.Out, "  "+outcome.Detail)
		}
	}
	if saveErr != nil {
		return run, saveErr
	}
	fmt.Fprintln(app.Out, "Receipt: "+path)
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	for _, outcome := range run.Receipt.Outcomes {
		if outcome.Status != "completed" {
			return run, errors.New("skills management finished with items requiring attention; see receipt")
		}
	}
	return run, nil
}
