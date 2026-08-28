package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/config"
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
	var projectOnly, globalOnly, check, jsonOut bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "status"},
		Short:   "List project and global agent skills",
		Long: `List installed agent skills through the upstream skills CLI's JSON output.

With no scope flags, project and global skills are merged while remaining
separate rows. Project scope is rooted at the current Git checkout, even when
dev is run from a subdirectory. Listing never downloads the provider.

--check is the only form that contacts skill sources. It compares lock-recorded
content without writing installed skills or either lock file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			rows, err := agentskill.List(cmd.Context(), cwd, agentskill.ListOptions{
				Project: projectOnly, Global: globalOnly, Check: check,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return renderSkillJSON(app, rows)
			}
			return renderSkillTable(app, rows, agentskill.ProjectRoot(cmd.Context(), cwd), projectOnly, globalOnly)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&projectOnly, "project", "p", false, "list project skills")
	f.BoolVarP(&globalOnly, "global", "g", false, "list global skills")
	f.BoolVar(&check, "check", false, "contact Git sources and check for updates without installing them")
	f.BoolVar(&jsonOut, "json", false, "emit a stable machine-readable JSON array")
	return cmd
}

type skillJSON struct {
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	ScopeRoot    string   `json:"scope_root"`
	Path         string   `json:"path"`
	Agents       []string `json:"agents"`
	Source       string   `json:"source,omitempty"`
	SourceURL    string   `json:"source_url,omitempty"`
	SourceType   string   `json:"source_type,omitempty"`
	ManagedBy    string   `json:"managed_by"`
	UpdateStatus string   `json:"update_status"`
	UpdateDetail string   `json:"update_detail,omitempty"`
}

func renderSkillJSON(app *App, rows []agentskill.Skill) error {
	out := make([]skillJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, skillJSON{
			Name: row.Name, Scope: string(row.Scope), ScopeRoot: row.ScopeRoot,
			Path: row.Path, Agents: row.Agents, Source: row.Source,
			SourceURL: row.SourceURL, SourceType: row.SourceType,
			ManagedBy: string(row.ManagedBy), UpdateStatus: string(row.UpdateStatus),
			UpdateDetail: row.UpdateDetail,
		})
	}
	enc := json.NewEncoder(app.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderSkillTable(app *App, rows []agentskill.Skill, projectRoot string, project, global bool) error {
	if !project && !global {
		project = true
	}
	if project {
		fmt.Fprintf(app.Out, "project root  %s\n\n", config.Contract(projectRoot))
	}
	if len(rows) == 0 {
		fmt.Fprintln(app.Out, "No agent skills found.")
		return nil
	}
	t := NewTable("SCOPE", "SKILL", "UPDATE", "AGENTS", "SOURCE", "PATH")
	for _, row := range rows {
		t.Add(string(row.Scope), row.Name, shortUpdate(row.UpdateStatus), compactAgents(row.Agents),
			dash(row.Source), config.Contract(row.Path))
	}
	t.Render(app.Out)
	return nil
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
			proc.Stdin, proc.Stdout, proc.Stderr = app.In, app.Out, app.Err
			return proc.Run()
		},
	}
}

func newSkillUpdateCmd(app *App) *cobra.Command {
	var project, global, yes bool
	cmd := &cobra.Command{
		Use:   "update <skill>",
		Short: "Update one skill in one explicit scope",
		Long: `Update exactly one lock-managed skill through the upstream skills CLI.

Project or global scope is required. The command confirms in a terminal unless
--yes is supplied; a non-interactive invocation must always pass --yes.`,
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
			projectRoot := agentskill.ProjectRoot(cmd.Context(), cwd)
			if !agentskill.Managed(cmd.Context(), projectRoot, args[0], scope) {
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
			proc, err := agentskill.UpdateCommand(cmd.Context(), projectRoot, args[0], scope)
			if err != nil {
				return err
			}
			proc.Stdin, proc.Stdout, proc.Stderr = app.In, app.Out, app.Err
			return proc.Run()
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&project, "project", "p", false, "update the project-scoped skill")
	f.BoolVarP(&global, "global", "g", false, "update the global skill")
	f.BoolVarP(&yes, "yes", "y", false, "skip dev's confirmation")
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
