package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/licenses"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

type repoScaffoldRequest struct {
	Root          string
	Name          string
	Description   string
	Category      string
	Preset        string
	Inputs        map[string]any
	Selections    map[string]bool
	Gitignore     []string
	License       string
	LicenseHolder string
	ImportOrphans bool
	SkillAgents   []string
	Existing      bool
	Variables     map[string]any
}

type preparedRepoScaffold struct {
	Config  scaffold.Config
	Project projectconfig.Result
	Plan    scaffold.Plan
	Init    repoInitSelection
}

type appliedRepoScaffold struct {
	Files  []scaffold.WriteResult `json:"files,omitempty"`
	Native repoInitResult         `json:"native"`
}

func prepareRepoScaffold(app *App, request repoScaffoldRequest) (preparedRepoScaffold, error) {
	catalog, project, err := loadRepoScaffoldConfig(app, request.Root, request.Existing)
	if err != nil {
		return preparedRepoScaffold{}, err
	}
	presetName := request.Preset
	if presetName == "" && request.Existing && project.Effective.Repo.Setup.Preset != nil {
		presetName = *project.Effective.Repo.Setup.Preset
	}
	if presetName == "" {
		presetName = catalog.DefaultPreset
	}
	preset, err := catalog.ResolvePreset(presetName)
	if err != nil {
		return preparedRepoScaffold{}, err
	}
	selections := map[string]bool{}
	for key, value := range request.Selections {
		selections[key] = value
	}
	if preset.Readme != nil && !*preset.Readme && presetHasItem(preset, "readme") {
		selections["readme"] = false
	}
	if preset.ClaudePlans != nil && !*preset.ClaudePlans {
		if presetHasItem(preset, "claude-settings") {
			selections["claude-settings"] = false
		}
		if presetHasItem(preset, "claude-plans-directory") {
			selections["claude-plans-directory"] = false
		}
	}
	if (strings.TrimSpace(preset.AgentContract) == "" || preset.AgentContract == "none") && presetHasItem(preset, "agent-contract") {
		selections["agent-contract"] = false
	}
	variables := map[string]any{
		"description": request.Description,
		"category":    request.Category,
	}
	for key, value := range request.Variables {
		variables[key] = value
	}
	plan, err := scaffold.BuildPlan(catalog, scaffold.PlanOptions{
		Preset: presetName, Root: request.Root, Name: request.Name,
		Inputs: request.Inputs, Variables: variables, Selections: selections,
	})
	if err != nil {
		return preparedRepoScaffold{}, err
	}
	if request.SkillAgents != nil {
		for index := range plan.Skills {
			plan.Skills[index].Agents = append([]string(nil), request.SkillAgents...)
		}
	}
	readmeWanted := preset.Readme != nil && *preset.Readme
	if selected, ok := selections["readme"]; ok {
		readmeWanted = selected
	}
	agentWanted := strings.TrimSpace(preset.AgentContract) != "" && preset.AgentContract != "none"
	if selected, ok := selections["agent-contract"]; ok {
		agentWanted = selected
	}
	claudeWanted := preset.ClaudePlans != nil && *preset.ClaudePlans
	if selected, ok := selections["claude-settings"]; ok {
		claudeWanted = selected
	}
	gitignore := preset.Gitignore
	if request.Gitignore != nil {
		gitignore = request.Gitignore
	}
	license := preset.License
	if request.License != "" {
		license = request.License
	}
	if strings.EqualFold(license, "ask") {
		license = "none"
	}
	if license != "" && !strings.EqualFold(license, "none") && !strings.EqualFold(license, "ask") {
		fetcher := licenses.NewFetcher(filepath.Join(config.CacheHome(), "dev", "licenses"))
		fetcher.NoWrite = true
		if _, err := fetcher.Get(ctxOf(), license); err != nil {
			return preparedRepoScaffold{}, err
		}
	}
	init := repoInitSelection{
		Name: request.Name, Description: request.Description,
		README:    readmeWanted && !planHasDestination(plan, "README.md"),
		Gitignore: gitignore, License: license, LicenseHolder: request.LicenseHolder,
		ClaudePlans:   claudeWanted,
		ImportOrphans: request.ImportOrphans,
		AgentContract: agentWanted && !planHasDestination(plan, "AGENTS.md"),
	}
	return preparedRepoScaffold{Config: catalog, Project: project, Plan: plan, Init: init}, nil
}

func planHasDestination(plan scaffold.Plan, relative string) bool {
	relative = filepath.ToSlash(filepath.Clean(relative))
	for _, file := range plan.Files {
		if filepath.ToSlash(filepath.Clean(file.RelativePath)) == relative {
			return true
		}
	}
	return false
}

func presetHasItem(preset scaffold.Preset, id string) bool {
	for _, file := range preset.Files {
		if file.ID == id {
			return true
		}
	}
	for _, hook := range preset.Hooks {
		if hook.ID == id {
			return true
		}
	}
	for _, skill := range preset.Skills {
		if skill.ID == id {
			return true
		}
	}
	for _, item := range preset.Catalog {
		if item.ID == id {
			return true
		}
	}
	return false
}

