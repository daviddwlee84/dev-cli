package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type graduateFlags struct {
	name, category, remoteURL   string
	yes, dryRun                 bool
	pushSet, privateSet, urlSet bool
	upstream                    repoBootstrapFlags
}

func defaultGraduateFlags() graduateFlags {
	return graduateFlags{upstream: repoBootstrapFlags{forge: "auto", private: true, push: true}}
}

type graduateUpstream struct {
	action string
	url    string
	create repoPublishRequest
	push   bool
}

// Parse the requested upstream without executing a provider. This boundary is
// also used by dry-run, and rejects conflicting options before local changes.
func (f graduateFlags) upstreamRequest() (graduateUpstream, error) {
	hint := strings.ToLower(strings.TrimSpace(f.upstream.forge))
	if hint == "" {
		hint = "auto"
	}
	switch hint {
	case "auto", "github", "gitlab", "none":
	default:
		return graduateUpstream{}, errors.New("--forge must be auto, github, gitlab or none")
	}
	action := "local"
	if f.upstream.remote || hint == "github" || hint == "gitlab" {
		action = "create"
	}
	if f.upstream.remote && hint == "none" {
		return graduateUpstream{}, errors.New("--remote conflicts with --forge=none")
	}
	if f.urlSet && strings.TrimSpace(f.remoteURL) == "" {
		return graduateUpstream{}, errors.New("--remote-url requires a non-empty URL")
	}
	if f.remoteURL != "" {
		if action == "create" || hint == "none" || f.upstream.namespace != "" || f.upstream.visibility != "" || f.privateSet {
			return graduateUpstream{}, errors.New("--remote-url cannot be combined with upstream creation options")
		}
		if err := validateGraduateRemoteURL(f.remoteURL); err != nil {
			return graduateUpstream{}, err
		}
		return graduateUpstream{action: "add", url: f.remoteURL, push: f.pushSet && f.upstream.push}, nil
	}
	if action != "create" {
		if f.upstream.namespace != "" || f.upstream.visibility != "" {
			return graduateUpstream{}, errors.New("--namespace and --visibility require --remote or --forge=github|gitlab")
		}
		return graduateUpstream{action: "local"}, nil
	}
	visibility := forge.VisibilityPrivate
	if !f.upstream.private {
		visibility = forge.VisibilityPublic
	}
	if f.upstream.visibility != "" {
		explicit := forge.Visibility(strings.ToLower(strings.TrimSpace(f.upstream.visibility)))
		if f.privateSet && explicit != visibility {
			return graduateUpstream{}, errors.New("--private conflicts with --visibility")
		}
		visibility = explicit
	}
	switch visibility {
	case forge.VisibilityPrivate, forge.VisibilityPublic, forge.VisibilityInternal:
	default:
		return graduateUpstream{}, errors.New("--visibility must be private, public or internal")
	}
	if hint == "github" && visibility == forge.VisibilityInternal {
		return graduateUpstream{}, errors.New("internal visibility requires --forge=gitlab")
	}
	namespace := strings.TrimSpace(f.upstream.namespace)
	if namespace != "" {
		for _, part := range strings.Split(namespace, "/") {
			if err := pathx.ValidateComponent(part); err != nil || strings.HasPrefix(part, "-") {
				return graduateUpstream{}, errors.New("--namespace must contain valid owner or group names")
			}
		}
		if hint == "github" && strings.Contains(namespace, "/") {
			return graduateUpstream{}, errors.New("GitHub namespace must be one owner or organization")
		}
	}
	return graduateUpstream{action: "create", push: f.upstream.push, create: repoPublishRequest{
		Forge: forge.Kind(hint), Namespace: namespace, Visibility: visibility, Push: f.upstream.push,
	}}, nil
}

func validateGraduateRemoteURL(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "-") ||
		strings.ContainsFunc(value, unicode.IsControl) || repo.RedactRemoteRef(value) == "<unsafe remote>" {
		return errors.New("remote URL must be a non-empty, valid Git remote reference")
	}
	return nil
}

