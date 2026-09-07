package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/spf13/cobra"
)

func newSubmoduleAddCmd(app *App, alias bool) *cobra.Command {
	var req submodule.AddRequest
	var yes, dryRun, jsonOut bool
	use := "add [source] [path]"
	if alias {
		use = "add-as-submodule [source] [path]"
	}
	cmd := &cobra.Command{
		Use: use, Short: "Add a known repository or network URL as a submodule of this checkout",
		Long: `Choose a known local repository's remote, a cached forge repository, or a network URL.
The destination is relative to the exact parent checkout's root. Existing paths
and Git stores are never adopted or overwritten. Only .gitmodules and the new
gitlink are staged; no commit, push, task branch or runtime is created.

Pinned checkout is the default. The wizard also offers the remote's actual
default branch. Local sources and URLs containing credentials are not supported.
Dry-run is local-only; refs are resolved only when the approved clone runs.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			if req.Parent == "" {
				var err error
				req.Parent, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			parent, parentErr := gitx.Discover(ctx, req.Parent)
			if parentErr != nil {
				return parentErr
			}
			req.Parent = parent.Root
			interactive := app.interactive() && !jsonOut
			if !interactive && !dryRun && !yes {
				return errors.New("non-interactive submodule add requires --yes (or --dry-run)")
			}
			if len(args) == 0 && !interactive {
				return errors.New("source is required outside an interactive terminal")
			}
			p := newPrompter(app)
			source := ""
			if len(args) > 0 {
				source = args[0]
			}
			var err error
			req.Source, err = resolveSubmoduleSource(ctx, app, p, source, interactive)
			if errors.Is(err, picker.ErrCanceled) || errors.Is(err, errPromptCanceled) {
				return submoduleAddCanceled(app, jsonOut)
			}
			if err != nil {
				return err
			}
			if len(args) > 1 {
				req.Path = args[1]
			} else {
				req.Path = repo.NameFromRef(req.Source)
			}
			if interactive && !yes {
				req.Path, err = p.line("Submodule path (relative to parent checkout root)", req.Path)
				if err == nil {
					req.Checkout, err = p.choice("Checkout mode", defaultString(req.Checkout, "pinned"), "pinned / default-branch", map[string]string{"pinned": "pinned", "default-branch": "default-branch"})
				}
				if err == nil && req.Checkout == "pinned" {
					req.Ref, err = p.line("Pinned ref (blank = remote default branch commit)", req.Ref)
				}
				if err == nil {
					settings, settingsErr := submodule.Settings(app.Cfg, req.Parent, req.Init)
					if settingsErr != nil {
						return settingsErr
					}
					req.Init, err = p.choice("Initialize descendants", settings.Init, "recursive / none", map[string]string{"recursive": "recursive", "none": "none"})
				}
				if errors.Is(err, errPromptCanceled) {
					return submoduleAddCanceled(app, jsonOut)
				}
				if err != nil {
					return err
				}
			}
			plan, err := submodule.PlanAdd(ctx, app.Cfg, req)
			if err != nil {
				return err
			}
			if dryRun {
				return renderSubmoduleAddition(app, plan.Report(), jsonOut)
			}
			if !jsonOut {
				if err := renderSubmoduleAddition(app, plan.Report(), false); err != nil {
					return err
				}
			}
			if !yes {
				confirmed, err := p.confirm("Clone, add and stage this submodule?", false)
				if errors.Is(err, errPromptCanceled) || (err == nil && !confirmed) {
					return submoduleAddCanceled(app, jsonOut)
				}
				if err != nil {
					return err
				}
			}
			result, applyErr := plan.Apply(ctx, func(ctx context.Context, root string) error {
				return guardSharedCheckout(ctx, app, app.Runtime(), root)
			})
			return errors.Join(applyErr, renderSubmoduleAddition(app, result, jsonOut))
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Parent, "parent", "", "exact parent checkout (default: nearest repository containing cwd)")
	f.StringVar(&req.Checkout, "checkout", "pinned", "new child checkout mode: pinned or default-branch")
	f.StringVar(&req.Ref, "ref", "", "commit, tag or branch to pin; requires --checkout=pinned")
	f.StringVar(&req.Init, "submodules", "", "initialize descendants: recursive or none (default: parent policy)")
	f.BoolVar(&yes, "yes", false, "approve cloning and staging without confirmation")
	f.BoolVar(&dryRun, "dry-run", false, "report a local plan without fetching or writing")
	f.BoolVar(&jsonOut, "json", false, "emit a structured plan or partial result without prompts")
	registerFlagCompletion(cmd, "checkout", fixedCompletions("pinned", "default-branch"))
	registerFlagCompletion(cmd, "submodules", fixedCompletions("recursive", "none"))
	return cmd
}

func submoduleAddCanceled(app *App, jsonOut bool) error {
	if jsonOut {
		return errors.New("submodule addition canceled")
	}
	_, err := fmt.Fprintln(app.Out, "Canceled; nothing was added.")
	return err
}

func renderSubmoduleAddition(app *App, r submodule.AddResult, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(app.Out).Encode(r)
	}
	fmt.Fprintf(app.Out, "submodule add: %s\n  parent      %s\n  source      %s\n  path        %s\n  checkout    %s\n  descendants %s\n", r.Phase, r.Parent, r.Source, r.Path, r.Checkout, r.Init)
	if r.HEAD != "" {
		fmt.Fprintf(app.Out, "  commit      %s\n", r.HEAD)
	} else {
		fmt.Fprintf(app.Out, "  ref         %s (resolved only during approved clone)\n", defaultString(r.Ref, "remote default branch"))
	}
	if r.Branch != "" {
		fmt.Fprintf(app.Out, "  branch      %s\n", r.Branch)
	}
	if r.Phase != "planned" {
		fmt.Fprintf(app.Out, "  git store   %s\n  staged      %t\n", r.GitDir, r.Staged)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintln(app.Out, "  warning     "+warning)
	}
	return nil
}

type submoduleSourceCandidate struct {
	url         string
	label       string
	description string
	aliases     []string
}

func submoduleSourceCandidates(ctx context.Context, app *App) ([]submoduleSourceCandidate, error) {
	var candidates []submoduleSourceCandidate
	identities := map[string]int{}
	add := func(candidate submoduleSourceCandidate) {
		if gitx.NetworkCloneURL(candidate.url) != nil {
			return
		}
		identity := catalog.NormalizeRemoteIdentity(candidate.url)
		if identity == "" {
			return
		}
		if i, ok := identities[identity]; ok {
			candidates[i].aliases = append(candidates[i].aliases, candidate.aliases...)
			return
		}
		identities[identity] = len(candidates)
		candidates = append(candidates, candidate)
	}
	locals, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.Options{})
	if err != nil {
		return nil, err
	}
	for _, r := range locals {
		if !r.HasGit || r.Bare {
			continue
		}
		sources, err := gitx.FetchCloneSources(ctx, r.Path)
		if err != nil {
			app.warnf("cannot read clone sources for %s", r.Display())
			continue
		}
		for _, s := range gitx.PreferredCloneSources(sources) {
			add(submoduleSourceCandidate{url: s.URL, label: r.Display() + " (" + s.Remote + ")", description: "local · " + r.Path, aliases: []string{r.Name, r.Display(), r.Path, s.URL}})
		}
	}
	rows, ok, stale := cachedRemoteRows(app)
	if ok && stale {
		app.warnf("remote cache is stale or incomplete; run `dev repo remote --refresh` to update candidates")
	}
	for _, row := range rows {
		r := row.Repo
		u := r.CloneURL
		if u == "" {
			u = r.SSHURL
		}
		add(submoduleSourceCandidate{url: u, label: r.Label(), description: r.Visibility + " · cached forge", aliases: []string{r.Name, r.FullName, r.Label(), u}})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].label < candidates[j].label })
	return candidates, nil
}

func resolveSubmoduleSource(ctx context.Context, app *App, p *prompter, source string, interactive bool) (string, error) {
	qualified := strings.HasPrefix(source, "github:") || strings.HasPrefix(source, "gitlab:") || strings.HasPrefix(source, "azure-devops:")
	if source != "" && !qualified && gitx.NetworkCloneURL(source) == nil {
		return source, nil
	}
	// Never include rejected URL text in diagnostics or interpret it as a query.
	if !qualified && (strings.Contains(source, ":") || strings.Contains(source, "@") || strings.ContainsAny(source, "?#\\") || strings.HasPrefix(source, "-")) {
		return "", gitx.NetworkCloneURL(source)
	}
	candidates, err := submoduleSourceCandidates(ctx, app)
	if err != nil {
		return "", err
	}
	var selected []submoduleSourceCandidate
	for _, c := range candidates {
		if source == "" {
			selected = append(selected, c)
			continue
		}
		for _, alias := range c.aliases {
			if alias == source {
				selected = append(selected, c)
				break
			}
		}
	}
	if source != "" && len(selected) == 1 {
		return selected[0].url, nil
	}
	if source != "" && len(selected) == 0 {
		if qualified {
			return "", errors.New("no cached repository matches the provider-qualified name; refresh inventory explicitly or provide a URL")
		}
		if filepath.IsAbs(source) || strings.HasPrefix(source, ".") {
			return "", errors.New("local-only sources are not supported; choose a known repository with a network remote")
		}
		normalized := repo.NormalizeCloneRef(source)
		if normalized != source && gitx.NetworkCloneURL(normalized) == nil {
			return normalized, nil
		}
		return "", errors.New("no known repository matches source; provide an exact name or network URL")
	}
	if !interactive {
		var labels []string
		for _, c := range selected {
			labels = append(labels, c.label)
		}
		return "", fmt.Errorf("ambiguous repository source; choose an exact URL from: %s", strings.Join(labels, ", "))
	}
	items := make([]picker.Item, 0, len(selected)+1)
	for _, c := range selected {
		items = append(items, picker.Item{Value: c.url, Label: c.label, Description: c.description})
	}
	if source == "" {
		items = append(items, picker.Item{Value: manualCloneReference, Label: "Enter a network URL or owner/name…"})
	}
	choice, used, err := app.pick(ctx, picker.Request{Prompt: "Repository to add as submodule", Items: items})
	if err != nil {
		return "", err
	}
	if used && choice.Item.Value != manualCloneReference {
		for _, c := range selected {
			if c.url == choice.Item.Value {
				return c.url, nil
			}
		}
		return "", picker.ErrCanceled
	}
	manual, err := p.line("Network Git URL or exact repository name", "")
	if err != nil {
		return "", err
	}
	if manual == "" {
		return "", errPromptCanceled
	}
	return resolveSubmoduleSource(ctx, app, p, manual, false)
}