func loadRepoScaffoldConfig(app *App, root string, includeProject bool) (scaffold.Config, projectconfig.Result, error) {
	var paths []string
	global := app.scaffoldsPath
	if global == "" {
		global = config.ScaffoldsFile()
		if _, err := os.Stat(global); errors.Is(err, os.ErrNotExist) {
			global = ""
		} else if err != nil {
			return scaffold.Config{}, projectconfig.Result{}, err
		}
	}
	if global != "" {
		paths = append(paths, global)
	}

	var project projectconfig.Result
	if includeProject {
		legacy := legacyProjectLayer(root)
		loaded, err := projectconfig.Load(root, legacy)
		if err != nil {
			return scaffold.Config{}, project, err
		}
		project = loaded
		if project.ScaffoldsPresent {
			paths = append(paths, project.Paths.Scaffolds)
		}
	}
	catalog, err := scaffold.Load(paths...)
	if err != nil {
		return scaffold.Config{}, project, err
	}
	return catalog, project, nil
}

func legacyProjectLayer(root string) *projectconfig.Layer {
	override, ok := wt.LoadRepoOverride(root)
	if !ok {
		return nil
	}
	layer := &projectconfig.Layer{Source: filepath.Join(root, wt.OverrideFilename)}
	if override.Worktree.Include != nil {
		value := append([]string(nil), override.Worktree.Include...)
		layer.Override.Worktree.Include = &value
	}
	if override.Worktree.Link != nil {
		value := append([]string(nil), override.Worktree.Link...)
		layer.Override.Worktree.Link = &value
	}
	if override.Worktree.PostCreate != nil {
		value := *override.Worktree.PostCreate
		layer.Override.Worktree.PostCreate = &value
	}
	if override.Worktree.Strategy != "" {
		value := override.Worktree.Strategy
		layer.Override.Worktree.Strategy = &value
	}
	if override.Worktree.Strategies != nil {
		value := map[string]string{}
		for key, strategy := range override.Worktree.Strategies {
			value[key] = strategy
		}
		layer.Override.Worktree.Strategies = &value
	}
	return layer
}

func applyPreparedRepoScaffold(prepared preparedRepoScaffold, existing bool) (appliedRepoScaffold, error) {
	policy := scaffold.ExistingError
	if existing {
		policy = scaffold.ExistingSkip
	}
	files, err := scaffold.ApplyFiles(prepared.Plan, policy)
	if err != nil {
		return appliedRepoScaffold{}, err
	}
	native, err := applyRepoInitializers(ctxOf(), prepared.Plan.Root, prepared.Init)
	if err != nil {
		return appliedRepoScaffold{Files: files}, err
	}
	return appliedRepoScaffold{Files: files, Native: native}, nil
}

func repoHooksFromPlan(plan scaffold.Plan) []repoHookSpec {
	result := make([]repoHookSpec, 0, len(plan.Hooks))
	for _, hook := range plan.Hooks {
		result = append(result, repoHookSpec{
			ID: hook.ID, Phase: repoHookPhase(hook.Phase), Command: hook.Command,
			Run: hook.Run, Interactive: hook.Interactive, Required: hook.Required,
			Timeout: hook.Timeout.Duration,
		})
	}
	return result
}

func repoSkillsFromPlan(plan scaffold.Plan) []repoSkillSpec {
	result := make([]repoSkillSpec, 0, len(plan.Skills))
	for _, skill := range plan.Skills {
		converted := repoSkillSpec{ID: skill.ID, Source: skill.Source, Name: skill.Name, Agents: skill.Agents}
		if skill.Setup != nil {
			converted.Setup = &repoSkillSetup{
				Phase: repoHookPhase(skill.Setup.Phase), Interpreter: skill.Setup.Interpreter,
				Script: skill.Setup.Script, Builtin: skill.Setup.Builtin, Args: skill.Setup.Args,
				Required: skill.Setup.Required, Timeout: skill.Setup.Timeout.Duration,
			}
		}
		result = append(result, converted)
	}
	return result
}

func ensureProjectConfigTrust(app *App, root string, project projectconfig.Result, plan scaffold.Plan) error {
	for _, diagnostic := range project.Diagnostics {
		app.warnf("%s", diagnostic.Message)
	}
	for _, skill := range plan.Skills {
		if skill.Setup != nil && skill.Setup.Builtin == "" && project.ScaffoldsPresent && skill.Origin == project.Paths.Scaffolds &&
			!projectconfig.IsLocalSkillSource(skill.Source) {
			return fmt.Errorf("project-authored skill %s declares setup from mutable remote source %q; project skill setup must use a local source whose content can be trusted", skill.Name, skill.Source)
		}
	}
	if !project.RequiresTrust() || !projectExecutionAffectsPlan(project, plan) {
		return nil
	}
	store := projectconfig.NewTrustStore(filepath.Join(app.Cfg.StateDir(), "trust", "project-config-v1.json"))
	trusted, err := store.Check(ctxOf(), root, project.ExecutionHash)
	if err != nil {
		return err
	}
	if trusted {
		return nil
	}
	if !app.interactive() {
		return fmt.Errorf("project configuration can execute commands and is not trusted; review it, then run dev config trust %s --yes", config.Contract(root))
	}
	p := newPrompter(app)
	ok, err := p.confirm("Trust this repository's executable .dev-cli configuration hash?", false)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("project configuration was not trusted; nothing was executed")
	}
	_, err = store.Approve(ctxOf(), root, project.ExecutionHash)
	return err
}

