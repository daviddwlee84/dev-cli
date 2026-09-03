package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/repotemplate"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
	"github.com/spf13/cobra"
)

type repoBootstrapFlags struct {
	category       string
	path           string
	preset         string
	handoff        string
	description    string
	gitignore      []string
	license        string
	licenseHolder  string
	set            []string
	enable         []string
	disable        []string
	agents         []string
	browseSkills   bool
	importOrphans  bool
	dryRun         bool
	yes            bool
	json           bool
	open           bool
	checkIn        string
	template       string
	templateRef    string
	templateSubdir string

	remote     bool
	forge      string
	namespace  string
	visibility string
	private    bool
	public     bool
	push       bool

	commit  bool
	message string
}

type repoWorkflowRequest struct {
	Kind          repo.AcquireKind
	Name          string
	Ref           string
	Destination   string
	Scaffold      repoScaffoldRequest
	Prepared      preparedRepoScaffold
	Template      *repotemplate.Snapshot
	Handoff       repoHandoff
	Publish       *repoPublishRequest
	BrowseSkills  bool
	CheckIn       repoCheckIn
	CommitMessage string
	DryRun        bool
	JSON          bool
}

type repoWorkflowResult struct {
	Operation           string                    `json:"operation"`
	Path                string                    `json:"path"`
	Preset              string                    `json:"preset,omitempty"`
	Created             bool                      `json:"created"`
	Cloned              bool                      `json:"cloned"`
	Scaffold            appliedRepoScaffold       `json:"scaffold"`
	Skills              repoSkillResult           `json:"skills"`
	Hooks               []repoHookResult          `json:"hooks,omitempty"`
	Commit              bool                      `json:"committed"`
	Staged              bool                      `json:"staged"`
	StagedPaths         int                       `json:"staged_paths,omitempty"`
	CommitMessage       string                    `json:"commit_message,omitempty"`
	CommitDraftProvider string                    `json:"commit_draft_provider,omitempty"`
	Template            *repotemplate.ApplyResult `json:"template,omitempty"`
	Remote              *repoPublishResult        `json:"remote,omitempty"`
	Handoff             repoHandoff               `json:"handoff"`
	Warnings            []string                  `json:"warnings,omitempty"`
}

func newRepoNewCmd(app *App) *cobra.Command {
	flags := repoBootstrapFlags{private: true, push: true, forge: "auto"}
	cmd := &cobra.Command{
		Use:     "new [name|clone-ref]",
		Aliases: []string{"create"},
		Short:   "Create a new repository or clone an explicit source",
		Long: `Create a repository under paths.project_root from any directory.

With no name in an interactive terminal, a wizard chooses the destination,
scaffold preset, agent setup, optional GitHub/GitLab upstream and final
handoff. An explicit name keeps the original script-friendly minimal behavior
unless --preset or other bootstrap flags are supplied. A clear Git URL, path,
or owner/name reference is acquired as a clone instead of a fresh history.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if flags.json || !app.interactive() {
					return errors.New("repository name is required outside an interactive terminal")
				}
				request, confirmed, err := runRepoNewWizard(app, flags)
				if errors.Is(err, errPromptCanceled) || !confirmed {
					fmt.Fprintln(app.Out, "Canceled; nothing was created.")
					return nil
				}
				if err != nil {
					return err
				}
				return executeRepoWorkflow(app, request)
			}
			if looksLikeCloneReference(args[0]) {
				if err := validateNewAsCloneFlags(flags); err != nil {
					return err
				}
				request, err := buildCloneRepoRequest(app, args[0], flags)
				if err != nil {
					return err
				}
				return executeRepoWorkflow(app, request)
			}
			request, err := buildNewRepoRequest(app, args[0], flags)
			if err != nil {
				return err
			}
			return executeRepoWorkflow(app, request)
		},
	}
	bindRepoBootstrapFlags(cmd, &flags, true, true, false)
	cmd.Flags().StringVar(&flags.template, "template", "", "local directory, Git URL, or owner/repo used as a snapshot template")
	cmd.Flags().StringVar(&flags.templateRef, "template-ref", "", "Git branch, tag, or commit to snapshot from --template")
	cmd.Flags().StringVar(&flags.templateSubdir, "template-subdir", "", "relative directory within --template to use as the repository root")
	registerFlagCompletion(cmd, "handoff", fixedCompletions(repoHandoffCompletions()...))
	registerFlagCompletion(cmd, "check-in", fixedCompletions(repoCheckInCompletions()...))
	registerFlagCompletion(cmd, "forge", fixedCompletions("auto", "github", "gitlab", "none"))
	registerFlagCompletion(cmd, "visibility", fixedCompletions("private", "public", "internal"))
	return cmd
}

func validateNewAsCloneFlags(flags repoBootstrapFlags) error {
	if flags.template != "" || flags.templateRef != "" || flags.templateSubdir != "" {
		return errors.New("--template flags create a fresh history and cannot be combined with a clone reference")
	}
	if flags.remote || flags.forge != "auto" || flags.namespace != "" || flags.visibility != "" ||
		flags.public || !flags.private || !flags.push {
		return errors.New("upstream-creation flags cannot be combined with a clone reference; the cloned remote is preserved")
	}
	return nil
}

func newRepoCloneCmd(app *App) *cobra.Command {
	flags := repoBootstrapFlags{forge: "auto"}
	cmd := &cobra.Command{
		Use:   "clone [owner/name|url|path]",
		Short: "Clone a repository and optionally apply a setup preset",
		Long: `Clone into paths.project_root (or --path). In a terminal, omitting the
reference opens a cache-backed repository picker, retains manual source entry,
and offers an idempotent scaffold after the clone. Opening the picker never
refreshes forge providers. Setup is never selected by default for an existing
team or third-party repo.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if flags.json || !app.interactive() {
					return errors.New("clone reference is required outside an interactive terminal")
				}
				request, setup, confirmed, err := runRepoCloneWizard(app, flags)
				if errors.Is(err, errPromptCanceled) {
					fmt.Fprintln(app.Out, "Canceled; nothing was cloned.")
					return nil
				}
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintln(app.Out, "Canceled; nothing was cloned.")
					return nil
				}
				if !setup || request.Scaffold.Preset != "" || request.DryRun {
					return executeRepoWorkflow(app, request)
				}
				if err := executeRepoCloneAcquire(app, &request); err != nil {
					return err
				}
				flags.handoff = string(request.Handoff)
				setupRequest, ok, err := runRepoSetupWizard(app, request.Destination, flags)
				if errors.Is(err, errPromptCanceled) {
					fmt.Fprintln(app.Out, "Clone is ready; setup was canceled.")
					return finishRepoClone(app, request)
				}
				if err != nil {
					return fmt.Errorf("clone is ready at %s but setup could not be prepared: %w", config.Contract(request.Destination), err)
				}
				if ok {
					setupRequest.Kind = repo.AcquireClone
					setupRequest.Ref = request.Ref
					if err := executeRepoSetup(app, setupRequest); err != nil {
						return fmt.Errorf("clone is ready at %s but setup failed: %w", config.Contract(request.Destination), err)
					}
					return nil
				}
				return finishRepoClone(app, request)
			}
			request, err := buildCloneRepoRequest(app, args[0], flags)
			if err != nil {
				return err
			}
			return executeRepoWorkflow(app, request)
		},
	}
	bindRepoBootstrapFlags(cmd, &flags, true, false, false)
	cmd.Flags().BoolVarP(&flags.open, "open", "o", false, "open the clone in the runtime afterwards (alias for --handoff=open)")
	registerFlagCompletion(cmd, "handoff", fixedCompletions(repoHandoffCompletions()...))
	registerFlagCompletion(cmd, "check-in", fixedCompletions(repoCheckInCompletions()...))
	return cmd
}

