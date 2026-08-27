package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/bootstrap"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

func newBootstrapCmd(app *App) *cobra.Command {
	var (
		maxDepth      int
		followLinks   bool
		includeWT     bool
		jsonOut       bool
		indexRoot     string
		moveRoot      string
		layoutName    string
		relativeLinks bool
		apply         bool
		yes           bool
		configOut     string
		forceConfig   bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap [path...]",
		Short: "Discover and optionally organise an existing machine without breaking its layout",
		Long: `Recursively find Git repositories under the paths you name (or paths.scan_roots
when none are given), deduplicate them by Git identity, and classify canonical
checkouts, linked worktrees, bare repositories and symlink aliases.

The default is a report. It changes nothing.

For a non-destructive navigation layer, make a symlink index:

    dev bootstrap ~/code /mnt/work --index ~/Projects --layout flat
    dev bootstrap ~/code /mnt/work --index ~/Projects --apply

The physical repositories stay where they are. Put the index first in
paths.scan_roots if you want its names to be the UI; direct symlinks to repos
are first-class discovery entries and physical + indexed paths are shown once.

Physical moves are supported, but deliberately strict:

    dev bootstrap ~/old --move ~/Projects --layout preserve
    dev bootstrap ~/old --move ~/Projects --apply

Move refuses dirty repos, linked worktrees, live sessions, symlink aliases,
occupied destinations and cross-filesystem renames. It reports before acting,
and --apply still confirms each repository unless --yes is explicit.

Layout is never imposed globally. flat means <target>/<repo>; preserve mirrors
the path relative to each scan root. Existing scan_roots, project_root and
worktree_path remain freely configurable for any structure.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			// Flags override policy; otherwise the generated config is the one
			// place a machine says how deep to scan and how an index is laid out.
			if !cmd.Flags().Changed("max-depth") {
				maxDepth = app.Cfg.Bootstrap.MaxDepth
			}
			if !cmd.Flags().Changed("follow-symlinks") {
				followLinks = app.Cfg.Bootstrap.FollowSymlinks
			}
			if !cmd.Flags().Changed("layout") && app.Cfg.Bootstrap.Layout != "" {
				layoutName = app.Cfg.Bootstrap.Layout
			}
			if !cmd.Flags().Changed("relative-links") {
				relativeLinks = app.Cfg.Bootstrap.RelativeLinks
			}
			if !cmd.Flags().Changed("index") && moveRoot == "" && app.Cfg.Bootstrap.IndexRoot != "" {
				indexRoot = app.Cfg.Bootstrap.IndexRoot
			}

			roots := expandRoots(args)
			if len(roots) == 0 {
				roots = app.Cfg.ScanRoots()
				// An orphan checkout whose canonical repo is outside the scan
				// roots cannot be found through git worktree list. Include dev's
				// configured pool when it exists, without warning on a fresh
				// machine where it has not been created yet.
				wtRoot := config.Expand(app.Cfg.Paths.WorktreeRoot)
				if info, err := os.Stat(wtRoot); err == nil && info.IsDir() && !pathIn(wtRoot, roots) {
					roots = append(roots, wtRoot)
				}
			}
			if len(roots) == 0 {
				return fmt.Errorf("no paths to scan — pass one, or configure paths.scan_roots")
			}
			if indexRoot != "" && moveRoot != "" {
				return fmt.Errorf("--index and --move are different strategies; pick one")
			}

			layout, err := bootstrap.ParseLayout(layoutName)
			if err != nil {
				return err
			}
			repos, warnings := bootstrap.Scan(ctx, roots, bootstrap.Options{
				MaxDepth: maxDepth, FollowSymlinkDirs: followLinks, IncludeWorktrees: includeWT,
			})
			if moveRoot != "" {
				// The explicit roots decide which repos are candidates, but the
				// configured roots may hold a symlink index whose aliases would
				// break. Fold those aliases into matching candidates without
				// adding unrelated repos to the move plan.
				known, moreWarnings := bootstrap.Scan(ctx, app.Cfg.ScanRoots(), bootstrap.Options{
					MaxDepth: maxDepth, FollowSymlinkDirs: followLinks, IncludeWorktrees: includeWT,
				})
				repos = bootstrap.EnrichAliases(repos, known)
				warnings = append(warnings, moreWarnings...)
			}

			if jsonOut {
				return renderBootstrapJSON(app, roots, repos, warnings)
			}
			renderBootstrapScan(app, roots, repos, warnings)

			var plan bootstrap.OrganizePlan
			switch {
			case indexRoot != "":
				indexRoot = config.Expand(indexRoot)
				plan = bootstrap.IndexPlan(repos, indexRoot, layout, relativeLinks)
				renderOrganizePlan(app, plan)
				if apply {
					n, err := bootstrap.ApplyIndex(plan)
					if err != nil {
						return err
					}
					fmt.Fprintf(app.Out, "\ncreated %d symlink(s); no repository moved\n", n)
				}

			case moveRoot != "":
				moveRoot = config.Expand(moveRoot)
				plan = bootstrap.MovePlan(ctx, repos, moveRoot, layout)
				blockLiveMoves(app, &plan)
				renderOrganizePlan(app, plan)
				if apply {
					if len(plan.Blocked()) > 0 {
						return fmt.Errorf("move plan has %d blocked repository(s); resolve them before applying. "+
							"No repository moved", len(plan.Blocked()))
					}
					if err := applyMovePlan(app, plan, yes); err != nil {
						return err
					}
				}
			}

			if configOut != "" {
				configRoots := roots
				projectRoot := roots[0]
				if indexRoot != "" {
					// The index is a navigation layer, not where physical new
					// repos should be created. Put it first so its names are the
					// UI, retain the physical roots as fallback, and keep the
					// first physical root as project_root.
					configRoots = append([]string{indexRoot}, roots...)
					projectRoot = roots[0]
				} else if moveRoot != "" {
					configRoots, projectRoot = []string{moveRoot}, moveRoot
				}
				if err := writeBootstrapConfig(app, configOut, configRoots, projectRoot, forceConfig); err != nil {
					return err
				}
			}

			if !apply && (indexRoot != "" || moveRoot != "") {
				fmt.Fprintln(app.Out, "\nReport only. Re-run with --apply to act on ready rows.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&maxDepth, "max-depth", 8, "recursive depth; 0 means unlimited")
	f.BoolVar(&followLinks, "follow-symlinks", true, "follow symlinked container directories with cycle detection")
	f.BoolVar(&includeWT, "worktrees", true, "include linked worktrees in the report")
	f.BoolVar(&jsonOut, "json", false, "emit the scan as JSON")
	f.StringVar(&indexRoot, "index", "", "plan a non-destructive symlink catalog at this path")
	f.StringVar(&moveRoot, "move", "", "plan physical repository moves into this path")
	f.StringVar(&layoutName, "layout", "flat", "target layout: flat or preserve")
	f.BoolVar(&relativeLinks, "relative-links", false, "use relative symlink payloads in an index")
	f.BoolVar(&apply, "apply", false, "apply the ready index or move actions")
	f.BoolVar(&yes, "yes", false, "with --move --apply, do not confirm each repository")
	f.StringVar(&configOut, "config-out", "", "write a new config.toml for the resulting roots")
	f.BoolVar(&forceConfig, "force-config", false, "overwrite config-out if it exists")
	return cmd
}

func pathIn(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, r := range roots {
		r = filepath.Clean(r)
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func expandRoots(args []string) []string {
	out := make([]string, 0, len(args))
	for _, r := range args {
		if expanded := config.Expand(r); expanded != "" {
			out = append(out, expanded)
		}
	}
	return out
}

func renderBootstrapScan(app *App, roots []string, repos []bootstrap.Repository, warnings []error) {
	fmt.Fprintf(app.Out, "Scanned %s\n\n", strings.Join(contractAll(roots), ", "))
	if len(repos) == 0 {
		fmt.Fprintln(app.Out, "No repositories found.")
	} else {
		t := NewTable("KIND", "REPO", "BRANCH", "GIT", "ALIASES", "PATH")
		for _, r := range repos {
			git := "—"
			if r.Kind != bootstrap.Bare {
				git = r.Status.Summary()
			}
			aliases := "—"
			if len(r.Aliases) > 0 {
				aliases = fmt.Sprintf("%d", len(r.Aliases))
			}
			path := config.Contract(r.Path)
			if r.Symlink {
				path += " → " + config.Contract(r.RealPath)
			}
			t.Add(string(r.Kind), r.Name, dash(r.Branch), git, aliases, path)
		}
		t.Render(app.Out)
	}

	counts := map[bootstrap.Kind]int{}
	aliases := 0
	clones := map[string]bool{}
	for _, r := range repos {
		counts[r.Kind]++
		aliases += len(r.Aliases)
		clones[r.CloneKey()] = true
	}
	fmt.Fprintf(app.Out, "\n%d clone(s): %d canonical, %d worktree, %d bare, %d symlink alias(es)\n",
		len(clones), counts[bootstrap.Canonical], counts[bootstrap.Worktree], counts[bootstrap.Bare], aliases)
	for _, w := range warnings {
		app.warnf("scan: %v", w)
	}
}

func renderOrganizePlan(app *App, p bootstrap.OrganizePlan) {
	fmt.Fprintf(app.Out, "\n%s plan — %s layout at %s\n\n",
		strings.ToUpper(p.Mode), p.Layout, config.Contract(p.Root))
	if len(p.Actions) == 0 {
		fmt.Fprintln(app.Out, "No canonical repositories to organise.")
		return
	}
	t := NewTable("STATE", "REPO", "SOURCE", "TARGET / REASON")
	for _, a := range p.Actions {
		last := config.Contract(a.Target)
		if a.Reason != "" {
			last += " — " + a.Reason
		}
		t.Add(string(a.State), a.Repo.Name, config.Contract(a.Source), last)
	}
	t.Render(app.Out)
	fmt.Fprintf(app.Out, "\n%d ready, %d blocked, %d already current\n",
		len(p.Ready()), len(p.Blocked()), countActionState(p.Actions, bootstrap.Current))
}

func countActionState(actions []bootstrap.Action, state bootstrap.ActionState) int {
	n := 0
	for _, a := range actions {
		if a.State == state {
			n++
		}
	}
	return n
}

// blockLiveMoves adds runtime and cwd safety checks to the filesystem plan.
func blockLiveMoves(app *App, p *bootstrap.OrganizePlan) {
	ctx := ctxOf()
	sessions, _ := app.Runtime().List(ctx)
	cwd, _ := os.Getwd()
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.State != bootstrap.Ready {
			continue
		}
		if cwd == a.Source || strings.HasPrefix(cwd, a.Source+string(filepath.Separator)) {
			a.State, a.Reason = bootstrap.Blocked, "the current shell is inside this repository"
			continue
		}
		for _, s := range sessions {
			for _, dir := range s.Dirs {
				if dir == a.Source || strings.HasPrefix(dir, a.Source+string(filepath.Separator)) {
					a.State, a.Reason = bootstrap.Blocked,
						fmt.Sprintf("live %s session %s is inside the repository", app.Runtime().Name(), s.Handle)
					break
				}
			}
		}
	}
}

func applyMovePlan(app *App, plan bootstrap.OrganizePlan, yes bool) error {
	in := bufio.NewReader(os.Stdin)
	moved := 0
	for _, a := range plan.Actions {
		if a.State != bootstrap.Ready {
			continue
		}
		if !yes && !confirm(app, in, fmt.Sprintf("move %s to %s", a.Repo.Name, config.Contract(a.Target))) {
			continue
		}
		one := bootstrap.OrganizePlan{Mode: "move", Root: plan.Root, Layout: plan.Layout,
			Actions: []bootstrap.Action{a}}
		n, err := bootstrap.ApplyMoves(one)
		if err != nil {
			return fmt.Errorf("%d repository(s) moved before failure: %w", moved, err)
		}
		moved += n
		if err := rewriteTaskRepoPaths(app, a.Source, a.Target); err != nil {
			app.warnf("repository moved, but task paths could not be updated: %v", err)
		}
	}
	fmt.Fprintf(app.Out, "\nmoved %d repository(s) atomically\n", moved)
	if moved > 0 {
		fmt.Fprintln(app.Out, "Update paths.scan_roots if the destination is not already covered, or use --config-out next time.")
	}
	return nil
}

// rewriteTaskRepoPaths keeps task entries usable after an explicit move.
func rewriteTaskRepoPaths(app *App, from, to string) error {
	tasks, err := app.Tasks.List()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.RepoPath != from {
			continue
		}
		t.RepoPath = to
		// MovePlan refuses linked worktrees, so a task cannot have a valid
		// worktree path under the moved clone. Clear a stale one defensively.
		if t.WorktreePath != "" && strings.HasPrefix(t.WorktreePath, from+string(filepath.Separator)) {
			t.WorktreePath = strings.Replace(t.WorktreePath, from, to, 1)
		}
		if err := app.Tasks.Save(t); err != nil {
			return err
		}
	}
	return nil
}

func writeBootstrapConfig(app *App, path string, roots []string, projectRoot string, force bool) error {
	path = config.Expand(path)
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists; use --force-config to overwrite", config.Contract(path))
	}
	contracted := contractAll(roots)
	layout := config.Layout{
		ScanRoots: contracted, ProjectRoot: config.Contract(projectRoot),
		TriesRoot: app.Cfg.Paths.TriesRoot, WorktreeRoot: app.Cfg.Paths.WorktreeRoot,
	}
	body := renderStarterConfig(layout.Fallbacks())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "\nwrote %s with scan_roots = %v\n", config.Contract(path), contracted)
	return nil
}

type bootstrapJSON struct {
	Roots        []string               `json:"roots"`
	Repositories []bootstrap.Repository `json:"repositories"`
	Warnings     []string               `json:"warnings,omitempty"`
}

func renderBootstrapJSON(app *App, roots []string, repos []bootstrap.Repository, warnings []error) error {
	out := bootstrapJSON{Roots: roots, Repositories: repos}
	for _, w := range warnings {
		out.Warnings = append(out.Warnings, w.Error())
	}
	enc := json.NewEncoder(app.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
