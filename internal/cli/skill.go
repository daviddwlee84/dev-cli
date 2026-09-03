package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/skill"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newSkillCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Inspect agent skills and manage dev's bundled skill",
		Long: `dev ships the agent skill that documents it.

Keeping the skill inside the binary is what stops the two drifting: an agent
reading a stale command list is worse than one reading none. The same content
is available as "dev --skill", so a dotfiles installer can sync it without
vendoring a copy.`,
	}
	cmd.AddCommand(
		newSkillListCmd(app),
		newSkillAddCmd(app),
		newSkillUpdateCmd(app),
		newSkillPrintCmd(app),
		newSkillInstallCmd(app),
		newSkillSyncCmd(app),
	)
	return cmd
}

func newSkillListCmd(app *App) *cobra.Command {
	var (
		projectOnly bool
		globalOnly  bool
		check       bool
		jsonOut     bool
		all         bool
		repoRef     string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "status"},
		Short:   "List project and global agent skills",
		Long: `List installed agent skills directly from documented agent paths and lock files.

With no scope flags, project and global skills remain separate rows. Project
scope defaults to the exact current checkout, including a linked worktree.
--all scans each configured canonical repository; --repo selects one repository
or explicit checkout path. Listing never runs Node, npm, npx, or agent code.

--check is the only form that contacts skill sources. It compares lock-recorded
content without writing installed skills or either lock file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && repoRef != "" {
				return fmt.Errorf("choose at most one of --all or --repo")
			}
			project := projectOnly || !globalOnly
			if !project && (all || repoRef != "") {
				return fmt.Errorf("--all and --repo require project scope")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targets, err := resolveAgentTargets(cmd.Context(), app, cwd, all, repoRef, project)
			if err != nil {
				return err
			}
			result, err := inventory.CollectAgentSkills(cmd.Context(), targets, inventory.AgentSkillOptions{
				Project: projectOnly, Global: globalOnly, Check: check,
			})
			if err != nil {
				return err
			}
			renderSkillDiagnostics(app, result.Diagnostics)
			if jsonOut {
				return renderSkillJSON(app, result.Skills)
			}
			return renderSkillTable(app, result.Skills, targets, projectOnly, globalOnly)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&projectOnly, "project", "p", false, "list project skills")
	f.BoolVarP(&globalOnly, "global", "g", false, "list global skills")
	f.BoolVar(&all, "all", false, "scan every configured canonical repository")
	f.StringVarP(&repoRef, "repo", "r", "", "scan one repository or explicit checkout path")
	f.BoolVar(&check, "check", false, "contact Git sources and check for updates without installing them")
	f.BoolVar(&jsonOut, "json", false, "emit a stable machine-readable JSON array")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	return cmd
}

func resolveAgentTargets(ctx context.Context, app *App, cwd string, all bool, repoRef string, project bool) ([]agenttarget.Target, error) {
	if !project {
		return nil, nil
	}
	if all {
		return agenttarget.All(ctx, app.Cfg.DiscoveryRoots())
	}
	if repoRef != "" {
		target, err := agenttarget.ResolveRepository(ctx, app.Cfg.DiscoveryRoots(), repoRef)
		if err != nil {
			return nil, err
		}
		return []agenttarget.Target{target}, nil
	}
	target, err := agenttarget.Current(ctx, cwd)
	if err != nil {
		return nil, err
	}
	canonical, discoverErr := agenttarget.All(ctx, app.Cfg.DiscoveryRoots())
	if discoverErr == nil {
		target = agenttarget.ReconcileCurrent(canonical, target)
	}
	return []agenttarget.Target{target}, nil
}

type skillInstallationJSON struct {
	Path            string   `json:"path"`
	RealPath        string   `json:"real_path"`
	LogicalPaths    []string `json:"logical_paths"`
	AgentIDs        []string `json:"agent_ids"`
	Integrity       string   `json:"integrity"`
	IntegrityDetail string   `json:"integrity_detail,omitempty"`
}

type skillJSON struct {
	Name            string                  `json:"name"`
	Scope           string                  `json:"scope"`
	ScopeRoot       string                  `json:"scope_root"`
	Path            string                  `json:"path"`
	Agents          []string                `json:"agents"`
	Source          string                  `json:"source,omitempty"`
	SourceURL       string                  `json:"source_url,omitempty"`
	SourceType      string                  `json:"source_type,omitempty"`
	ManagedBy       string                  `json:"managed_by"`
	UpdateStatus    string                  `json:"update_status"`
	UpdateDetail    string                  `json:"update_detail,omitempty"`
	Repo            string                  `json:"repo,omitempty"`
	RepoPath        string                  `json:"repo_path,omitempty"`
	Checkout        string                  `json:"checkout,omitempty"`
	Installations   []skillInstallationJSON `json:"installations,omitempty"`
	Presence        string                  `json:"presence"`
	Integrity       string                  `json:"integrity"`
	IntegrityDetail string                  `json:"integrity_detail,omitempty"`
	AgentIDs        []string                `json:"agent_ids,omitempty"`
	RegistrySource  string                  `json:"registry_source"`
	RegistryVersion string                  `json:"registry_version"`
	LockVersion     int                     `json:"lock_version,omitempty"`
	Ref             string                  `json:"ref,omitempty"`
	SkillPath       string                  `json:"skill_path,omitempty"`
	Plugin          string                  `json:"plugin,omitempty"`
	InstalledAt     string                  `json:"installed_at,omitempty"`
	UpdatedAt       string                  `json:"updated_at,omitempty"`
	WellKnownDigest string                  `json:"well_known_digest,omitempty"`
}

func renderSkillJSON(app *App, rows []agentskill.Skill) error {
	out := make([]skillJSON, 0, len(rows))
	for _, row := range rows {
		installations := make([]skillInstallationJSON, 0, len(row.Installations))
		for _, installation := range row.Installations {
			installations = append(installations, skillInstallationJSON{
				Path: installation.Path, RealPath: installation.RealPath,
				LogicalPaths: installation.LogicalPaths, AgentIDs: installation.AgentIDs,
				Integrity: string(installation.Integrity), IntegrityDetail: installation.IntegrityDetail,
			})
		}
		item := skillJSON{
			Name: row.Name, Scope: string(row.Scope), ScopeRoot: row.ScopeRoot,
			Path: row.Path, Agents: row.Agents, Source: row.Source,
			SourceURL: row.SourceURL, SourceType: row.SourceType,
			ManagedBy: string(row.ManagedBy), UpdateStatus: string(row.UpdateStatus),
			UpdateDetail: row.UpdateDetail, Repo: row.Repository, RepoPath: row.RepositoryPath,
			Checkout: row.Checkout, Installations: installations, Presence: string(row.Presence),
			Integrity: string(row.Integrity), IntegrityDetail: row.IntegrityDetail,
			AgentIDs: row.Attribution.AgentIDs, RegistrySource: row.Attribution.Registry,
			RegistryVersion: row.RegistryVersion,
		}
		if row.Lock != nil {
			item.LockVersion, item.Ref, item.SkillPath = row.Lock.Version, row.Lock.Ref, row.Lock.SkillPath
			item.Plugin, item.InstalledAt, item.UpdatedAt = row.Lock.PluginName, row.Lock.InstalledAt, row.Lock.UpdatedAt
			item.WellKnownDigest = row.Lock.WellKnownDigest
		}
		out = append(out, item)
	}
	enc := json.NewEncoder(app.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderSkillTable(app *App, rows []agentskill.Skill, targets []agenttarget.Target, project, global bool) error {
	if !project && !global {
		project = true
	}
	if project {
		if len(targets) == 1 {
			fmt.Fprintf(app.Out, "project root  %s\n\n", config.Contract(targets[0].CheckoutRoot))
		} else {
			fmt.Fprintf(app.Out, "project roots  %d repositories\n\n", len(targets))
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(app.Out, "No agent skills found.")
		return nil
	}
	style := app.outStyle()
	t := app.newTable("REPO", "SCOPE", "SKILL", "INSTALL", "UPDATE", "AGENTS", "SOURCE", "PATH")
	for _, row := range rows {
		path := "—"
		if row.Path != "" {
			path = config.Contract(row.Path)
		}
		t.Add(dash(row.Repository), style.dim(string(row.Scope)), row.Name,
			style.updateState(skillInstallLabel(row)), style.updateState(shortUpdate(row.UpdateStatus)),
			compactAgents(row.Agents), dash(row.Source), path)
	}
	t.Render(app.Out)
	return nil
}

func skillInstallLabel(row agentskill.Skill) string {
	if row.Presence == agentskill.PresenceMissing {
		return "missing"
	}
	switch row.Integrity {
	case agentskill.IntegrityVerified:
		return "verified"
	case agentskill.IntegrityDrifted:
		return "drifted"
	default:
		return "present"
	}
}

func renderSkillDiagnostics(app *App, diagnostics []agentskill.Diagnostic) {
	for _, diagnostic := range diagnostics {
		owner := string(diagnostic.Scope)
		if diagnostic.Repository != "" {
			owner = diagnostic.Repository + "/" + owner
		}
		fmt.Fprintf(app.Err, "warning: skill inventory %s (%s): %s\n",
			owner, config.Contract(diagnostic.Path), diagnostic.Message)
	}
}

func compactAgents(agents []string) string {
	if len(agents) == 0 {
		return "—"
	}
	names := append([]string(nil), agents...)
	sort.Strings(names)
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d", len(names)-3)
}

func shortUpdate(status agentskill.UpdateStatus) string {
	switch status {
	case agentskill.UpdateAvailable:
		return "update"
	case agentskill.UpdateMissing:
		return "missing"
	case agentskill.UpdateUnknown:
		return "unknown"
	case agentskill.UpdateFailed:
		return "failed"
	default:
		return string(status)
	}
}

func newSkillAddCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "add [package]",
		Short: "Open the interactive skills installer",
		Long: `Start the upstream skills interactive wizard at the current Git checkout root.

With no package, use ` + agentskill.DefaultSource + `. dev does not select all
skills, agents, or a scope: those decisions remain in the upstream wizard.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			source := agentskill.DefaultSource
			if len(args) == 1 {
				source = args[0]
			}
			proc, err := agentskill.AddCommand(cmd.Context(), agentskill.ProjectRoot(cmd.Context(), cwd), source)
			if err != nil {
				return err
			}
			proc.Command.Stdin, proc.Command.Stdout, proc.Command.Stderr = app.In, app.Out, app.Err
			return proc.Run()
		},
	}
}