func newRepoSetupCmd(app *App) *cobra.Command {
	flags := repoBootstrapFlags{private: true, push: true, forge: "auto"}
	cmd := &cobra.Command{
		Use:   "setup [repo-or-path]",
		Short: "Apply a scaffold to an existing repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveSetupRepo(app, args)
			if err != nil {
				return err
			}
			if flags.preset == "" && app.interactive() && !flags.json {
				request, confirmed, err := runRepoSetupWizard(app, root, flags)
				if errors.Is(err, errPromptCanceled) || !confirmed {
					fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
					return nil
				}
				if err != nil {
					return err
				}
				return executeRepoSetup(app, request)
			}
			if flags.preset == "" {
				return errors.New("--preset is required for non-interactive repo setup")
			}
			request, err := buildSetupRepoRequest(app, root, flags)
			if err != nil {
				return err
			}
			return executeRepoSetup(app, request)
		},
	}
	bindRepoBootstrapFlags(cmd, &flags, false, true, true)
	registerFlagCompletion(cmd, "handoff", fixedCompletions(repoHandoffCompletions()...))
	registerFlagCompletion(cmd, "check-in", fixedCompletions(repoCheckInCompletions()...))
	registerFlagCompletion(cmd, "forge", fixedCompletions("auto", "github", "gitlab", "none"))
	registerFlagCompletion(cmd, "visibility", fixedCompletions("private", "public", "internal"))
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func bindRepoBootstrapFlags(cmd *cobra.Command, flags *repoBootstrapFlags, acquisition, remote, setup bool) {
	f := cmd.Flags()
	if acquisition {
		f.StringVarP(&flags.category, "category", "c", "", "category subdirectory under project_root")
		f.StringVar(&flags.path, "path", "", "exact destination path")
	}
	f.StringVar(&flags.preset, "preset", "", "scaffold preset")
	f.StringVar(&flags.handoff, "handoff", "", "afterwards: stay, cd, open or start")
	f.StringVar(&flags.checkIn, "check-in", "", "finish generated changes: auto, commit, stage or none")
	f.StringVar(&flags.description, "description", "", "repository description")
	f.StringSliceVar(&flags.gitignore, "gitignore", nil, "gitignore template (repeatable or comma-separated)")
	f.StringVar(&flags.license, "license", "", "license keyword (for example mit or apache-2.0)")
	f.StringVar(&flags.licenseHolder, "license-holder", "", "copyright holder used in the license template")
	f.StringArrayVar(&flags.set, "set", nil, "preset input as key=value (repeatable)")
	f.StringArrayVar(&flags.enable, "enable", nil, "enable a scaffold item by id (repeatable)")
	f.StringArrayVar(&flags.disable, "disable", nil, "disable a scaffold item by id (repeatable)")
	f.StringSliceVar(&flags.agents, "agent", nil, "agent targets for selected project skills")
	f.BoolVar(&flags.browseSkills, "browse-skills", false, "open the upstream skills installer during setup")
	f.BoolVar(&flags.importOrphans, "import-orphan-plans", false, "copy matching global Claude plans into the repository")
	f.BoolVar(&flags.dryRun, "dry-run", false, "show the plan without changing anything")
	f.BoolVarP(&flags.yes, "yes", "y", false, "confirm the non-interactive scaffold plan")
	f.BoolVar(&flags.json, "json", false, "emit a machine-readable result")
	messageDefault := ""
	if setup {
		messageDefault = "chore: initialize repository"
	}
	f.StringVarP(&flags.message, "message", "m", messageDefault, "commit message for --check-in=commit or stage")
	if remote {
		f.BoolVar(&flags.remote, "remote", false, "also create a GitHub or GitLab upstream")
		f.StringVar(&flags.forge, "forge", "auto", "upstream provider: auto, github, gitlab or none")
		f.StringVar(&flags.namespace, "namespace", "", "GitHub owner/org or GitLab namespace")
		f.StringVar(&flags.visibility, "visibility", "", "upstream visibility: private, public or internal")
		f.BoolVar(&flags.private, "private", true, "create a private upstream (compatibility flag)")
		f.BoolVar(&flags.public, "public", false, "create a public upstream")
		f.BoolVar(&flags.push, "push", true, "push the current branch after publishing")
	}
	if setup {
		f.BoolVar(&flags.commit, "commit", false, "commit only the setup changes (requires a clean starting checkout)")
	}
}