// runTryGraduateWorkflow is the dashboard adapter. Expected binds the selected
// catalog record before prompting; the model owns no filesystem/publication policy.
func runTryGraduateWorkflow(ctx context.Context, app *App, request tui.WorkflowRequest) error {
	selected := request.Try.Item.Entry
	if selected == nil || selected.ID == "" || request.Try.Item.ID != selected.ID {
		return errors.New("select a cataloged Try before graduation")
	}
	return runGraduateWorkflow(ctx, app, selected.ID, selected.Clone(), defaultGraduateFlags(), true)
}

func runGraduateWorkflow(ctx context.Context, app *App, ref string, expected *catalog.Entry, flags graduateFlags, forceWizard bool) error {
	upstream, err := flags.upstreamRequest()
	if err != nil {
		return err
	}
	service, err := newExperimentService(app)
	if err != nil {
		return err
	}
	selection, diagnostics, err := service.ResolveGraduate(ctx, experiment.GraduateRequest{Ref: ref, Expected: expected})
	warnExperimentDiagnostics(app, diagnostics)
	if err != nil {
		return err
	}
	source := selection.Live.CurrentPath
	identity, err := gitx.DirectoryIdentity(source)
	if err != nil {
		return err
	}
	ref = selection.ID
	if ref == "" {
		ref = source
	}
	remotes, err := graduateRemotes(ctx, source)
	if err != nil {
		return err
	}
	if len(remotes) > 0 && upstream.action != "local" {
		return errors.New("this Try already has remotes; graduate locally to preserve them, or manage remotes explicitly first")
	}
	wizard := !flags.dryRun && !flags.yes && (forceWizard || app.interactive())
	var prompt *prompter
	if wizard {
		prompt = newPrompter(app)
		flags, err = promptGraduate(ctx, app, prompt, selection, remotes, flags, upstream.action)
		if err != nil {
			return err
		}
		upstream, err = flags.upstreamRequest()
		if err != nil {
			return err
		}
	}
	if current, err := gitx.DirectoryIdentity(source); err != nil || current != identity {
		return errors.New("selected Try directory changed while preparing graduation; select it again")
	}
	remotes, err = graduateRemotes(ctx, source)
	if err != nil {
		return err
	}
	if len(remotes) > 0 && upstream.action != "local" {
		return errors.New("Try remotes changed; no upstream action was applied")
	}
	plan, err := service.PlanGraduate(ctx, experiment.GraduateRequest{
		Ref: ref, Name: flags.name, Category: flags.category, DryRun: flags.dryRun, Expected: selection.Entry,
	})
	warnExperimentDiagnostics(app, plan.Diagnostics)
	if err != nil {
		return err
	}
	upstream.create.Name = plan.Name
	if upstream.action == "create" && strings.HasPrefix(plan.Name, "-") {
		return errors.New("upstream repository names must not begin with a dash")
	}
	if !flags.dryRun && upstream.action == "create" {
		kind, err := chooseForge(string(upstream.create.Forge))
		if err != nil {
			return err
		}
		upstream.create.Forge = kind
		if readiness := forge.Probe(ctx, kind); !readiness.Ready() {
			return fmt.Errorf("%s is not ready: %s; %s", kind, readiness.Detail, readiness.Action)
		}
		if kind == forge.GitHub && upstream.create.Visibility == forge.VisibilityInternal {
			return errors.New("internal visibility requires GitLab; choose --forge=gitlab")
		}
		if kind == forge.GitHub && strings.Contains(upstream.create.Namespace, "/") {
			return errors.New("GitHub namespace must be one owner or organization")
		}
	}
	if upstream.push && !plan.NeedsInitialCommit {
		status, err := gitx.StatusOf(gitx.WithReadOnlyObservations(ctx), plan.Source)
		if err != nil {
			return err
		}
		if status.Detached || status.Branch == "" || status.Branch == "HEAD" {
			return errors.New("cannot push a detached checkout; select --push=false")
		}
	}
	renderGraduatePlan(app, plan, remotes, upstream)
	if flags.dryRun {
		fmt.Fprintln(app.Out, "\n(dry run — nothing moved or published)")
		return nil
	}
	if wizard {
		confirmed, err := prompt.confirm("Graduate this Try?", true)
		if err != nil {
			return err
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	result, err := service.ApplyGraduate(ctx, plan)
	reportGraduateLocalEffects(app, result, err)
	if err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "\n%s is now a project at %s. Start work on it with:\n  dev work start %s --task <name>\n",
		result.Plan.Name, config.Contract(result.Plan.Destination), result.Plan.Name)
	if upstream.action != "local" {
		if err := applyGraduateUpstream(ctx, app, service, &result, upstream); err != nil {
			return err
		}
	}
	return graduateHandoff(ctx, app, service, result)
}

func reportGraduateLocalEffects(app *App, result experiment.GraduateResult, applyErr error) {
	if applyErr == nil {
		if result.GitInitialized {
			fmt.Fprintln(app.Out, "   git init")
		}
		if result.InitialCommitMade {
			fmt.Fprintln(app.Out, "   committed the existing work")
		}
		return
	}
	if result.GitInitialized {
		fmt.Fprintln(app.Err, "Git initialization completed and is retained.")
	}
	if result.InitialCommitMade {
		fmt.Fprintln(app.Err, "The initial commit completed and is retained.")
	}
	if result.RolledBack {
		fmt.Fprintf(app.Err, "The directory move was rolled back to %s.\n", config.Contract(result.Plan.Source))
	} else if result.Moved {
		fmt.Fprintf(app.Err, "The directory moved to %s; inspect the retained catalog recovery intent before retrying.\n", config.Contract(result.Plan.Destination))
	}
}

func graduateRemotes(ctx context.Context, source string) ([]string, error) {
	discovered, err := gitx.Discover(ctx, source)
	if errors.Is(err, gitx.ErrNotARepo) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if filepath.Clean(discovered.Root) != filepath.Clean(source) {
		return nil, nil
	}
	names, err := gitx.Run(ctx, source, "remote")
	if err != nil {
		return nil, err
	}
	return strings.Fields(names), nil
}

func promptGraduate(ctx context.Context, app *App, p *prompter, selected experiment.Item, remotes []string, flags graduateFlags, action string) (graduateFlags, error) {
	fmt.Fprintln(app.Out, p.style.title("Graduate a Try"))
	fmt.Fprintf(app.Out, "  source  %s\n", config.Contract(selected.Live.CurrentPath))
	name := flags.name
	if name == "" {
		name = experiment.SuggestedGraduateName(selected)
	}
	var err error
	flags.name, err = p.line("Project name", name)
	if err != nil {
		return flags, err
	}
	flags.category, err = p.line("Category (optional)", flags.category)
	if err != nil {
		return flags, err
	}
	if len(remotes) > 0 {
		return flags, nil
	}
	action, err = p.choiceOf("Upstream", action, []string{"local", "add", "create"},
		map[string]string{"local": "local", "none": "local", "add": "add", "url": "add", "create": "create"})
	if err != nil {
		return flags, err
	}
	switch action {
	case "local":
		flags.remoteURL, flags.upstream.namespace, flags.upstream.visibility = "", "", ""
		flags.upstream.remote, flags.urlSet, flags.privateSet = false, false, false
		flags.upstream.forge = "none"
	case "add":
		flags.remoteURL, err = p.line("Existing remote URL", flags.remoteURL)
		if err != nil {
			return flags, err
		}
		flags.urlSet = true
		pushDefault := flags.pushSet && flags.upstream.push
		flags.upstream.push, err = p.confirm("Push the current branch commits?", pushDefault)
		flags.pushSet = true
		flags.upstream.remote, flags.privateSet = false, false
		flags.upstream.namespace, flags.upstream.visibility, flags.upstream.forge = "", "", "auto"
	case "create":
		if flags.upstream.visibility == "" && flags.privateSet && !flags.upstream.private {
			flags.upstream.visibility = "public"
		}
		flags.remoteURL, flags.urlSet, flags.privateSet = "", false, false
		flags.upstream.remote = true
		if flags.upstream.forge == "none" {
			flags.upstream.forge = "auto"
		}
		if !flags.pushSet {
			flags.upstream.push = true
		}
		var available bool
		available, err = promptUpstreamCreation(ctx, p, &flags.upstream)
		if err == nil && !available {
			err = errors.New("no authenticated GitHub/GitLab CLI is ready; choose local graduation or add an existing URL")
		}
	}
	return flags, err
}

func renderGraduatePlan(app *App, plan experiment.GraduatePlan, remotes []string, upstream graduateUpstream) {
	fmt.Fprintf(app.Out, "graduate  %s\n       →  %s\n", config.Contract(plan.Source), config.Contract(plan.Destination))
	fmt.Fprintln(app.Out, "   preserve  catalog identity, tags, notes, history and current files")
	if plan.NeedsGitInit {
		fmt.Fprintln(app.Out, "   git       initialize Git")
	}
	if plan.NeedsInitialCommit {
		fmt.Fprintln(app.Out, "   commit    create the initial commit from current nonignored files")
	}
	if len(remotes) > 0 {
		fmt.Fprintf(app.Out, "   remotes   preserve %s\n", strings.Join(remotes, ", "))
	}
	switch upstream.action {
	case "add":
		fmt.Fprintf(app.Out, "   upstream  add origin %s; push=%t\n", repo.RedactRemoteRef(upstream.url), upstream.push)
	case "create":
		name := upstream.create.Name
		if upstream.create.Namespace != "" {
			name = upstream.create.Namespace + "/" + name
		}
		fmt.Fprintf(app.Out, "   upstream  create %s %s (%s); push=%t\n", upstream.create.Forge, name, upstream.create.Visibility, upstream.push)
	default:
		fmt.Fprintln(app.Out, "   upstream  no changes")
	}
}

func applyGraduateUpstream(ctx context.Context, app *App, service *experiment.Service, result *experiment.GraduateResult, upstream graduateUpstream) error {
	return applyGraduateUpstreamWithPublisher(ctx, app, service, result, upstream, publishRepositoryExpected)
}

type graduatePublisher func(context.Context, string, repoPublishRequest, *repoPublishExpectation) (repoPublishResult, error)

func applyGraduateUpstreamWithPublisher(ctx context.Context, app *App, service *experiment.Service, result *experiment.GraduateResult, upstream graduateUpstream, publish graduatePublisher) error {
	path := result.Plan.Destination
	var published repoPublishResult
	var err error
	if result.PublicationError != nil {
		err = fmt.Errorf("publication checkout could not be frozen: %w", result.PublicationError)
	} else if result.Publication == nil {
		err = errors.New("publication has no frozen checkout authority")
	} else if upstream.push && (result.Publication.Detached || result.Publication.Branch == "") {
		err = errors.New("a detached checkout does not authorize a push; use --push=false")
	} else {
		authority := *result.Publication
		expected := &repoPublishExpectation{Branch: authority.Branch, Head: authority.Head,
			Validate: func() error { return verifyGraduatePublication(ctx, authority) },
		}
		// Check before lock acquisition too, so a missing/replaced checkout does
		// not cause a lease directory to be manufactured at a stale location.
		if err = expected.Validate(); err == nil {
			err = gitx.WithLifecycleMoveLock(ctx, authority.GitCommonDir, func() error {
				var stageErr error
				if stageErr = expected.Validate(); stageErr == nil {
					var remotes []string
					remotes, stageErr = graduateRemotes(ctx, path)
					if stageErr == nil && len(remotes) > 0 {
						stageErr = errors.New("remotes appeared after graduation; upstream action was not started")
					}
				}
				if stageErr == nil {
					if upstream.action == "add" {
						published, stageErr = attachRepositoryUpstreamExpected(ctx, path, upstream.url, upstream.push, expected)
					} else {
						published, stageErr = publish(ctx, path, upstream.create, expected)
					}
				}
				// Origin refresh takes only its catalog transaction, not the asset/move
				// lease. Keep the repository lease until retained effects are observed.
				if refreshed, refreshErr := service.RefreshOrigin(ctx, result.Item.ID); refreshErr == nil {
					result.Item = refreshed
				} else if stageErr == nil {
					stageErr = refreshErr
				}
				return stageErr
			})
		}
	}
	if err != nil {
		if published.Added {
			fmt.Fprintf(app.Err, "Origin attached to %s; this configuration is retained.\n", repo.RedactRemoteRef(published.Remote.RemoteURL))
		}
		if published.created {
			fmt.Fprintf(app.Err, "Upstream created at %s.\n", repo.RedactRemoteRef(published.Remote.URL))
		} else if published.Remote.URL != "" {
			fmt.Fprintf(app.Err, "Upstream may exist at %s.\n", repo.RedactRemoteRef(published.Remote.URL))
		}
		if published.created || published.Added {
			switch {
			case published.Pushed:
				fmt.Fprintln(app.Err, "The reviewed commit was pushed; final local setup is incomplete.")
			case !upstream.push:
				fmt.Fprintln(app.Err, "Push was not requested.")
			case !published.pushAttempted:
				fmt.Fprintln(app.Err, "Push was not started.")
			default:
				fmt.Fprintln(app.Err, "Push was attempted; its outcome is unconfirmed. Inspect the upstream before retrying.")
			}
		}
		return fmt.Errorf("local graduation succeeded at %s; upstream action failed and was not retried: %w", config.Contract(path), repo.RedactCloneError(err, upstream.url))
	}
	fmt.Fprintf(app.Out, "   remote    %s\n", repo.RedactRemoteRef(published.Remote.RemoteURL))
	if published.Pushed {
		fmt.Fprintf(app.Out, "   pushed    %s\n", published.Branch)
	}
	return nil
}

func verifyGraduatePublication(ctx context.Context, expected experiment.GraduatePublication) error {
	ctx = gitx.WithReadOnlyObservations(ctx)
	if expected.Checkout == "" || !expected.Detached && expected.Branch == "" || expected.Head == "" {
		return errors.New("graduation publication authority is incomplete")
	}
	for _, target := range []struct{ path, identity string }{
		{expected.Checkout, expected.CheckoutIdentity}, {expected.GitCommonDir, expected.GitCommonIdentity}, {expected.GitDir, expected.GitDirIdentity},
	} {
		identity, err := gitx.DirectoryIdentity(target.path)
		if err != nil || target.identity == "" || identity != target.identity {
			return errors.New("graduated checkout or Git storage identity changed before publication")
		}
	}
	repository, err := gitx.Discover(ctx, expected.Checkout)
	if err != nil || filepath.Clean(repository.Root) != filepath.Clean(expected.Checkout) ||
		filepath.Clean(repository.GitCommonDir) != filepath.Clean(expected.GitCommonDir) ||
		filepath.Clean(repository.GitDir) != filepath.Clean(expected.GitDir) {
		return errors.New("graduated repository paths changed before publication")
	}
	branch, err := gitx.Run(ctx, expected.Checkout, "symbolic-ref", "--quiet", "HEAD")
	if expected.Detached && !quietGitExit(err, 1) || !expected.Detached && (err != nil || branch != "refs/heads/"+expected.Branch) {
		return errors.New("graduated branch changed before publication; push was not authorized")
	}
	head, err := gitx.Run(ctx, expected.Checkout, "rev-parse", "--verify", "HEAD")
	if err != nil || head != expected.Head {
		return errors.New("graduated HEAD changed before publication; push was not authorized")
	}
	return nil
}

func graduateHandoff(ctx context.Context, app *App, service *experiment.Service, result experiment.GraduateResult) error {
	copy := *app
	copy.workflowHandoff = nil
	handoff := func() error {
		target := result.Item.OpenTarget()
		if err := openOrCD(&copy, ctx, target.Path, result.Plan.Name); err != nil {
			return err
		}
		_, err := service.Touch(ctx, result.Item.ID)
		return err
	}
	if app.workflowHandoff != nil {
		return app.workflowHandoff(handoff)
	}
	return handoff()
}
