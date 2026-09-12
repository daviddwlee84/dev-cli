package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"
)

func newArtifactSyncCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var push, pull, apply, yes bool
	var remote, branch, plan string
	c := &cobra.Command{Use: "sync", Short: "Fetch or publish the configured Git archive", Long: `Preview an explicit Git archive pull or push. Preview queries the remote refs.
Apply uses the reviewed URL, checkout and ref identity. Pull only fast-forwards
a clean archive; push proves fast-forward ancestry and uses an exact ref lease.
Divergence and changed configuration require a new review, never a force retry.
No native agent sessions or tool databases are synchronized.`, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
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
			if p.Kind != "artifact_sync_push" && p.Kind != "artifact_sync_pull" {
				return errors.New("plan kind does not match sync")
			}
			if e = h.confirm(p, yes); e != nil {
				return e
			}
			p, e = s.ApplySync(ctx, plan)
			return h.output(p, e)
		}
		if plan != "" || yes {
			return errors.New("--plan and --yes require --apply")
		}
		if push == pull {
			return errors.New("choose exactly one of --push or --pull")
		}
		direction := "push"
		if pull {
			direction = "pull"
		}
		p, e := s.PreviewSync(ctx, direction, remote, branch)
		return h.output(p, e)
	}}
	h.bind(c)
	f := c.Flags()
	f.BoolVar(&push, "push", false, "preview publication of archive commits")
	f.BoolVar(&pull, "pull", false, "preview fetching and fast-forwarding the archive")
	f.StringVar(&remote, "remote", "origin", "configured archive remote name")
	f.StringVar(&branch, "branch", "", "remote branch (must match the archive's current branch)")
	f.BoolVar(&apply, "apply", false, "apply the exact reviewed network plan")
	f.StringVar(&plan, "plan", "", "reviewed sync plan ID")
	f.BoolVarP(&yes, "yes", "y", false, "confirm applying the reviewed sync plan")
	return c
}

func newArtifactBackupCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var remote, plan string
	var apply, yes bool
	c := &cobra.Command{Use: "backup [migration-plan]", Short: "Publish a verified original Git backup to an empty remote", Long: `Preview publishing the frozen named refs of a completed migration's original Git backup.
The destination must be empty. Preview contacts it; apply pins every ref with an
empty-ref lease and verifies the published result. Existing refs are not replaced.
Raw Git history is not scanned or redacted by this operation. Selected working
snapshots stay in the migration's local current/ directory, outside the Git bundle.`, Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(c.Context(), 20*time.Minute)
		defer cancel()
		s, e := h.service(ctx)
		if e != nil {
			return e
		}
		if apply {
			if plan == "" || len(args) > 0 {
				return errors.New("apply requires --plan and no migration argument")
			}
			p, e := s.ReadPlan(ctx, plan)
			if e != nil {
				return e
			}
			if p.Kind != "artifact_backup" {
				return errors.New("plan kind does not match backup")
			}
			if e = h.confirm(p, yes); e != nil {
				return e
			}
			p, e = s.ApplyBackup(ctx, plan)
			return h.output(p, e)
		}
		if plan != "" || yes {
			return errors.New("--plan and --yes require --apply")
		}
		if len(args) != 1 || remote == "" {
			return errors.New("select a completed migration plan and --remote URL")
		}
		p, e := s.PreviewBackup(ctx, args[0], remote)
		return h.output(p, e)
	}}
	h.bind(c)
	f := c.Flags()
	f.StringVar(&remote, "remote", "", "empty destination Git URL or absolute local repository path")
	f.BoolVar(&apply, "apply", false, "publish the reviewed original Git refs")
	f.StringVar(&plan, "plan", "", "reviewed backup publication plan ID")
	f.BoolVarP(&yes, "yes", "y", false, "confirm the reviewed backup publication")
	return c
}