func buildNewRepoRequest(app *App, name string, flags repoBootstrapFlags) (repoWorkflowRequest, error) {
	destination, err := repoDestination(app.Cfg.Paths.ProjectRoot, flags.category, name, flags.path)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	inputs, selections, err := parseScaffoldOverrides(flags)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	preset := flags.preset
	if preset == "" {
		preset = "minimal"
	}
	prepared, err := prepareRepoScaffold(app, repoScaffoldRequest{
		Root: destination, Name: name, Description: flags.description, Category: flags.category,
		Preset: preset, Inputs: inputs, Selections: selections, Gitignore: explicitSlice(flags.gitignore),
		License: flags.license, LicenseHolder: flags.licenseHolder, ImportOrphans: flags.importOrphans,
		SkillAgents: explicitSlice(flags.agents),
	})
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	templateSource := prepared.Plan.Settings.Template
	templateRef := prepared.Plan.Settings.TemplateRef
	templateSubdir := prepared.Plan.Settings.TemplateSubdir
	if flags.template != "" {
		if strings.EqualFold(strings.TrimSpace(flags.template), "none") {
			templateSource, templateRef, templateSubdir = "", "", ""
		} else {
			templateSource = flags.template
		}
	}
	if flags.templateRef != "" {
		templateRef = flags.templateRef
	}
	if flags.templateSubdir != "" {
		templateSubdir = flags.templateSubdir
	}
	if strings.TrimSpace(templateSource) == "" && (strings.TrimSpace(templateRef) != "" || strings.TrimSpace(templateSubdir) != "") {
		return repoWorkflowRequest{}, errors.New("--template-ref and --template-subdir require --template or a preset template")
	}
	// The raw preset source may contain URL credentials. It is no longer needed
	// by the scaffold pipeline once the effective template selection is copied
	// above, so keep every rendered/JSON plan display-safe.
	prepared.Plan.Settings.Template = ""
	prepared.Plan.Settings.TemplateRef = ""
	prepared.Plan.Settings.TemplateSubdir = ""
	checkIn, err := resolveRepoCheckIn(flags, prepared, repo.AcquireNew)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	handoff, err := flagsHandoff(flags, repoHandoffStay)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	publish, err := publishRequestFromFlags(flags, name)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	request := repoWorkflowRequest{
		Kind: repo.AcquireNew, Name: name, Destination: destination,
		Scaffold: repoScaffoldRequest{Root: destination, Name: name}, Prepared: prepared,
		Handoff: handoff, Publish: publish, BrowseSkills: flags.browseSkills,
		CheckIn: checkIn, CommitMessage: flags.message, DryRun: flags.dryRun, JSON: flags.json,
	}
	if err := validateRepoWorkflowRequest(app, request, flags.yes); err != nil {
		return repoWorkflowRequest{}, err
	}
	if strings.TrimSpace(templateSource) != "" {
		preparedTemplate, err := repotemplate.Prepare(ctxOf(), repotemplate.Request{
			Source: templateSource, Ref: templateRef, Subdir: templateSubdir,
		})
		if err != nil {
			return repoWorkflowRequest{}, err
		}
		request.Template = &preparedTemplate
		request.Prepared.Plan.Settings.Template = preparedTemplate.Source
		request.Prepared.Plan.Settings.TemplateRef = preparedTemplate.Ref
		request.Prepared.Plan.Settings.TemplateSubdir = preparedTemplate.Subdir
	}
	return request, nil
}

func buildCloneRepoRequest(app *App, ref string, flags repoBootstrapFlags) (repoWorkflowRequest, error) {
	safeRef := repo.RedactCloneRef(ref)
	name := repo.NameFromRef(safeRef)
	if name == "" {
		return repoWorkflowRequest{}, fmt.Errorf("could not derive a directory name from %q", safeRef)
	}
	destination, err := repoDestination(app.Cfg.Paths.ProjectRoot, flags.category, name, flags.path)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	handoff, err := flagsHandoff(flags, repoHandoffStay)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	checkIn, err := resolveRepoCheckIn(flags, preparedRepoScaffold{}, repo.AcquireClone)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	request := repoWorkflowRequest{
		Kind: repo.AcquireClone, Name: name, Ref: ref, Destination: destination,
		Handoff: handoff, BrowseSkills: flags.browseSkills, CheckIn: checkIn,
		CommitMessage: flags.message, DryRun: flags.dryRun, JSON: flags.json,
	}
	if flags.preset == "" && cloneSetupOptionsSelected(flags) {
		return request, errors.New("clone setup options require --preset")
	}
	if flags.preset != "" {
		if !app.interactive() && !flags.yes {
			return request, errors.New("non-interactive clone setup requires --yes")
		}
		inputs, selections, err := parseScaffoldOverrides(flags)
		if err != nil {
			return request, err
		}
		request.Scaffold = repoScaffoldRequest{
			Root: destination, Name: name, Description: flags.description, Category: flags.category,
			Preset: flags.preset, Inputs: inputs, Selections: selections,
			Gitignore: explicitSlice(flags.gitignore), License: flags.license,
			LicenseHolder: flags.licenseHolder, ImportOrphans: flags.importOrphans, Existing: true,
			SkillAgents: explicitSlice(flags.agents),
		}
		// A clone's project layer does not exist until acquisition. Prepare it in
		// executeRepoWorkflow after the clone succeeds.
	}
	return request, validateRepoWorkflowRequest(app, request, flags.yes)
}

