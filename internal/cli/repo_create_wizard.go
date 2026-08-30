package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

func runRepoNewWizard(app *App, flags repoBootstrapFlags) (repoWorkflowRequest, bool, error) {
	p := newPrompter(app)
	fmt.Fprintln(p.out, p.style.title("Create a repository"))
	name, err := p.line("Repository name or clone reference", "")
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	if strings.TrimSpace(name) == "" {
		return repoWorkflowRequest{}, false, fmt.Errorf("repository name is required")
	}
	if looksLikeCloneReference(name) {
		if err := validateNewAsCloneFlags(flags); err != nil {
			return repoWorkflowRequest{}, false, err
		}
		fmt.Fprintln(p.out, "  "+p.style.dim("detected a clone reference; source history and remote will be preserved"))
		request, _, confirmed, err := promptRepoCloneWizard(app, p, flags, name, false)
		return request, confirmed, err
	}
	flags.category, err = p.line("Category (optional)", flags.category)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	defaultDestination, err := repoDestination(app.Cfg.Paths.ProjectRoot, flags.category, name, flags.path)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	destination, err := p.line("Destination", config.Contract(defaultDestination))
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	flags.path = config.Expand(destination)

	catalog, _, err := loadRepoScaffoldConfig(app, "", false)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	defaultPreset := interactiveDefaultPreset(catalog)
	flags.preset, err = promptScaffoldPreset(p, catalog, defaultPreset)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	preset, err := catalog.ResolvePreset(flags.preset)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	flags.description, err = p.line("Description (optional)", flags.description)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	customize, err := p.confirm("Customize preset and template options?", repoWizardCustomizationSelected(flags) || presetRequiresWizardInput(preset))
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	if customize {
		templateDefault := preset.Template
		if flags.template != "" {
			templateDefault = flags.template
		}
		flags.template, err = p.lineWithDisplayFallback(
			"Template source (optional; local path, Git URL, or owner/repo)",
			templateDefault,
			repo.RedactCloneRef(templateDefault),
		)
		if err != nil {
			return repoWorkflowRequest{}, false, err
		}
		if strings.EqualFold(strings.TrimSpace(flags.template), "none") {
			flags.templateRef, flags.templateSubdir = "", ""
		} else if strings.TrimSpace(flags.template) != "" {
			refDefault := preset.TemplateRef
			if flags.templateRef != "" {
				refDefault = flags.templateRef
			}
			flags.templateRef, err = p.line("Template ref (optional branch/tag/commit)", refDefault)
			if err != nil {
				return repoWorkflowRequest{}, false, err
			}
			subdirDefault := preset.TemplateSubdir
			if flags.templateSubdir != "" {
				subdirDefault = flags.templateSubdir
			}
			flags.templateSubdir, err = p.line("Template subdirectory (optional)", subdirDefault)
			if err != nil {
				return repoWorkflowRequest{}, false, err
			}
		}
		if err := promptScaffoldOptions(p, catalog, flags.preset, &flags); err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	if flags.remote || preset.Remote == "ask" {
		if err := promptUpstream(app, p, name, &flags); err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	checkInDefault := presetInitialCheckIn(preset)
	if flags.checkIn != "" && flags.checkIn != string(repoCheckInAuto) {
		checkInDefault = flags.checkIn
	}
	if flags.remote && (flags.checkIn == "" || flags.checkIn == string(repoCheckInAuto)) {
		flags.checkIn = string(repoCheckInCommit)
		fmt.Fprintln(p.out, "  check-in     commit (required before publishing)")
	} else {
		flags.checkIn, err = promptRepoCheckIn(p, checkInDefault)
		if err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	handoffDefault := defaultString(flags.handoff, defaultString(preset.Handoff, "cd"))
	flags.handoff, err = promptRepoHandoff(p, handoffDefault)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	request, err := buildNewRepoRequest(app, name, flags)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	fmt.Fprintln(p.out)
	renderPreparedRepoScaffold(app, request.Prepared)
	renderRepoWorkflowSummary(app, request)
	confirmed, err := p.confirm("Create this repository?", true)
	return request, confirmed, err
}

func runRepoCloneWizard(app *App, flags repoBootstrapFlags) (repoWorkflowRequest, bool, bool, error) {
	p := newPrompter(app)
	fmt.Fprintln(p.out, p.style.title("Clone a repository"))
	ref, err := p.line("Git URL, path, or owner/name", "")
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	return promptRepoCloneWizard(app, p, flags, ref, true)
}

func promptRepoCloneWizard(app *App, p *prompter, flags repoBootstrapFlags, ref string, offerSetup bool) (repoWorkflowRequest, bool, bool, error) {
	name := repoNameFromRef(repo.RedactCloneRef(ref))
	if name == "" {
		return repoWorkflowRequest{}, false, false, fmt.Errorf("could not derive a repository name from %q", ref)
	}
	category, err := p.line("Category (optional)", flags.category)
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	flags.category = category
	defaultDestination, err := repoDestination(app.Cfg.Paths.ProjectRoot, flags.category, name, flags.path)
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	destination, err := p.line("Destination", config.Contract(defaultDestination))
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	flags.path = config.Expand(destination)
	setup := flags.preset != ""
	if offerSetup && !setup {
		setup, err = p.confirm("Apply a repository setup preset after cloning?", false)
		if err != nil {
			return repoWorkflowRequest{}, false, false, err
		}
	}
	flags.handoff, err = promptRepoHandoff(p, defaultString(flags.handoff, "cd"))
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	request, err := buildCloneRepoRequest(app, ref, flags)
	if err != nil {
		return repoWorkflowRequest{}, false, false, err
	}
	fmt.Fprintln(p.out, "\n"+p.style.title("Summary"))
	fmt.Fprintf(p.out, "  source       %s\n", repo.RedactCloneRef(ref))
	fmt.Fprintf(p.out, "  destination  %s\n", config.Contract(request.Destination))
	if request.Scaffold.Preset != "" {
		fmt.Fprintf(p.out, "  setup        %s\n", request.Scaffold.Preset)
	} else {
		fmt.Fprintf(p.out, "  setup        %t\n", setup)
	}
	fmt.Fprintf(p.out, "  handoff      %s\n", request.Handoff)
	confirmed, err := p.confirm("Clone this repository?", true)
	return request, setup, confirmed, err
}

func runRepoSetupWizard(app *App, root string, flags repoBootstrapFlags) (repoWorkflowRequest, bool, error) {
	p := newPrompter(app)
	fmt.Fprintln(p.out, p.style.title("Set up an existing repository"))
	catalog, project, err := loadRepoScaffoldConfig(app, root, true)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	defaultPreset := interactiveDefaultPreset(catalog)
	if project.Effective.Repo.Setup.Preset != nil {
		defaultPreset = *project.Effective.Repo.Setup.Preset
	}
	flags.preset, err = promptScaffoldPreset(p, catalog, defaultPreset)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	preset, err := catalog.ResolvePreset(flags.preset)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	customize, err := p.confirm("Customize preset options?", repoWizardCustomizationSelected(flags) || presetRequiresWizardInput(preset))
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	if customize {
		if err := promptScaffoldOptions(p, catalog, flags.preset, &flags); err != nil {
			return repoWorkflowRequest{}, false, err
		}
		flags.importOrphans, err = p.confirm("Import matching orphan Claude plans?", flags.importOrphans)
		if err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	if gitRemote := gitRemoteAt(root); gitRemote == "" && (flags.remote || preset.Remote == "ask") {
		if err := promptUpstream(app, p, filepath.Base(root), &flags); err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	checkInDefault := string(repoCheckInNone)
	if project.Effective.Repo.Setup.Commit != nil && *project.Effective.Repo.Setup.Commit {
		checkInDefault = string(repoCheckInCommit)
	}
	if project.Effective.Repo.Setup.CheckIn != nil {
		checkInDefault = *project.Effective.Repo.Setup.CheckIn
	}
	if flags.commit {
		checkInDefault = string(repoCheckInCommit)
	}
	if flags.checkIn != "" && flags.checkIn != string(repoCheckInAuto) {
		checkInDefault = flags.checkIn
	}
	if flags.remote && (flags.checkIn == "" || flags.checkIn == string(repoCheckInAuto)) {
		flags.checkIn = string(repoCheckInCommit)
		fmt.Fprintln(p.out, "  check-in     commit (required before publishing)")
	} else {
		flags.checkIn, err = promptRepoCheckIn(p, checkInDefault)
		if err != nil {
			return repoWorkflowRequest{}, false, err
		}
	}
	handoffDefault := defaultString(preset.Handoff, "stay")
	if project.Effective.Repo.Setup.Handoff != nil {
		handoffDefault = *project.Effective.Repo.Setup.Handoff
	}
	if flags.handoff != "" {
		handoffDefault = flags.handoff
	}
	flags.handoff, err = promptRepoHandoff(p, handoffDefault)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	request, err := buildSetupRepoRequest(app, root, flags)
	if err != nil {
		return repoWorkflowRequest{}, false, err
	}
	fmt.Fprintln(p.out)
	renderPreparedRepoScaffold(app, request.Prepared)
	renderRepoWorkflowSummary(app, request)
	confirmed, err := p.confirm("Apply this repository setup?", true)
	return request, confirmed, err
}

func promptScaffoldPreset(p *prompter, catalog scaffold.Config, fallback string) (string, error) {
	names := scaffoldPresetNames(catalog)
	fmt.Fprintln(p.out, p.style.title("Presets:"))
	for _, name := range names {
		preset, _ := catalog.ResolvePreset(name)
		fmt.Fprintf(p.out, "  %-16s %s\n", name, preset.Description)
	}
	for {
		value, err := p.line("Preset", fallback)
		if err != nil {
			return "", err
		}
		if _, ok := catalog.Presets[value]; ok {
			return value, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning("choose one of: "+strings.Join(names, ", ")))
	}
}

func promptScaffoldOptions(p *prompter, catalog scaffold.Config, presetName string, flags *repoBootstrapFlags) error {
	preset, err := catalog.ResolvePreset(presetName)
	if err != nil {
		return err
	}
	for _, input := range preset.Inputs {
		label := input.Label
		if label == "" {
			label = input.ID
		}
		fallback := parseInputDefault(input.Default)
		switch input.Type {
		case scaffold.InputBool:
			value, _ := strconvParseBoolDefault(fallback)
			chosen, err := p.confirm(label, value)
			if err != nil {
				return err
			}
			flags.set = replaceSetValue(flags.set, input.ID, fmt.Sprint(chosen))
		case scaffold.InputChoice:
			choices := map[string]string{}
			for _, choice := range input.Choices {
				choices[strings.ToLower(choice)] = choice
			}
			if fallback == "" && len(input.Choices) > 0 {
				fallback = input.Choices[0]
			}
			value, err := p.choice(label+" ("+strings.Join(input.Choices, "/")+")", fallback,
				strings.Join(input.Choices, ", "), choices)
			if err != nil {
				return err
			}
			flags.set = replaceSetValue(flags.set, input.ID, value)
		default:
			for {
				value, err := p.line(label, fallback)
				if err != nil {
					return err
				}
				if input.IsRequired() && strings.TrimSpace(value) == "" {
					fmt.Fprintln(p.out, "  "+p.style.warning(label+" is required"))
					continue
				}
				flags.set = replaceSetValue(flags.set, input.ID, value)
				break
			}
		}
	}

	readme := preset.Readme != nil && *preset.Readme
	readme, err = p.confirm("Create README.md?", readme)
	if err != nil {
		return err
	}
	if presetHasItem(preset, "readme") {
		setSelection(flags, "readme", readme)
	}

	gitignoreFallback := strings.Join(preset.Gitignore, ",")
	gitignore, err := p.line("Gitignore templates (comma-separated, blank for common only)", gitignoreFallback)
	if err != nil {
		return err
	}
	flags.gitignore = splitCommaValues(gitignore)

	licenseFallback := preset.License
	if licenseFallback == "ask" || licenseFallback == "" {
		licenseFallback = "none"
	}
	flags.license, err = p.line("License (none/mit/apache-2.0/...)", licenseFallback)
	if err != nil {
		return err
	}
	if flags.license != "" && flags.license != "none" {
		flags.licenseHolder, err = p.line("License holder", flags.licenseHolder)
		if err != nil {
			return err
		}
	}

	if preset.ClaudePlans != nil || presetHasItem(preset, "claude-settings") || presetHasItem(preset, "claude-plans-directory") {
		claudePlans := preset.ClaudePlans != nil && *preset.ClaudePlans
		claudePlans, err = p.confirm("Keep Claude plans in .claude/plans?", claudePlans)
		if err != nil {
			return err
		}
		if presetHasItem(preset, "claude-settings") {
			setSelection(flags, "claude-settings", claudePlans)
		}
		if presetHasItem(preset, "claude-plans-directory") {
			setSelection(flags, "claude-plans-directory", claudePlans)
		}
	}

	if preset.AgentContract != "" || presetHasItem(preset, "agent-contract") {
		agentContract := preset.AgentContract != "" && preset.AgentContract != "none"
		agentContract, err = p.confirm("Create AGENTS.md guidance?", agentContract)
		if err != nil {
			return err
		}
		if presetHasItem(preset, "agent-contract") {
			setSelection(flags, "agent-contract", agentContract)
		}
	}

	for _, item := range preset.Catalog {
		selected, err := p.confirm("Enable "+item.Label+"?", item.IsDefault())
		if err != nil {
			return err
		}
		setSelection(flags, item.ID, selected)
	}
	if len(preset.Skills) > 0 {
		agentDefault := catalog.DefaultAgents
		if flags.agents != nil {
			agentDefault = flags.agents
		}
		agents, err := p.line("Skill agents (comma-separated)", strings.Join(agentDefault, ","))
		if err != nil {
			return err
		}
		flags.agents = splitCommaValues(agents)
		if len(flags.agents) == 0 {
			return fmt.Errorf("at least one skill agent is required")
		}
	}
	flags.browseSkills, err = p.confirm("Browse additional skills in the upstream installer?", flags.browseSkills)
	return err
}

func promptUpstream(app *App, p *prompter, name string, flags *repoBootstrapFlags) error {
	create, err := p.confirm("Create a GitHub/GitLab upstream?", flags.remote)
	if err != nil || !create {
		flags.remote = false
		return err
	}
	ready := forge.ProbeAll(ctxOf())
	var available []forge.Readiness
	for _, candidate := range ready {
		if candidate.Ready() {
			available = append(available, candidate)
		} else {
			fmt.Fprintf(p.out, "  %s: %s (%s)\n", candidate.Forge, candidate.Status, candidate.Action)
		}
	}
	if len(available) == 0 {
		fmt.Fprintln(p.out, "  "+p.style.warning("no authenticated GitHub or GitLab CLI is ready; creating locally only"))
		flags.remote = false
		return nil
	}
	selected := available[0].Forge
	if len(available) > 1 {
		choices := map[string]string{"github": "github", "gh": "github", "gitlab": "gitlab", "gl": "gitlab"}
		choice, err := p.choice("Forge (github/gitlab)", string(selected), "github, gitlab", choices)
		if err != nil {
			return err
		}
		selected = forge.Kind(choice)
	}
	flags.remote = true
	flags.forge = string(selected)
	flags.namespace, err = p.line("Owner / organization / namespace (optional)", flags.namespace)
	if err != nil {
		return err
	}
	flags.visibility, err = p.choice("Visibility (private/public/internal)", "private",
		"private, public, internal", map[string]string{
			"private": "private", "public": "public", "internal": "internal",
		})
	if err != nil {
		return err
	}
	flags.push, err = p.confirm("Push the initial branch?", true)
	_ = name
	return err
}

func promptRepoHandoff(p *prompter, fallback string) (string, error) {
	return p.choice("Afterwards (stay/cd/open/start)", fallback, "stay, cd, open, start", map[string]string{
		"stay": "stay", "s": "stay", "cd": "cd", "c": "cd",
		"open": "open", "o": "open", "start": "start", "t": "start",
	})
}

func promptRepoCheckIn(p *prompter, fallback string) (string, error) {
	return p.choice("Check-in generated changes (commit/stage/none)", fallback, "commit, stage, none", map[string]string{
		"commit": "commit", "c": "commit",
		"stage": "stage", "s": "stage",
		"none": "none", "n": "none",
	})
}

func presetInitialCheckIn(preset scaffold.Preset) string {
	if preset.InitialCheckIn != "" {
		return preset.InitialCheckIn
	}
	if preset.InitialCommit == nil || *preset.InitialCommit {
		return string(repoCheckInCommit)
	}
	return string(repoCheckInNone)
}

func repoWizardCustomizationSelected(flags repoBootstrapFlags) bool {
	return flags.template != "" || flags.templateRef != "" || flags.templateSubdir != "" ||
		flags.gitignore != nil || flags.license != "" || flags.licenseHolder != "" ||
		len(flags.set) > 0 || len(flags.enable) > 0 || len(flags.disable) > 0 ||
		flags.agents != nil || flags.browseSkills || flags.importOrphans
}

func presetRequiresWizardInput(preset scaffold.Preset) bool {
	for _, input := range preset.Inputs {
		if input.IsRequired() && strings.TrimSpace(parseInputDefault(input.Default)) == "" {
			return true
		}
	}
	return false
}

func renderRepoWorkflowSummary(app *App, request repoWorkflowRequest) {
	fmt.Fprintln(app.Out, app.outStyle().title("Repository workflow"))
	if template := repoTemplateSummary(request.Template); template != nil {
		selection := template.Source
		if template.Ref != "" {
			selection += "@" + template.Ref
		}
		if template.Subdir != "" {
			selection += "//" + template.Subdir
		}
		fmt.Fprintf(app.Out, "  template   %s (%d files)\n", selection, template.Files)
		renderRepoTemplateFilePreview(app, request.Template)
	}
	checkIn, message := plannedRepoCheckIn(request)
	if message == "" {
		fmt.Fprintf(app.Out, "  check-in   %s\n", checkIn)
	} else {
		fmt.Fprintf(app.Out, "  check-in   %s (%s)\n", checkIn, message)
	}
	fmt.Fprintf(app.Out, "  handoff    %s\n", request.Handoff)
	if request.Publish == nil {
		fmt.Fprintln(app.Out, "  upstream   local only")
	} else {
		fullName := request.Publish.Name
		if request.Publish.Namespace != "" {
			fullName = request.Publish.Namespace + "/" + fullName
		}
		fmt.Fprintf(app.Out, "  upstream   %s:%s (%s, push=%t)\n",
			request.Publish.Forge, fullName, request.Publish.Visibility, request.Publish.Push)
	}
}

func setSelection(flags *repoBootstrapFlags, id string, enabled bool) {
	flags.enable = removeString(flags.enable, id)
	flags.disable = removeString(flags.disable, id)
	if enabled {
		flags.enable = append(flags.enable, id)
	} else {
		flags.disable = append(flags.disable, id)
	}
}

func replaceSetValue(values []string, key, value string) []string {
	prefix := key + "="
	result := values[:0]
	for _, existing := range values {
		if !strings.HasPrefix(existing, prefix) {
			result = append(result, existing)
		}
	}
	return append(result, prefix+value)
}

func removeString(values []string, remove string) []string {
	result := values[:0]
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

func splitCommaValues(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func interactiveDefaultPreset(catalog scaffold.Config) string {
	if len(catalog.Sources) > 1 && catalog.DefaultPreset != "" {
		return catalog.DefaultPreset
	}
	if _, ok := catalog.Presets["agent-ready"]; ok {
		return "agent-ready"
	}
	return catalog.DefaultPreset
}

func strconvParseBoolDefault(value string) (bool, bool) {
	parsed, err := strconv.ParseBool(value)
	return parsed, err == nil
}

func gitRemoteAt(root string) string { return gitx.Remote(ctxOf(), root, "origin") }
