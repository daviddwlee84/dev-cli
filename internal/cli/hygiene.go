package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	rt "github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/spf13/cobra"
)

func hygieneInvocation(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "hygiene" {
			return true
		}
	}
	return false
}

type hygieneCLI struct {
	app                     *App
	repo                    string
	json, publicOnly        bool
	secrets, known, generic string
}

func (h *hygieneCLI) service(ctx context.Context) (*hygiene.Service, error) {
	root := h.repo
	if root == "" {
		root = mustGetwd()
	}
	root = config.Expand(root)
	return hygiene.Open(ctx, hygiene.Options{Root: root, CacheDir: sshDiscoveryCacheDir(), StateDir: h.app.Cfg.StateDir(), GlobalPolicy: filepath.Join(config.ConfigHome(), "dev", "hygiene.toml"), Override: hygiene.Policy{Secrets: hygiene.Mode(h.secrets), Known: hygiene.Mode(h.known), Generic: hygiene.Mode(h.generic)}, PublicOnly: h.publicOnly})
}
func (h *hygieneCLI) base(ctx context.Context) (*hygiene.Service, error) {
	copy := *h
	copy.secrets = ""
	copy.known = ""
	copy.generic = ""
	copy.publicOnly = false
	return copy.service(ctx)
}
func (h *hygieneCLI) output(value any, err error) error {
	if h.json {
		data, e := json.Marshal(value)
		if e != nil {
			return e
		}
		var safe any
		if e = json.Unmarshal(data, &safe); e != nil {
			return e
		}
		textFields := map[string]bool{"detail": true, "notices": true, "file": true, "rule": true, "reason": true, "replacement": true, "source": true, "visibility_source": true, "paths": true, "completed": true, "id": true, "rules": true, "masked": true, "commit": true}
		var scrub func(any, string) any
		scrub = func(v any, key string) any {
			switch x := v.(type) {
			case string:
				if textFields[key] {
					return feedback.Sanitize(x)
				}
			case []any:
				for i := range x {
					x[i] = scrub(x[i], key)
				}
			case map[string]any:
				for k := range x {
					x[k] = scrub(x[k], k)
				}
			}
			return v
		}

		enc := json.NewEncoder(h.app.Out)
		enc.SetIndent("", "  ")
		if e = enc.Encode(scrub(safe, "")); e != nil {
			return e
		}
	} else {
		switch v := value.(type) {
		case hygiene.Report:
			fmt.Fprintf(h.app.Out, "Hygiene %s · %s · %d files · %d blocking / %d warning findings\nReport %s\n", v.Scope, v.Status, v.Files, v.Blocked, v.Warnings, v.ID)
			for _, f := range v.Findings {
				fmt.Fprintf(h.app.Out, "  %s  %s:%d  %s ×%d  %s\n", f.Disposition, feedback.Sanitize(f.File), f.Line, feedback.Sanitize(f.Rule), f.Occurrences, f.ID)
			}
			for _, g := range v.Skipped {
				fmt.Fprintf(h.app.Out, "  excluded: %s %s\n", g.Code, feedback.Sanitize(g.File))
			}
			for _, g := range v.Gaps {
				fmt.Fprintf(h.app.Out, "  coverage: %s %s\n", g.Code, feedback.Sanitize(g.File))
				if g.Encoding != nil {
					h.encodingDetail(g.Encoding)
				}
				if g.Commit != "" {
					fmt.Fprintf(h.app.Out, "    commit: %s\n", feedback.Sanitize(g.Commit))
				}
			}
			if len(v.Gaps) > 0 {
				fmt.Fprintln(h.app.Out, "Scan incomplete: coverage gaps must be resolved; warning findings do not block commits.")
				for _, g := range v.Gaps {
					if g.Encoding != nil {
						fmt.Fprintln(h.app.Out, "Inspect dev hygiene repair-encoding --help for selected working-file repair. It preserves the index; review and stage the repair before rescanning staged content. Historical blobs require a separate history assessment.")
						break
					}
				}
			}
		case hygiene.Summary:
			h.renderSummary(v)
		case hygiene.Plan:
			fmt.Fprintf(h.app.Out, "%s · %s\nPlan %s\n", v.Kind, v.Status, v.ID)
			if v.ReviewFile != "" {
				fmt.Fprintf(h.app.Out, "Private full proposal: dev hygiene review-path %s\n", v.ID)
			}
			for _, f := range v.Files {
				fmt.Fprintf(h.app.Out, "  %s · %d replacements · %s → %s\n", feedback.Sanitize(f.File), f.Replacements, f.BeforeDigest, f.AfterDigest)
				if f.Encoding != nil {
					h.encodingDetail(f.Encoding)
				}
			}
			for _, notice := range v.Notices {
				fmt.Fprintln(h.app.Out, feedback.Sanitize(notice))
			}
			if v.HookAction != "" {
				fmt.Fprintf(h.app.Out, "Hook: %s\n", v.HookAction)
			}
			if v.RequiresWriterStopped {
				fmt.Fprintln(h.app.Out, "Artifact writes require the exact writer to stop before --apply --writer-stopped.")
			}
			for _, r := range v.Recovery {
				fmt.Fprintf(h.app.Out, "Recovery receipt: %s\n", r)
			}
		case hygiene.Status:
			fmt.Fprintf(h.app.Out, "Hook: %s (%s) · configured: %t\nPolicy: secrets=%s known=%s generic=%s\nRemote visibility: %s (never changes policy automatically)\n", v.Hook, v.HookScope, v.Configured, v.Policy.Secrets, v.Policy.Known, v.Policy.Generic, v.Visibility)
			for _, name := range []string{"pre-commit", "gitleaks"} {
				fmt.Fprintf(h.app.Out, "%s: %s\n", name, v.Tools[name])
			}
			for _, g := range v.Gaps {
				fmt.Fprintf(h.app.Out, "  gap: %s\n", g)
			}
		case hygiene.Candidates:
			fmt.Fprintf(h.app.Out, "Private candidates · complete: %t\n", v.Complete)
			for _, c := range v.Items {
				fmt.Fprintf(h.app.Out, "  %s  %s · %s:%d · recommend %s\n", c.ID, c.Kind, c.Source, c.Line, c.Recommended)
			}
		default:
			enc := json.NewEncoder(h.app.Out)
			if e := enc.Encode(value); e != nil {
				return e
			}
		}
	}
	if err != nil {
		return errors.New(feedback.Sanitize(err.Error()))
	}
	return nil
}