func cloneSetupOptionsSelected(flags repoBootstrapFlags) bool {
	checkInNeedsSetup := flags.checkIn != "" &&
		!strings.EqualFold(flags.checkIn, string(repoCheckInAuto)) &&
		!strings.EqualFold(flags.checkIn, string(repoCheckInNone))
	return flags.description != "" || flags.gitignore != nil || flags.license != "" || flags.licenseHolder != "" ||
		len(flags.set) > 0 || len(flags.enable) > 0 || len(flags.disable) > 0 || flags.agents != nil ||
		flags.browseSkills || flags.importOrphans || checkInNeedsSetup || flags.message != ""
}

func buildSetupRepoRequest(app *App, root string, flags repoBootstrapFlags) (repoWorkflowRequest, error) {
	inputs, selections, err := parseScaffoldOverrides(flags)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	name := filepath.Base(root)
	prepared, err := prepareRepoScaffold(app, repoScaffoldRequest{
		Root: root, Name: name, Description: flags.description, Preset: flags.preset,
		Inputs: inputs, Selections: selections, Gitignore: explicitSlice(flags.gitignore),
		License: flags.license, LicenseHolder: flags.licenseHolder,
		ImportOrphans: flags.importOrphans, Existing: true, SkillAgents: explicitSlice(flags.agents),
	})
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	checkIn, err := resolveRepoCheckIn(flags, prepared, "")
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	handoff, err := flagsHandoff(flags, repoHandoffStay)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	publish, err := publishRequestFromFlags(flags, name)
	if err != nil {
		return repoWorkflowRequest{}, err
	}
	if publish != nil && checkIn != repoCheckInCommit {
		return repoWorkflowRequest{}, errors.New("publishing repository setup requires --check-in=commit (or --commit) so the upstream includes the setup changes")
	}
	request := repoWorkflowRequest{
		Name: name, Destination: root, Prepared: prepared, Handoff: handoff,
		Publish: publish, BrowseSkills: flags.browseSkills,
		CheckIn: checkIn, CommitMessage: flags.message,
		DryRun: flags.dryRun, JSON: flags.json,
	}
	return request, validateRepoWorkflowRequest(app, request, flags.yes)
}

func validateRepoWorkflowRequest(app *App, request repoWorkflowRequest, yes bool) error {
	if request.JSON && request.Handoff != repoHandoffStay {
		return errors.New("--json requires --handoff=stay")
	}
	if request.Handoff == repoHandoffStart && !app.interactive() {
		return errors.New("--handoff=start requires an interactive terminal")
	}
	if request.JSON && request.BrowseSkills {
		return errors.New("--json cannot open the interactive upstream skills installer")
	}
	if request.BrowseSkills && !app.interactive() {
		return errors.New("--browse-skills requires an interactive terminal")
	}
	if request.Kind == repo.AcquireNew && request.Publish != nil && request.Publish.Push {
		if request.CheckIn != repoCheckInCommit {
			return errors.New("pushing a new upstream requires --check-in=commit; use --push=false to create an empty upstream")
		}
	}
	if request.Publish != nil && request.CheckIn == repoCheckInStage {
		return errors.New("--check-in=stage cannot create an upstream; review and commit locally before publishing")
	}
	if request.Handoff == repoHandoffStart && request.CheckIn != repoCheckInCommit &&
		(request.Kind == repo.AcquireNew || request.Scaffold.Preset != "" || request.Prepared.Plan.Root != "") {
		return errors.New("--handoff=start requires --check-in=commit when repository setup changes files")
	}
	if request.Kind != repo.AcquireNew && request.Publish != nil {
		remoteName := strings.TrimSpace(request.Publish.RemoteName)
		if remoteName == "" {
			remoteName = "origin"
		}
		if current := gitx.Remote(ctxOf(), request.Destination, remoteName); current != "" {
			return fmt.Errorf("remote %s already points to %s; omit --remote or remove it explicitly before publishing", remoteName, current)
		}
	}
	if !app.interactive() && !yes && (len(request.Prepared.Plan.Hooks) > 0 || len(request.Prepared.Plan.Skills) > 0 || request.BrowseSkills) {
		return errors.New("non-interactive hooks or skill setup require --yes")
	}
	for _, skill := range request.Prepared.Plan.Skills {
		if _, err := agentskill.InstallCommand(ctxOf(), request.Destination, skill.Source, []string{skill.Name}, skill.Agents); err != nil {
			return fmt.Errorf("skill %s cannot be installed: %w", skill.Name, err)
		}
		if skill.Setup == nil || skill.Setup.Builtin != "" || skill.Setup.Interpreter == "" {
			continue
		}
		if _, err := exec.LookPath(skill.Setup.Interpreter); err != nil && skill.Setup.Required {
			return fmt.Errorf("skill %s setup requires unavailable interpreter %q", skill.Name, skill.Setup.Interpreter)
		}
	}
	for _, hook := range request.Prepared.Plan.Hooks {
		if runtime.GOOS == "windows" && strings.TrimSpace(hook.Run) != "" {
			return fmt.Errorf("hook %s uses a POSIX shell expression, which is unsupported on Windows; use command = [...]", hook.ID)
		}
		if len(hook.Command) > 0 && hook.Required && !isRepoLocalExecutable(request.Destination, hook.Command[0]) {
			if _, err := exec.LookPath(hook.Command[0]); err != nil {
				return fmt.Errorf("hook %s requires unavailable executable %q", hook.ID, hook.Command[0])
			}
		}
	}
	return nil
}

