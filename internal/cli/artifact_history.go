package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/spf13/cobra"
)

type artifactHistoryCLI struct {
	app  *App
	repo string
	json bool
}

func (h *artifactHistoryCLI) bind(c *cobra.Command) {
	c.Flags().StringVar(&h.repo, "repo", "", "source repository or worktree (default: current directory)")
	c.Flags().BoolVar(&h.json, "json", false, "emit structured metadata without transcript contents")
}
func (h *artifactHistoryCLI) service(ctx context.Context) (*agenthistory.Service, error) {
	root := h.repo
	if root == "" {
		root = mustGetwd()
	}
	return agenthistory.Open(ctx, agenthistory.Options{Root: config.Expand(root), StateDir: h.app.Cfg.StateDir(), GlobalHygiene: filepath.Join(config.ConfigHome(), "dev", "hygiene.toml")})
}
func (h *artifactHistoryCLI) output(value any, err error) error {
	if p, ok := value.(agenthistory.Plan); ok && err != nil && p.Status == "prepared" {
		p.Status = "blocked"
		if errors.Is(err, agenthistory.ErrStale) {
			p.Status = "stale"
		}
		value = p
	}
	if h.json {
		enc := json.NewEncoder(h.app.Out)
		enc.SetIndent("", "  ")
		if e := enc.Encode(value); e != nil {
			return e
		}
	} else {
		switch v := value.(type) {
		case agenthistory.Plan:
			if v.Remote != "" {
				fmt.Fprintf(h.app.Out, "Remote: %s\n", feedback.Sanitize(v.Remote))
			}
			fmt.Fprintf(h.app.Out, "%s · %s\nPlan %s\n", v.Kind, v.Status, v.ID)
			if v.Archive != "" {
				fmt.Fprintf(h.app.Out, "Archive: %s\n", feedback.Sanitize(config.Contract(v.Archive)))
			}
			for _, f := range v.Files {
				fmt.Fprintf(h.app.Out, "  %s · %d bytes · %s\n", feedback.Sanitize(f.File), f.Bytes, f.Scan)
			}
			for _, report := range v.Reports {
				fmt.Fprintf(h.app.Out, "Hygiene report %s · %s · %d blocking\n", report.ID, report.Status, report.Blocked)
				for _, finding := range report.Findings {
					fmt.Fprintf(h.app.Out, "  %s:%d · %s · %s · %s\n", feedback.Sanitize(finding.File), finding.Line, feedback.Sanitize(finding.Rule), finding.Disposition, finding.ID)
				}
			}
			for _, notice := range v.Notices {
				fmt.Fprintln(h.app.Out, feedback.Sanitize(notice))
			}
			if v.ReviewDir != "" {
				fmt.Fprintf(h.app.Out, "Private review: %s\n", feedback.Sanitize(config.Contract(v.ReviewDir)))
			}
			if v.ArchiveCommit != "" {
				fmt.Fprintf(h.app.Out, "Archive commit: %s\n", v.ArchiveCommit)
			}
			if v.Output != "" {
				fmt.Fprintf(h.app.Out, "Output: %s\n", feedback.Sanitize(config.Contract(v.Output)))
			}
			for _, receipt := range v.Recovery {
				fmt.Fprintf(h.app.Out, "Recovery: %s\n", feedback.Sanitize(config.Contract(receipt)))
			}
		case agenthistory.Status:
			if !v.Configured {
				fmt.Fprintln(h.app.Out, "No artifact policy. Run dev artifact setup --help or dev help ai-artifacts.")
			} else {
				fmt.Fprintf(h.app.Out, "History: %s · source=%s · capture=%s\nTracked files: %d · pending archive: %d\n", v.Policy.Mode, v.Policy.Source, v.Policy.Capture, v.TrackedFiles, len(v.PendingFiles))
				if v.Binding != nil {
					fmt.Fprintf(h.app.Out, "Protection: %s · archive=%s\n", v.Binding.Protection, feedback.Sanitize(config.Contract(v.Binding.Archive)))
				}
				for _, file := range v.PendingFiles {
					fmt.Fprintln(h.app.Out, "  pending:", feedback.Sanitize(file))
				}
			}
			for _, notice := range v.Notices {
				fmt.Fprintln(h.app.Out, feedback.Sanitize(notice))
			}
		case []agenthistory.Match:
			for _, m := range v {
				fmt.Fprintf(h.app.Out, "%s:%s · %s · %s\n  %s", m.Record.Provider, m.Record.SessionID, shortOID(m.Record.SourceCommit), m.Record.Protection, feedback.Sanitize(config.Contract(m.Path)))
				if len(m.Lines) > 0 {
					fmt.Fprintf(h.app.Out, " · lines %v", m.Lines)
				}
				fmt.Fprintln(h.app.Out)
			}
			fmt.Fprintf(h.app.Out, "%d match(es); content remains in the archive.\n", len(v))
		}
	}
	if err != nil {
		return errors.New(feedback.Sanitize(err.Error()))
	}
	return nil
}
func (h *artifactHistoryCLI) confirm(p agenthistory.Plan, yes bool) error {
	if yes {
		return nil
	}
	if h.json || !h.app.interactive() {
		return errors.New("review the artifact plan and pass --yes to apply")
	}
	_ = h.output(p, nil)
	if !confirm(h.app, bufio.NewReader(h.app.In), "apply artifact plan "+p.ID) {
		return errors.New("artifact operation canceled")
	}
	return nil
}

func newArtifactStatusCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	c := &cobra.Command{Use: "status", Short: "Show how agent history is captured, stored, and protected", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		r, e := s.Status(c.Context())
		return h.output(r, e)
	}}
	h.bind(c)
	return c
}
func newArtifactSetupCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var o agenthistory.SetupOptions
	var apply, yes bool
	var plan string
	c := &cobra.Command{Use: "setup", Short: "Choose where agent history is kept and what enters Git", Long: `Preview repository history policy, local archive binding and optional SpecStory configuration.

Sources are specstory (recorder Markdown) or files (explicit exported files).
The provider in --session is the originating agent, such as codex or claude.
Archive protection is off, check or redact; it never changes source commit hooks.

Setup edits selected policy/config/ignore/export files only. It does not untrack,
commit, push, install tools or start an agent. Create the archive checkout with
ordinary git init or git clone first. Inspect the plan, then apply its exact ID.

  dev artifact setup --mode archive --source specstory --archive /path/to/history --protection off --json
  dev artifact setup --apply --plan <id> --yes`, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		if apply {
			if plan == "" {
				return errors.New("--apply requires --plan")
			}
			p, e := s.ReadPlan(c.Context(), plan)
			if e != nil {
				return e
			}
			if p.Kind != "artifact_setup" {
				return errors.New("plan kind does not match setup")
			}
			if e = h.confirm(p, yes); e != nil {
				return e
			}
			p, e = s.ApplySetup(c.Context(), plan)
			return h.output(p, e)
		}
		if plan != "" || yes {
			return errors.New("--plan and --yes require --apply")
		}
		if o.Archive != "" {
			o.Archive = config.Expand(o.Archive)
		}
		p, e := s.PreviewSetup(c.Context(), o)
		return h.output(p, e)
	}}
	h.bind(c)
	f := c.Flags()
	f.StringVar(&o.Mode, "mode", "", "retention: track, archive or unmanaged (required on first setup)")
	f.StringVar(&o.Source, "source", "", "capture source: specstory or files (detects existing SpecStory history)")
	f.StringVar(&o.Capture, "capture", "", "project or external (external requires local SpecStory config)")
	f.StringVar(&o.Archive, "archive", "", "absolute path to a separate existing Git archive checkout")
	f.StringVar(&o.Protection, "protection", "", "archive copy: off, check or redact (default: check)")
	f.StringArrayVar(&o.Paths, "path", nil, "literal source-relative file/directory (repeatable; SpecStory defaults to .specstory/history)")
	f.BoolVar(&o.ExportIgnore, "export-ignore", true, "exclude selected evidence from git archive source packages")
	f.BoolVar(&apply, "apply", false, "apply the exact reviewed setup plan")
	f.StringVar(&plan, "plan", "", "reviewed setup plan ID")
	f.BoolVarP(&yes, "yes", "y", false, "confirm applying the reviewed plan")
	registerFlagCompletion(c, "mode", fixedCompletions("track", "archive", "unmanaged"))
	registerFlagCompletion(c, "source", fixedCompletions("specstory", "files"))
	registerFlagCompletion(c, "protection", fixedCompletions("off", "check", "redact"))
	return c
}
func newArtifactArchiveCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var o agenthistory.ArchiveOptions
	var apply, yes, stopped bool
	var plan string
	c := &cobra.Command{Use: "archive", Short: "Preserve selected history in an external Git archive", Long: `Preview immutable copies of selected transcript/plan files, then commit them to the configured archive.

Source files and the source index stay unchanged. Redaction, when selected,
changes the archive copy only. No push occurs. Review the private proposal and
wait for the exact recorder to stop before applying it. Capturing a stable file
does not prove its writer has stopped. --file paths are relative to the source
repo, including when SpecStory writes to an external capture directory.`, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		if apply {
			if plan == "" {
				return errors.New("--apply requires --plan")
			}
			p, e := s.ReadPlan(c.Context(), plan)
			if e != nil {
				return e
			}
			if p.Kind != "artifact_archive" {
				return errors.New("plan kind does not match archive")
			}
			if e = h.confirm(p, yes); e != nil {
				return e
			}
			guard := (&hygieneCLI{app: app}).guard
			p, e = s.ApplyArchive(c.Context(), plan, agenthistory.ApplyOptions{WriterStopped: stopped, Guard: guard})
			return h.output(p, e)
		}
		if plan != "" || yes || stopped {
			return errors.New("--plan, --yes and --writer-stopped require --apply")
		}
		p, e := s.PreviewArchive(c.Context(), o)
		return h.output(p, e)
	}}
	h.bind(c)
	f := c.Flags()
	f.StringVar(&o.Session, "session", "", "exact originating provider:uuid from the SpecStory preamble")
	f.StringArrayVar(&o.Files, "file", nil, "exact configured source-relative file (repeatable)")
	f.DurationVar(&o.Timeout, "timeout", 20*time.Minute, "snapshot scan deadline")
	f.BoolVar(&apply, "apply", false, "commit the exact reviewed copies to the archive")
	f.StringVar(&plan, "plan", "", "reviewed archive plan ID")
	f.BoolVarP(&yes, "yes", "y", false, "confirm the reviewed archive commit")
	f.BoolVar(&stopped, "writer-stopped", false, "attest the exact recorder has exited before applying")
	return c
}
func newArtifactFindCmd(app *App) *cobra.Command {
	h := &artifactHistoryCLI{app: app}
	var q agenthistory.Query
	c := &cobra.Command{Use: "find [text]", Short: "Find saved history by session, commit, or text", Long: `Find locally available committed archive records. Text is a literal, case-sensitive query.
Output contains metadata, paths and line numbers, never transcript snippets.
This does not fetch remotes, scan native agent directories, or resume an agent.`, Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		if len(args) > 0 {
			q.Text = args[0]
		}
		m, e := s.Find(c.Context(), q)
		return h.output(m, e)
	}}
	h.bind(c)
	c.Flags().StringVar(&q.Session, "session", "", "originating provider:uuid")
	c.Flags().StringVar(&q.Commit, "commit", "", "full source commit ID")
	c.Flags().BoolVar(&q.All, "all", false, "search all projects in this configured archive")
	c.Flags().IntVar(&q.Limit, "limit", 100, "maximum matching records (1–1000)")
	return c
}