func projectExecutionAffectsPlan(project projectconfig.Result, plan scaffold.Plan) bool {
	// A project-owned default_preset/extends selector can activate executable
	// items inherited from a global preset. In that case the effective hook or
	// skill retains its global Origin, but the repository still made the choice
	// that caused it to run and therefore must be trusted.
	if project.ScaffoldsPresent && (len(plan.Hooks) > 0 || len(plan.Skills) > 0) {
		return true
	}
	for _, hook := range plan.Hooks {
		if project.ScaffoldsPresent && hook.Origin == project.Paths.Scaffolds {
			return true
		}
	}
	for _, skill := range plan.Skills {
		if project.ScaffoldsPresent && skill.Origin == project.Paths.Scaffolds && skill.Setup != nil {
			return true
		}
	}
	if source, ok := project.SourceFor("repo.setup.preset"); ok && source == project.Paths.Config {
		return len(plan.Hooks) > 0 || len(plan.Skills) > 0
	}
	return false
}

func parseSetValues(values []string) (map[string]any, error) {
	result := map[string]any{}
	for _, value := range values {
		key, raw, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("--set %q: want key=value", value)
		}
		result[key] = strings.TrimSpace(raw)
	}
	return result, nil
}

func parseSelectionValues(enable, disable []string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, values := range []struct {
		items []string
		value bool
	}{{enable, true}, {disable, false}} {
		for _, item := range values.items {
			item = strings.TrimSpace(item)
			if item == "" {
				return nil, fmt.Errorf("scaffold item id must not be empty")
			}
			if previous, ok := result[item]; ok && previous != values.value {
				return nil, fmt.Errorf("scaffold item %q is both enabled and disabled", item)
			}
			result[item] = values.value
		}
	}
	return result, nil
}

func renderPreparedRepoScaffold(app *App, prepared preparedRepoScaffold) {
	plan := prepared.Plan
	fmt.Fprintln(app.Out, app.outStyle().title("Scaffold"))
	fmt.Fprintf(app.Out, "  preset     %s\n", plan.Preset)
	fmt.Fprintf(app.Out, "  destination %s\n", config.Contract(plan.Root))
	if len(plan.Files) > 0 {
		fmt.Fprintln(app.Out, "  files")
		for _, file := range plan.Files {
			fmt.Fprintf(app.Out, "    - %s\n", file.RelativePath)
		}
	}
	var native []string
	if prepared.Init.README {
		native = append(native, "README.md")
	}
	if prepared.Init.AgentContract {
		native = append(native, "AGENTS.md")
	}
	if prepared.Init.Gitignore != nil {
		native = append(native, ".gitignore (merge)")
	}
	if key := strings.TrimSpace(prepared.Init.License); key != "" && !strings.EqualFold(key, "none") {
		native = append(native, "LICENSE")
	}
	if prepared.Init.ClaudePlans {
		native = append(native, ".claude/settings.json (merge)", ".claude/plans/")
	}
	if prepared.Init.ImportOrphans {
		native = append(native, "matching orphan Claude plans (copy)")
	}
	if len(native) > 0 {
		fmt.Fprintln(app.Out, "  native setup")
		for _, item := range native {
			fmt.Fprintf(app.Out, "    - %s\n", item)
		}
	}
	if len(plan.Skills) > 0 {
		fmt.Fprintln(app.Out, "  skills")
		for _, skill := range plan.Skills {
			fmt.Fprintf(app.Out, "    - %s (%s)\n", skill.Name, strings.Join(skill.Agents, ", "))
			if skill.Setup != nil {
				command := append([]string{skill.Setup.Script}, skill.Setup.Args...)
				if skill.Setup.Builtin != "" {
					command = append([]string{"dev:builtin:" + skill.Setup.Builtin}, skill.Setup.Args...)
				} else if skill.Setup.Interpreter != "" {
					command = append([]string{skill.Setup.Interpreter}, command...)
				}
				fmt.Fprintf(app.Out, "      setup [%s] %s\n", skill.Setup.Phase, strings.Join(command, " "))
			}
		}
	}
	if len(plan.Hooks) > 0 {
		fmt.Fprintln(app.Out, "  hooks")
		for _, hook := range plan.Hooks {
			what := hook.Run
			if len(hook.Command) > 0 {
				what = strings.Join(hook.Command, " ")
			}
			fmt.Fprintf(app.Out, "    - %s [%s] %s\n", hook.ID, hook.Phase, what)
		}
	}
}

func scaffoldPresetNames(cfg scaffold.Config) []string {
	names := make([]string, 0, len(cfg.Presets))
	for name := range cfg.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