func isRepoLocalExecutable(root, executable string) bool {
	if !filepath.IsAbs(executable) {
		return strings.ContainsAny(executable, `/\\`)
	}
	relative, err := filepath.Rel(root, executable)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func executeRepoWorkflow(app *App, request repoWorkflowRequest) error {
	if request.Kind == repo.AcquireClone && request.DryRun {
		return renderCloneDryRun(app, request)
	}
	if request.Kind == repo.AcquireNew && request.DryRun {
		return renderRepoDryRun(app, request)
	}
	if request.Kind == repo.AcquireClone {
		if err := executeRepoCloneAcquire(app, &request); err != nil {
			return err
		}
		if request.Scaffold.Preset != "" {
			prepared, err := prepareRepoScaffold(app, request.Scaffold)
			if err != nil {
				return fmt.Errorf("clone is ready at %s but setup could not be prepared: %w", config.Contract(request.Destination), err)
			}
			request.Prepared = prepared
			if err := validateRepoWorkflowRequest(app, request, true); err != nil {
				return fmt.Errorf("clone is ready at %s but setup cannot start: %w", config.Contract(request.Destination), err)
			}
			if err := executeRepoSetup(app, request); err != nil {
				return fmt.Errorf("clone is ready at %s but setup failed: %w", config.Contract(request.Destination), err)
			}
			return nil
		}
		return finishRepoClone(app, request)
	}

	acquired, err := repo.Acquire(ctxOf(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: request.Name, Destination: request.Destination,
		InitialBranch: request.Prepared.Plan.Settings.InitialBranch,
	})
	if err != nil {
		return err
	}
	request.Destination = acquired.Path
	result := repoWorkflowResult{
		Operation: "new", Path: acquired.Path, Preset: request.Prepared.Plan.Preset,
		Created: true, Handoff: request.Handoff,
	}
	if request.Template != nil {
		applied, err := request.Template.Apply(acquired.Path)
		if err != nil {
			return fmt.Errorf("repository remains at %s: apply template: %w", config.Contract(acquired.Path), err)
		}
		result.Template = &applied
	}
	if err := executeScaffoldPipeline(app, request, &result); err != nil {
		return fmt.Errorf("repository remains at %s: %w", config.Contract(acquired.Path), err)
	}
	return finishRepoWorkflow(app, request, result)
}

func executeRepoCloneAcquire(app *App, request *repoWorkflowRequest) error {
	if request.DryRun {
		return renderCloneDryRun(app, *request)
	}
	acquired, err := repo.Acquire(ctxOf(), repo.AcquireRequest{
		Kind: repo.AcquireClone, Name: request.Name, CloneRef: request.Ref, Destination: request.Destination,
	})
	if err != nil {
		return err
	}
	request.Destination = acquired.Path
	request.Name = acquired.Name
	return nil
}

func finishRepoClone(app *App, request repoWorkflowRequest) error {
	result := repoWorkflowResult{
		Operation: "clone", Path: request.Destination, Created: true, Cloned: true, Handoff: request.Handoff,
	}
	return finishRepoWorkflow(app, request, result)
}

func executeRepoSetup(app *App, request repoWorkflowRequest) error {
	if request.Prepared.Plan.Root == "" {
		prepared, err := prepareRepoScaffold(app, request.Scaffold)
		if err != nil {
			return err
		}
		request.Prepared = prepared
	}
	if request.DryRun {
		return renderRepoDryRun(app, request)
	}
	status, err := gitx.StatusOf(ctxOf(), request.Destination)
	if err != nil {
		return err
	}
	if status.Dirty() {
		return errors.New("repo setup requires a clean checkout so its changes remain attributable")
	}
	operation := "setup"
	created, cloned := false, false
	if request.Kind == repo.AcquireClone {
		operation, created, cloned = "clone", true, true
	}
	result := repoWorkflowResult{
		Operation: operation, Path: request.Destination, Preset: request.Prepared.Plan.Preset,
		Created: created, Cloned: cloned, Handoff: request.Handoff,
	}
	if err := executeScaffoldPipeline(app, request, &result); err != nil {
		return err
	}
	return finishRepoWorkflow(app, request, result)
}

func executeScaffoldPipeline(app *App, request repoWorkflowRequest, result *repoWorkflowResult) error {
	trustApp := app
	if request.JSON {
		copy := *app
		copy.Out = app.Err
		copy.interactiveCheck = func() bool { return false }
		trustApp = &copy
	}
	if err := ensureProjectConfigTrust(trustApp, request.Destination, request.Prepared.Project, request.Prepared.Plan); err != nil {
		return err
	}
	applied, err := applyPreparedRepoScaffold(request.Prepared, request.Kind != repo.AcquireNew || request.Template != nil)
	if err != nil {
		return err
	}
	result.Scaffold = applied
	result.Warnings = append(result.Warnings, applied.Native.Warnings...)
	for _, warning := range applied.Native.Warnings {
		app.warnf("%s", warning)
	}

	workingApp := app
	if request.JSON {
		copy := *app
		copy.Out = app.Err
		workingApp = &copy
	}
	skills := repoSkillsFromPlan(request.Prepared.Plan)
	installed, err := installRepoSkills(ctxOf(), workingApp, request.Destination, skills)
	if err != nil {
		return err
	}
	result.Skills.Installed = append(result.Skills.Installed, installed.Installed...)
	result.Warnings = append(result.Warnings, installed.Warnings...)
	if request.BrowseSkills {
		source := agentskill.DefaultSource
		if len(request.Prepared.Plan.Catalog) > 0 && request.Prepared.Plan.Catalog[0].Source != "" {
			source = request.Prepared.Plan.Catalog[0].Source
		}
		if err := runUpstreamSkillCatalog(ctxOf(), workingApp, request.Destination, source); err != nil {
			return err
		}
	}
	hooks := repoHooksFromPlan(request.Prepared.Plan)
	beforeSkill, err := runRepoSkillSetups(ctxOf(), workingApp, request.Destination, repoHookBeforeCommit, skills)
	if err != nil {
		return err
	}
	result.Skills.Setup = append(result.Skills.Setup, beforeSkill.Setup...)
	result.Warnings = append(result.Warnings, beforeSkill.Warnings...)
	beforeHooks, err := runRepoHooks(ctxOf(), workingApp, request.Destination, repoHookBeforeCommit, hooks)
	result.Hooks = appendRepoHookResult(result.Hooks, beforeHooks)
	result.Warnings = append(result.Warnings, beforeHooks.Warnings...)
	if err != nil {
		return err
	}

	checkIn := request.CheckIn
	if checkIn == repoCheckInAuto || checkIn == "" {
		checkIn = repoCheckInNone
	}
	if checkIn != repoCheckInNone {
		if request.Kind != repo.AcquireNew {
			status, statusErr := gitx.StatusOf(ctxOf(), request.Destination)
			if statusErr != nil {
				return statusErr
			}
			if !status.Dirty() {
				checkIn = repoCheckInNone
			}
		}
	}
	if checkIn == repoCheckInStage {
		message := repoCommitMessage(request)
		staged, provider, warning, err := stageRepoForReview(ctxOf(), request.Destination, message)
		if err != nil {
			return err
		}
		result.Staged = staged > 0
		result.StagedPaths = staged
		result.CommitMessage = message
		result.CommitDraftProvider = provider
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
			app.warnf("%s", warning)
		}
	}
	if checkIn == repoCheckInCommit {
		message := repoCommitMessage(request)
		if _, err := gitx.Run(ctxOf(), request.Destination, "add", "-A"); err != nil {
			return err
		}
		commitArgs := []string{"commit", "-m", message}
		if request.Kind == repo.AcquireNew {
			commitArgs = append(commitArgs, "--allow-empty")
		}
		if _, err := gitx.Run(ctxOf(), request.Destination, commitArgs...); err != nil {
			return err
		}
		result.Commit = true
		result.CommitMessage = message
		afterSkill, err := runRepoSkillSetups(ctxOf(), workingApp, request.Destination, repoHookAfterCommit, skills)
		if err != nil {
			return err
		}
		result.Skills.Setup = append(result.Skills.Setup, afterSkill.Setup...)
		result.Warnings = append(result.Warnings, afterSkill.Warnings...)
		afterHooks, err := runRepoHooks(ctxOf(), workingApp, request.Destination, repoHookAfterCommit, hooks)
		result.Hooks = appendRepoHookResult(result.Hooks, afterHooks)
		result.Warnings = append(result.Warnings, afterHooks.Warnings...)
		if err != nil {
			return err
		}
	}
	if request.Publish != nil {
		published, err := publishRepository(ctxOf(), request.Destination, *request.Publish)
		result.Remote = &published
		if err != nil {
			detail := ""
			if published.Remote.URL != "" {
				detail = "; upstream may already exist at " + published.Remote.URL
			}
			return fmt.Errorf("local repository is ready but upstream publication failed%s: %w", detail, err)
		}
		afterRemoteSkill, err := runRepoSkillSetups(ctxOf(), workingApp, request.Destination, repoHookAfterRemote, skills)
		if err != nil {
			return err
		}
		result.Skills.Setup = append(result.Skills.Setup, afterRemoteSkill.Setup...)
		result.Warnings = append(result.Warnings, afterRemoteSkill.Warnings...)
		afterRemoteHooks, err := runRepoHooks(ctxOf(), workingApp, request.Destination, repoHookAfterRemote, hooks)
		result.Hooks = appendRepoHookResult(result.Hooks, afterRemoteHooks)
		result.Warnings = append(result.Warnings, afterRemoteHooks.Warnings...)
		if err != nil {
			return err
		}
	}
	return nil
}