func newSkillUpdateCmd(app *App) *cobra.Command {
	var (
		project bool
		global  bool
		yes     bool
		repoRef string
	)
	cmd := &cobra.Command{
		Use:   "update <skill>",
		Short: "Update one skill in one explicit scope",
		Long: `Update exactly one lock-managed skill through the upstream skills CLI.

Project or global scope is required. --repo selects a non-current project
checkout. The command confirms in a terminal unless --yes is supplied; a
non-interactive invocation must always pass --yes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == global {
				return fmt.Errorf("choose exactly one of --project or --global")
			}
			scope := agentskill.ScopeProject
			if global {
				scope = agentskill.ScopeGlobal
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if global && repoRef != "" {
				return fmt.Errorf("--repo is only valid with --project")
			}
			projectRoot := agentskill.ProjectRoot(cmd.Context(), cwd)
			if repoRef != "" {
				target, err := agenttarget.ResolveRepository(cmd.Context(), app.Cfg.DiscoveryRoots(), repoRef)
				if err != nil {
					return err
				}
				projectRoot = target.CheckoutRoot
			}
			managedName, managed := agentskill.ManagedName(cmd.Context(), projectRoot, args[0], scope)
			if !managed {
				return fmt.Errorf("%s skill %s is not managed by the skills CLI", scope, args[0])
			}
			if !yes {
				if !app.interactive() {
					return fmt.Errorf("non-interactive skill update requires --yes")
				}
				if !confirm(app, bufio.NewReader(app.In), fmt.Sprintf("update %s skill %s", scope, args[0])) {
					fmt.Fprintln(app.Out, "not changed")
					return nil
				}
			}
			proc, err := agentskill.UpdateCommand(cmd.Context(), projectRoot, managedName, scope)
			if err != nil {
				return err
			}
			proc.Command.Stdin, proc.Command.Stdout, proc.Command.Stderr = app.In, app.Out, app.Err
			return proc.Run()
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&project, "project", "p", false, "update the project-scoped skill")
	f.BoolVarP(&global, "global", "g", false, "update the global skill")
	f.StringVarP(&repoRef, "repo", "r", "", "project repository or explicit checkout path")
	f.BoolVarP(&yes, "yes", "y", false, "skip dev's confirmation")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	return cmd
}

func newSkillPrintCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "print",
		Short: "Print SKILL.md to stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := skill.Render()
			if err != nil {
				return err
			}
			fmt.Fprint(app.Out, out)
			return nil
		},
	}
}

func newSkillInstallCmd(app *App) *cobra.Command {
	var (
		dir    string
		noLink bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the skill into the agent skills directory",
		Long: `Write the bundled skill to ~/.agents/skills/dev-cli and symlink it into the
per-tool skill directories that exist on this machine (~/.claude/skills).

Re-running is a no-op when nothing changed, so this is safe to call from a
dotfiles bootstrap on every apply.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := dir
			if target == "" {
				target = skill.DefaultDir()
			}
			res, err := skill.Install(config.Expand(target), !noLink)
			if err != nil {
				return err
			}
			switch {
			case len(res.Written) == 0:
				fmt.Fprintf(app.Out, "skill already current at %s\n", config.Contract(res.Dir))
			default:
				fmt.Fprintf(app.Out, "installed %d file(s) to %s\n", len(res.Written), config.Contract(res.Dir))
				for _, f := range res.Written {
					fmt.Fprintf(app.Out, "   %s\n", f)
				}
			}
			for _, l := range res.Links {
				fmt.Fprintf(app.Out, "   linked %s\n", config.Contract(l))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "install directory (default: ~/.agents/skills/dev-cli)")
	f.BoolVar(&noLink, "no-link", false, "do not symlink into per-tool skill directories")
	return cmd
}

func newSkillSyncCmd(app *App) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Regenerate the skill's command reference from the live command tree",
		Long: `Rewrite the generated section of the skill's command reference from this
binary's actual command tree, so the documented flags can never drift from the
implemented ones.

With --check, report drift and exit non-zero instead of writing — the form to
wire into CI or a pre-push hook.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			generated := generateCommandReference(cmd.Root())
			return syncCommandReference(app, generated, check)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report drift and exit non-zero instead of writing")
	return cmd
}

// generateCommandReference renders the whole command tree as markdown. This is
// the single source of truth for what the skill tells an agent dev can do.
func generateCommandReference(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("<!-- generated by `dev skill sync`; do not edit by hand -->\n\n")
	if flags := renderFlagSet(root.PersistentFlags()); flags != "" {
		b.WriteString("### Global options\n\n")
		b.WriteString(flags)
		b.WriteString("\n")
	}
	writeCommand(&b, root, 0)
	return b.String()
}

func writeCommand(b *strings.Builder, c *cobra.Command, depth int) {
	if c.Hidden || c.Name() == "no-help" {
		return
	}
	if depth > 0 {
		fmt.Fprintf(b, "### `%s`\n\n%s\n\n", c.CommandPath(), firstLine(c.Short))
		if usage := c.UseLine(); usage != "" {
			fmt.Fprintf(b, "```\n%s\n```\n\n", usage)
		}
		if flags := renderFlags(c); flags != "" {
			b.WriteString(flags)
			b.WriteString("\n")
		}
	}
	for _, sub := range c.Commands() {
		writeCommand(b, sub, depth+1)
	}
}

func renderFlags(c *cobra.Command) string {
	return renderFlagSet(c.LocalNonPersistentFlags())
}

func renderFlagSet(flags *pflag.FlagSet) string {
	var b strings.Builder
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", name, f.Usage)
	})
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
