package cli

import (
	"context"
	"errors"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

func newArtifactMigrateCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var o agenthistory.MigrationOptions
	var apply, yes, stopped bool
	var plan string
	var timeout time.Duration
	c := &cobra.Command{Use: "migrate", Short: "Stop tracking history or build a filtered repository copy", Long: `Preview an exact-path history migration with verified original Git recovery.

untrack saves the original refs/current selected files, keeps working files,
adds ignore entries, and removes only selected paths from the source index.
split builds original.bundle, original.git, history.git and filtered.git in a
new private output directory, with original-to-new commit maps and tree checks.
It does not change source refs, replace a remote, push, or rewrite release tags.

Backups contain raw content without scanning. Git bundles do not cover other
dirty files, reflogs, LFS payloads, unreachable objects or submodule repositories.
Current selected files have a 128 MiB per-file limit; choose at most 256 per plan.

  dev artifact migrate --mode split --path .specstory/history --json
  dev artifact migrate --apply --plan <id> --yes`, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(c.Context(), timeout)
		defer cancel()
		s, e := h.service(ctx)
		if e != nil {
			return e
		}
		if apply {
			if plan == "" {
				return errors.New("--apply requires --plan")
			}
			p, e := s.ReadPlan(ctx, plan)
			if e != nil {
				return e
			}
			if p.Kind != "artifact_migrate_untrack" && p.Kind != "artifact_migrate_split" {
				return errors.New("plan kind does not match migration")
			}
			if e = h.confirm(p, yes); e != nil {
				return e
			}
			p, e = s.ApplyMigration(ctx, plan, agenthistory.ApplyOptions{WriterStopped: stopped, Guard: (&hygieneCLI{app: app}).guard})
			return h.output(p, e)
		}
		if plan != "" || yes || stopped {
			return errors.New("--plan, --yes and --writer-stopped require --apply")
		}
		if o.Output != "" {
			o.Output = config.Expand(o.Output)
		}
		p, e := s.PreviewMigration(ctx, o)
		return h.output(p, e)
	}}
	h.bind(c)
	f := c.Flags()
	f.StringVar(&o.Mode, "mode", "", "untrack or split (required)")
	f.StringArrayVar(&o.Paths, "path", nil, "literal source-relative file/directory to extract (default: .specstory/history)")
	f.StringVar(&o.Output, "output", "", "new output directory outside Git (default: private migration state)")
	f.BoolVar(&apply, "apply", false, "apply the exact reviewed migration plan")
	f.StringVar(&plan, "plan", "", "reviewed migration plan ID")
	f.BoolVarP(&yes, "yes", "y", false, "confirm this migration without another prompt")
	f.BoolVar(&stopped, "writer-stopped", false, "attest the selected artifact recorder has exited (required for untrack)")
	f.DurationVar(&timeout, "timeout", 40*time.Minute, "deadline for backup/filter/verification")
	registerFlagCompletion(c, "mode", fixedCompletions("untrack", "split"))
	return c
}