func appendRepoHookResult(results []repoHookResult, next repoHookResult) []repoHookResult {
	if len(next.Ran) == 0 && len(next.Warnings) == 0 {
		return results
	}
	return append(results, next)
}

func finishRepoWorkflow(app *App, request repoWorkflowRequest, result repoWorkflowResult) error {
	if request.JSON {
		encoder := json.NewEncoder(app.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	verb := result.Operation
	if verb == "new" {
		verb = "created"
	} else if verb == "clone" {
		verb = "cloned"
	} else {
		verb = "set up"
	}
	fmt.Fprintf(app.Out, "%s %s\n", verb, config.Contract(result.Path))
	if result.Preset != "" {
		fmt.Fprintf(app.Out, "  preset  %s\n", result.Preset)
	}
	if result.Remote != nil && result.Remote.Remote.URL != "" {
		fmt.Fprintf(app.Out, "  remote  %s\n", result.Remote.Remote.URL)
	}
	if result.Template != nil {
		fmt.Fprintf(app.Out, "  template %s (%d files)\n", result.Template.Source, result.Template.AppliedFiles)
	}
	if result.Commit {
		fmt.Fprintf(app.Out, "  commit  %s\n", result.CommitMessage)
	} else if result.Staged {
		fmt.Fprintf(app.Out, "  staged  %d path(s) for review\n", result.StagedPaths)
		if result.CommitDraftProvider == "lazygit" {
			fmt.Fprintf(app.Out, "  message %s (prefilled for lazygit c)\n", result.CommitMessage)
		} else {
			fmt.Fprintf(app.Out, "  message %s\n", result.CommitMessage)
		}
	}
	warnOutsideDiscovery(app, result.Path)
	if result.Handoff == repoHandoffStart {
		status, err := gitx.StatusOf(ctxOf(), result.Path)
		if err == nil && status.Dirty() {
			return errors.New("cannot start an isolated task while setup changes are uncommitted; commit them or choose --handoff=cd")
		}
	}
	return performRepoHandoff(app, result.Path, filepath.Base(result.Path), result.Handoff)
}

func renderRepoDryRun(app *App, request repoWorkflowRequest) error {
	operation := string(request.Kind)
	if operation == "" {
		operation = "setup"
	}
	checkIn, commitMessage := plannedRepoCheckIn(request)
	template := repoTemplateSummary(request.Template)
	if request.JSON {
		return json.NewEncoder(app.Out).Encode(struct {
			Operation     string                `json:"operation"`
			DryRun        bool                  `json:"dry_run"`
			Path          string                `json:"path"`
			Plan          scaffold.Plan         `json:"plan"`
			CheckIn       repoCheckIn           `json:"check_in"`
			Commit        bool                  `json:"commit"`
			Stage         bool                  `json:"stage"`
			CommitMessage string                `json:"commit_message,omitempty"`
			Publish       *repoPublishRequest   `json:"publish,omitempty"`
			Template      *repotemplate.Summary `json:"template,omitempty"`
			Handoff       repoHandoff           `json:"handoff"`
		}{operation, true, request.Destination, request.Prepared.Plan, checkIn, checkIn == repoCheckInCommit,
			checkIn == repoCheckInStage, commitMessage, request.Publish, template, request.Handoff})
	}
	fmt.Fprintln(app.Out, app.outStyle().title("Dry run — nothing will be changed"))
	if template != nil {
		fmt.Fprintln(app.Out, app.outStyle().title("Template snapshot"))
		fmt.Fprintf(app.Out, "  source     %s\n", template.Source)
		if template.Ref != "" {
			fmt.Fprintf(app.Out, "  ref        %s\n", template.Ref)
		}
		if template.Subdir != "" {
			fmt.Fprintf(app.Out, "  subdir     %s\n", template.Subdir)
		}
		fmt.Fprintf(app.Out, "  contents   %d files, %d directories\n", template.Files, template.Directories)
		renderRepoTemplateFilePreview(app, request.Template)
	}
	renderPreparedRepoScaffold(app, request.Prepared)
	switch checkIn {
	case repoCheckInCommit:
		fmt.Fprintf(app.Out, "  check-in   commit (%s)\n", commitMessage)
	case repoCheckInStage:
		fmt.Fprintf(app.Out, "  check-in   stage for review (%s)\n", commitMessage)
	default:
		fmt.Fprintln(app.Out, "  check-in   none")
	}
	if request.Publish != nil {
		fmt.Fprintf(app.Out, "  upstream   %s:%s/%s (%s)\n", request.Publish.Forge,
			request.Publish.Namespace, request.Publish.Name, request.Publish.Visibility)
	} else {
		fmt.Fprintln(app.Out, "  upstream   local only")
	}
	fmt.Fprintf(app.Out, "  handoff    %s\n", request.Handoff)
	return nil
}

func repoTemplateSummary(snapshot *repotemplate.Snapshot) *repotemplate.Summary {
	if snapshot == nil {
		return nil
	}
	summary := snapshot.Summary()
	return &summary
}

func renderRepoTemplateFilePreview(app *App, snapshot *repotemplate.Snapshot) {
	if snapshot == nil {
		return
	}
	summary := snapshot.Summary()
	paths := make([]string, 0, len(summary.PathPreview))
	for _, path := range summary.PathPreview {
		paths = append(paths, strconv.Quote(path))
	}
	if len(paths) > 0 {
		suffix := ""
		if summary.PathPreviewTruncated {
			suffix = " (more omitted)"
		}
		fmt.Fprintf(app.Out, "  paths      %s%s\n", strings.Join(paths, ", "), suffix)
	}
	if summary.Live {
		fmt.Fprintln(app.Out, "  "+app.outStyle().warning("local template uses current files rather than a commit; review them before check-in or publishing"))
	}
}

func renderCloneDryRun(app *App, request repoWorkflowRequest) error {
	if request.JSON {
		return json.NewEncoder(app.Out).Encode(map[string]any{
			"operation": "clone", "dry_run": true, "source": repo.RedactCloneRef(request.Ref),
			"path": request.Destination, "preset": request.Scaffold.Preset,
			"check_in": request.CheckIn, "commit": request.CheckIn == repoCheckInCommit,
			"stage": request.CheckIn == repoCheckInStage, "publish": nil, "handoff": request.Handoff,
		})
	}
	fmt.Fprintln(app.Out, app.outStyle().title("Dry run — nothing will be cloned"))
	fmt.Fprintf(app.Out, "  source      %s\n", repo.RedactCloneRef(request.Ref))
	fmt.Fprintf(app.Out, "  destination %s\n", config.Contract(request.Destination))
	if request.Scaffold.Preset != "" {
		fmt.Fprintf(app.Out, "  setup       %s (planned after clone)\n", request.Scaffold.Preset)
	}
	fmt.Fprintf(app.Out, "  check-in    %s\n", request.CheckIn)
	fmt.Fprintln(app.Out, "  upstream    existing clone remote")
	fmt.Fprintf(app.Out, "  handoff     %s\n", request.Handoff)
	return nil
}

func plannedRepoCheckIn(request repoWorkflowRequest) (repoCheckIn, string) {
	mode := request.CheckIn
	if mode == "" || mode == repoCheckInAuto {
		mode = repoCheckInNone
	}
	if mode == repoCheckInNone {
		return mode, ""
	}
	return mode, repoCommitMessage(request)
}

func repoDestination(projectRoot, category, name, exact string) (string, error) {
	if err := pathx.ValidateComponent(name); err != nil {
		return "", fmt.Errorf("repository name %q: %w", name, err)
	}
	if exact != "" {
		return pathx.Canonical(config.Expand(exact))
	}
	root := config.Expand(projectRoot)
	components, err := categoryComponents(category)
	if err != nil {
		return "", err
	}
	components = append(components, name)
	return pathx.JoinChild(root, components...)
}

func categoryComponents(category string) ([]string, error) {
	category = strings.TrimSpace(category)
	if filepath.IsAbs(category) || filepath.VolumeName(category) != "" {
		return nil, fmt.Errorf("category %q must be relative to project_root", category)
	}
	var result []string
	for _, component := range strings.FieldsFunc(category, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component != "" {
			result = append(result, component)
		}
	}
	return result, nil
}

func parseScaffoldOverrides(flags repoBootstrapFlags) (map[string]any, map[string]bool, error) {
	inputs, err := parseSetValues(flags.set)
	if err != nil {
		return nil, nil, err
	}
	selections, err := parseSelectionValues(flags.enable, flags.disable)
	return inputs, selections, err
}

func explicitSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func flagsHandoff(flags repoBootstrapFlags, fallback repoHandoff) (repoHandoff, error) {
	value := flags.handoff
	if flags.open {
		if value != "" && value != string(repoHandoffOpen) {
			return "", errors.New("--open conflicts with --handoff")
		}
		value = string(repoHandoffOpen)
	}
	if value == "" {
		return fallback, nil
	}
	return parseRepoHandoff(value)
}

func publishRequestFromFlags(flags repoBootstrapFlags, name string) (*repoPublishRequest, error) {
	if !flags.remote && flags.forge == "auto" {
		return nil, nil
	}
	if flags.forge == "none" {
		return nil, nil
	}
	kind, err := chooseForge(flags.forge)
	if err != nil {
		return nil, err
	}
	readiness := forge.Probe(ctxOf(), kind)
	if !readiness.Ready() {
		return nil, fmt.Errorf("%s is not ready: %s; %s", kind, readiness.Detail, readiness.Action)
	}
	visibility := forge.VisibilityPrivate
	if flags.public && flags.visibility != "" && flags.visibility != string(forge.VisibilityPublic) {
		return nil, errors.New("--public conflicts with --visibility")
	}
	if flags.public {
		visibility = forge.VisibilityPublic
	} else if flags.visibility != "" {
		visibility = forge.Visibility(flags.visibility)
	} else if !flags.private {
		visibility = forge.VisibilityPublic
	}
	switch visibility {
	case forge.VisibilityPrivate, forge.VisibilityPublic, forge.VisibilityInternal:
	default:
		return nil, fmt.Errorf("visibility %q: want private, public or internal", visibility)
	}
	return &repoPublishRequest{
		Forge: kind, Name: name, Namespace: flags.namespace,
		Description: flags.description, Visibility: visibility, Push: flags.push,
	}, nil
}

func chooseForge(name string) (forge.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "github":
		return forge.GitHub, nil
	case "gitlab":
		return forge.GitLab, nil
	case "", "auto":
		for _, readiness := range forge.ProbeAll(ctxOf()) {
			if readiness.Ready() {
				return readiness.Forge, nil
			}
		}
		return forge.Unknown, errors.New("neither GitHub nor GitLab CLI is installed and authenticated")
	default:
		return forge.Unknown, fmt.Errorf("forge %q: want auto, github, gitlab or none", name)
	}
}

func looksLikeCloneReference(value string) bool {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") || strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "file://") {
		return true
	}
	if strings.ContainsAny(value, `/\\`) {
		return true
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") {
		return true
	}
	return false
}