func (h *hygieneCLI) encodingDetail(e *hygiene.EncodingIssue) {
	fmt.Fprintf(h.app.Out, "    %s · first error at line %d, byte offset %d (zero-based) · %d invalid bytes in %d runs · %d NUL bytes\n", feedback.Sanitize(e.Reason), e.Line, e.ByteOffset, e.InvalidBytes, e.InvalidSequences, e.NULBytes)
}

func (h *hygieneCLI) guard(ctx context.Context, root string, paths []string) error {
	runtime := h.app.Runtime()
	if runtime == nil || runtime.Name() == "none" {
		return nil
	}
	observation, err := rt.InspectOccupancy(ctx, runtime, root, rt.OccupancyOptions{CallerWorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"), CallerPaneID: os.Getenv("HERDR_PANE_ID")})
	if err != nil || observation.SessionList.Err != nil || observation.AgentActivityList.Err != nil || observation.CurrentPane.Err != nil || observation.SessionCoverageErr != nil {
		return errors.New("cannot verify checkout writer occupancy")
	}
	targets := make([]hygiene.WriterTarget, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return errors.New("cannot identify artifact path for writer checks")
		}
		rel = filepath.ToSlash(rel)
		target := hygiene.WriterTarget{Artifact: hygiene.IsArtifactPath(rel)}
		if target.Artifact && specStoryTranscriptPath(rel) {
			if _, session, readErr := artifact.ReadTranscriptSession(p); readErr == nil {
				target.Session = session
			}
		}
		targets = append(targets, target)
	}
	agents := make([]hygiene.WriterAgent, 0, len(observation.Agents))
	for _, a := range observation.Agents {
		session := a.Activity.Session
		if session == "" && a.IsCaller && !observation.AgentActivityList.Supported {
			// Only backends without an exact agent inventory fall back to panes.
			session = callerPaneAgentSession(observation)
		}
		agents = append(agents, hygiene.WriterAgent{Caller: a.IsCaller, Blocking: a.Blocking, Session: session})
	}
	return hygiene.CheckWriters(agents, targets, h.app.allowSharedCheckout)
}

// specStoryTranscriptPath limits preamble ownership proof to SpecStory history
// Markdown; plans and other artifacts never prove a different session.
func specStoryTranscriptPath(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || !strings.HasSuffix(strings.ToLower(parts[len(parts)-1]), ".md") {
		return false
	}
	return strings.EqualFold(parts[len(parts)-3], ".specstory") && strings.EqualFold(parts[len(parts)-2], "history")
}

func callerPaneAgentSession(observation rt.Occupancy) string {
	if observation.CallerPaneID == "" {
		return ""
	}
	for _, session := range observation.Sessions {
		for _, pane := range session.Panes {
			if pane.ID == observation.CallerPaneID {
				return pane.AgentSession
			}
		}
	}
	return ""
}
func (h *hygieneCLI) apply(ctx context.Context, s *hygiene.Service, id string, yes, writerStopped bool, expectedKind string) error {
	p, err := s.ReadPlan(ctx, id)
	if err != nil {
		return err
	}
	if p.Kind != expectedKind {
		return errors.New("plan kind does not match this command")
	}
	if !yes {
		if !h.app.interactive() {
			return errors.New("review the saved plan, then use --apply --plan ID --yes")
		}
		if err = h.output(p, nil); err != nil {
			return err
		}
		ok, e := newPrompter(h.app).confirm("Apply this hygiene plan?", false)
		if e != nil {
			return e
		}
		if !ok {
			return nil
		}
	}
	result, err := s.Apply(ctx, id, hygiene.ApplyOptions{WriterStopped: writerStopped, Guard: h.guard})
	return h.output(result, err)
}
func newHygieneCmd(app *App) *cobra.Command {
	h := &hygieneCLI{app: app}
	cmd := &cobra.Command{Use: "hygiene", Short: "Inspect hooks, scan secrets and privacy, and apply reviewed text changes", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	f := cmd.PersistentFlags()
	f.StringVar(&h.repo, "repo", "", "checkout to inspect (default: current directory)")
	f.BoolVar(&h.json, "json", false, "emit sanitized schema-v1 JSON")
	f.BoolVar(&h.publicOnly, "public-only", false, "use repository policy without personal/global rules (CI)")
	f.StringVar(&h.secrets, "secrets", "", "override secret policy: block, warn or off")
	f.StringVar(&h.known, "known", "", "override known privacy policy: block, warn or off")
	f.StringVar(&h.generic, "generic", "", "override generic privacy policy: block, warn or off")
	var checkRemote bool
	var remote string
	status := &cobra.Command{Use: "status", Short: "Inspect effective hooks and policy without executing them", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		v, e := s.Status(c.Context())
		if checkRemote && e == nil {
			s.CheckVisibility(c.Context(), &v, remote)
		}
		return h.output(v, e)
	}}
	status.Flags().BoolVar(&checkRemote, "check-remote", false, "explicitly query GitHub visibility; never change policy automatically")
	status.Flags().StringVar(&remote, "remote", "origin", "remote to query with --check-remote")
	var scope, historyRange string
	var timeout time.Duration
	var scanFiles []string
	var audit bool
	scan := &cobra.Command{Use: "scan", Short: "Scan index, working files or frozen local Git history", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.service(c.Context())
		if e != nil {
			return e
		}
		r, e := s.Scan(c.Context(), hygiene.ScanOptions{Scope: scope, Range: historyRange, Timeout: timeout, Files: scanFiles, Audit: audit})
		if e == nil && !r.OK() {
			e = fmt.Errorf("hygiene policy blocks %d finding(s)", r.Blocked)
		}
		return h.output(r, e)
	}}
	scan.Flags().StringVar(&scope, "scope", "worktree", "scan scope: staged, worktree or history")
	scan.Flags().StringVar(&historyRange, "range", "", "history only: full FROM..TO commit OIDs (no implicit fetch)")
	scan.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "maximum scan duration; incomplete scans fail")
	scan.Flags().StringArrayVar(&scanFiles, "file", nil, "select an in-scope relative file (repeatable; not history)")
	scan.Flags().BoolVar(&audit, "audit", false, "include findings suppressed by local exceptions, gitleaksignore and inline pragmas")
	var setupApply, setupYes, migrate, migrateHooks bool
	var setupPlan string
	setup := &cobra.Command{Use: "setup", Short: "Preview or apply repository hygiene configuration and hook integration", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		if setupApply {
			return h.apply(c.Context(), s, setupPlan, setupYes, false, "hygiene_setup")
		}
		p, e := s.PreviewSetupOptions(c.Context(), hygiene.SetupOptions{ReplaceRules: migrate, MigrateHooks: migrateHooks, UpdateRules: migrateHooks})
		return h.output(p, e)
	}}
	setup.Flags().BoolVar(&setupApply, "apply", false, "apply the exact saved setup plan")
	setup.Flags().StringVar(&setupPlan, "plan", "", "reviewed plan ID")
	setup.Flags().BoolVarP(&setupYes, "yes", "y", false, "confirm the reviewed plan")
	setup.Flags().BoolVar(&migrate, "migrate-rules", false, "preview replacement of existing gitleaks config with bundled safe rules")
	setup.Flags().BoolVar(&migrateHooks, "migrate-hooks", false, "preview migration of known equivalent scanner hooks and rule updates; retain finalizers and custom settings")
	var report, redactPlan string
	var files, findings []string
	var redactApply, redactYes, writerStopped bool
	redact := &cobra.Command{Use: "redact", Short: "Preview selected text replacements or apply a saved plan", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		if redactApply {
			return h.apply(c.Context(), s, redactPlan, redactYes, writerStopped, "hygiene_redact")
		}
		p, e := s.PreviewRedact(c.Context(), report, files, findings)
		return h.output(p, e)
	}}
	rf := redact.Flags()
	rf.StringVar(&report, "report", "", "worktree scan report ID")
	rf.StringArrayVar(&files, "file", nil, "relative file to redact (repeatable)")
	rf.StringArrayVar(&findings, "finding", nil, "specific finding ID to redact (repeatable)")
	rf.BoolVar(&redactApply, "apply", false, "apply a reviewed replacement plan")
	rf.StringVar(&redactPlan, "plan", "", "reviewed plan ID")
	rf.BoolVarP(&redactYes, "yes", "y", false, "confirm the reviewed replacements")
	rf.BoolVar(&writerStopped, "writer-stopped", false, "attest the exact artifact writer has exited; other live agents still block, and the calling agent needs proven session ownership or --allow-shared-checkout")
	var repairFiles []string
	var invalidMode, repairPlan string
	var repairApply, repairYes, repairWriter bool
	repair := &cobra.Command{Use: "repair-encoding", Short: "Preview or repair invalid UTF-8 bytes in selected working files", Long: `Preview a guarded repair of explicitly selected UTF-8 working files.
The default replaces each contiguous invalid byte run with one replacement
character (�); --invalid remove deletes those bytes. Valid bytes are preserved.
NUL bytes, UTF-16/32 BOMs and known binary extensions require separate review.

Apply retains a private original for hygiene restore and never changes the Git
index. Review and stage selected changes before scanning staged content again.
Artifact writers must exit before apply; encoding repair is not a secret scan.`, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if repairApply {
			if repairPlan == "" || len(repairFiles) > 0 || c.Flags().Changed("invalid") {
				return errors.New("apply requires --plan and uses its saved files and invalid-byte policy")
			}
		} else if repairPlan != "" || repairYes || repairWriter {
			return errors.New("--plan, --yes and --writer-stopped require --apply")
		}
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		if repairApply {
			return h.apply(c.Context(), s, repairPlan, repairYes, repairWriter, "hygiene_repair_encoding")
		}
		p, e := s.PreviewRepairEncoding(c.Context(), repairFiles, invalidMode)
		return h.output(p, e)
	}}
	repair.Flags().StringArrayVar(&repairFiles, "file", nil, "relative working file to repair (repeatable; required for preview)")
	repair.Flags().StringVar(&invalidMode, "invalid", "replace", "invalid-byte handling: replace with � or remove")
	repair.Flags().BoolVar(&repairApply, "apply", false, "apply the exact reviewed encoding repair plan")
	repair.Flags().StringVar(&repairPlan, "plan", "", "reviewed encoding repair plan ID")
	repair.Flags().BoolVarP(&repairYes, "yes", "y", false, "confirm the reviewed repair")
	repair.Flags().BoolVar(&repairWriter, "writer-stopped", false, "attest the exact artifact writer has exited; other live agents still block, and the calling agent needs proven session ownership or --allow-shared-checkout")
	var receipt string
	var restoreApply, restoreYes, restoreWriter bool
	restore := &cobra.Command{Use: "restore", Short: "Preview or restore one private recovery receipt", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		if restoreApply && !restoreYes {
			return errors.New("inspect recovery first, then pass --apply --yes")
		}
		if restoreApply {
			preview, previewErr := s.Restore(c.Context(), receipt, false, hygiene.ApplyOptions{})
			if previewErr != nil {
				return h.output(preview, previewErr)
			}
			paths := make([]string, 0, len(preview))
			for _, change := range preview {
				paths = append(paths, change.Path)
			}
			if e = h.guard(c.Context(), s.Root, paths); e != nil {
				return e
			}
		}
		v, e := s.Restore(c.Context(), receipt, restoreApply, hygiene.ApplyOptions{WriterStopped: restoreWriter, Guard: h.guard})
		return h.output(v, e)
	}}
	restore.Flags().StringVar(&receipt, "receipt", "", "private recovery receipt ID")
	restore.Flags().BoolVar(&restoreApply, "apply", false, "restore unchanged post-apply files")
	restore.Flags().BoolVarP(&restoreYes, "yes", "y", false, "confirm the reviewed recovery")
	restore.Flags().BoolVar(&restoreWriter, "writer-stopped", false, "attest artifact writers have exited before recovery")
	review := &cobra.Command{Use: "review-path <plan-or-report-id>", Short: "Print a plan's or report's private review file location without its contents", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		path, e := s.ReviewPath(c.Context(), args[0])
		if e != nil {
			return e
		}
		if h.json {
			return json.NewEncoder(h.app.Out).Encode(map[string]string{"review_path": path})
		}
		fmt.Fprintln(h.app.Out, path)
		return nil
	}}
	cmd.AddCommand(status, scan, h.reportCmd(), setup, redact, repair, restore, review, h.rulesCmd(), newHygieneManageCmd(h))
	return cmd
}
func (h *hygieneCLI) rulesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "Manage private rules, policy overrides and precise exceptions"}
	var from string
	var selected []string
	imp := &cobra.Command{Use: "import", Short: "Preview local identity candidates or plan selected private rules", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		if len(selected) == 0 {
			v, e := s.Candidates(c.Context(), from)
			return h.output(v, e)
		}
		p, e := s.PreviewImport(c.Context(), from, selected)
		return h.output(p, e)
	}}
	imp.Flags().StringVar(&from, "from", "ssh", "static source: ssh, local or machines")
	imp.Flags().StringArrayVar(&selected, "select", nil, "candidate ID to import (repeatable)")
	var ruleID, kind, valueFile, replacement, action string
	var paths []string
	add := &cobra.Command{Use: "add", Short: "Plan a private literal, CIDR or RE2 rule from a local value file", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		var data []byte
		if valueFile == "-" {
			data, e = io.ReadAll(io.LimitReader(h.app.In, 8193))
		} else {
			data, e = safefile.ReadStablePath(c.Context(), config.Expand(valueFile), 8192)
		}
		if e != nil || len(data) > 8192 {
			return errors.New("rule value file is unreadable or oversized")
		}
		p, e := s.PreviewRules(c.Context(), []hygiene.Rule{{ID: ruleID, Kind: kind, Value: strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), Replacement: replacement, Action: hygiene.Mode(action), Paths: paths}}, nil, hygiene.Policy{Version: 1})
		return h.output(p, e)
	}}
	af := add.Flags()
	af.StringVar(&ruleID, "id", "", "non-sensitive rule label")
	af.StringVar(&kind, "kind", "literal", "literal, cidr or regex")
	af.StringVar(&valueFile, "value-file", "", "private rule value file; - reads stdin")
	af.StringVar(&replacement, "replacement", "", "replacement text (default: redaction sentinel)")
	af.StringVar(&action, "action", "", "rule policy: block, warn or off")
	af.StringArrayVar(&paths, "path", nil, "relative path glob (repeatable)")
	var allowReport, allowFinding, reason string
	allow := &cobra.Command{Use: "allow", Short: "Plan a local exception for one exact finding with a reason", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		p, e := s.PreviewAllow(c.Context(), allowReport, allowFinding, reason)
		return h.output(p, e)
	}}
	allow.Flags().StringVar(&allowReport, "report", "", "scan report ID")
	allow.Flags().StringVar(&allowFinding, "finding", "", "exact finding ID")
	allow.Flags().StringVar(&reason, "reason", "", "non-sensitive justification")
	policy := &cobra.Command{Use: "policy", Short: "Plan local overrides selected with --secrets, --known and --generic", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		p, e := s.PreviewRules(c.Context(), nil, nil, hygiene.Policy{Version: 1, Secrets: hygiene.Mode(h.secrets), Known: hygiene.Mode(h.known), Generic: hygiene.Mode(h.generic)})
		return h.output(p, e)
	}}
	var plan string
	var yes bool
	apply := &cobra.Command{Use: "apply", Short: "Apply one reviewed private rule or policy plan", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, e := h.base(c.Context())
		if e != nil {
			return e
		}
		return h.apply(c.Context(), s, plan, yes, false, "hygiene_rules")
	}}
	apply.Flags().StringVar(&plan, "plan", "", "reviewed rule plan ID")
	apply.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the reviewed rule changes")
	cmd.AddCommand(imp, add, allow, policy, apply)
	return cmd
}

func hygieneDoctorChecks(app *App) []check {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := gitx.Discover(ctx, mustGetwd()); err != nil {
		return nil
	}
	h := &hygieneCLI{app: app}
	s, err := h.service(ctx)
	if err != nil {
		return []check{{"repo hygiene", checkWarn, "policy unavailable; run dev hygiene status"}}
	}
	status, err := s.Status(ctx)
	if err != nil || len(status.Gaps) > 0 {
		return []check{{"repo hygiene", checkWarn, "hook or policy coverage incomplete; run dev hygiene status / setup"}}
	}
	return []check{{"repo hygiene", checkOK, "recognized hook chain and repository policy (not a content scan)"}}
}