func resolveSetupRepo(app *App, args []string) (string, error) {
	if len(args) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		discovered, err := gitx.Discover(ctxOf(), cwd)
		if err != nil {
			return "", errors.New("current directory is not a repository; pass a repo or path")
		}
		return discovered.Root, nil
	}
	ref := args[0]
	if looksLikeRepoPath(ref) {
		discovered, err := gitx.Discover(ctxOf(), config.Expand(ref))
		if err == nil {
			return discovered.Root, nil
		}
	}
	resolved, _, err := resolveRepoRef(app, ref)
	if err != nil {
		return "", err
	}
	return resolved.Path, nil
}

func looksLikeRepoPath(value string) bool {
	return filepath.IsAbs(value) || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") ||
		strings.ContainsAny(value, `/\\`)
}

func warnOutsideDiscovery(app *App, path string) {
	for _, root := range app.Cfg.ScanRoots() {
		if contains, _ := pathx.Contains(root, path); contains {
			return
		}
	}
	for _, exact := range app.Cfg.RepoPaths() {
		left, _ := pathx.Canonical(exact)
		right, _ := pathx.Canonical(path)
		if left != "" && left == right {
			return
		}
	}
	app.warnf("%s is outside paths.scan_roots/repo_paths and will not appear in dev repo list", config.Contract(path))
}

func parseInputDefault(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}
