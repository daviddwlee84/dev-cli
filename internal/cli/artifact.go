package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func artifactStore(app *App) *artifact.Store {
	return artifact.NewStore(filepath.Join(app.Cfg.StateDir(), "artifact-intents", "v1"))
}

func newPrepareCmd(app *App) *cobra.Command {
	var (
		session       string
		specStoryPath string
		runID         string
		plans         []string
		allowLarge    bool
		closeout      string
		messageFile   string
		helperDir     string
		noPlan        bool
		jsonOutput    bool
	)
	cmd := &cobra.Command{
		Use:   "prepare [task-or-worktree]",
		Short: "Arm post-writer artifact finalization without closing this agent",
		Long: `Prepare a finished agent turn for an external coordinator.

Product changes must already be committed and the index empty. dev records the
exact worktree, branch, HEAD and agent session, but deliberately does not stage
the still-changing transcript, close the runtime, or remove the checkout.
After the agent exits, run dev artifact finalize from the outer SpecStory wrapper
or another workspace.

--closeout co-commit opts into a compatible canonical post-session helper. Stage
only reviewed product files, select the exact transcript and one plan (or
--no-plan), and provide a base --message-file. The real wrapper must be launched
without automatic commit authorization; dev never launches or closes an agent.
After queueing, report "finalization queued" and exit without further repository
operations. An external dev artifact finalize --allow-commit performs guarded
preparation and delegates one normal commit to the helper.

Use --run-id with co-commit only to bind an already-queued exact run after a
partial native binding failure; it cannot create wrapper lifecycle proof.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if closeout != "product-first" && closeout != "co-commit" {
				return errors.New("--closeout must be product-first or co-commit")
			}
			if closeout != "co-commit" && (messageFile != "" || helperDir != "" || noPlan) {
				return errors.New("--message-file, --closeout-helper and --no-plan require --closeout co-commit")
			}
			if closeout == "co-commit" && (len(plans) > 1 || (len(plans) == 0 && !noPlan) || (len(plans) != 0 && noPlan)) {
				return errors.New("co-commit requires one --plan or explicit --no-plan")
			}
			ctx := ctxOf()
			worktree, taskRecord, err := prepareTarget(app, args)
			if err != nil {
				return err
			}
			if session == "" && taskRecord != nil {
				session = taskRecord.AgentSession
			}
			if session == "" {
				session, err = inferAgentSession(ctx, app.Runtime(), worktree)
				if err != nil {
					return err
				}
			}
			if closeout == "co-commit" {
				request := artifact.CoCommitPrepareRequest{
					Worktree: worktree, Session: session, RunID: runID, SpecStoryPath: specStoryPath,
					NoPlan: noPlan, MessageFile: messageFile, HelperDir: helperDir, AllowLarge: allowLarge,
				}
				if len(plans) == 1 {
					request.Plan = plans[0]
				}
				if taskRecord != nil {
					request.TaskID, request.Base = taskRecord.ID, taskRecord.Base
				}
				service := &artifact.Service{Store: artifactStore(app)}
				result, err := service.PrepareCoCommit(ctx, request)
				return renderCoCommitResult(app, result, err, jsonOutput)
			}
			request := artifact.PrepareRequest{
				Worktree: worktree, Session: session, RunID: runID,
				Plans: plans, AllowLarge: allowLarge, SpecStoryPath: specStoryPath,
			}
			if taskRecord != nil {
				request.TaskID, request.Base = taskRecord.ID, taskRecord.Base
			}
			service := &artifact.Service{Store: artifactStore(app)}
			intent, err := service.Prepare(ctx, request)
			if err != nil {
				return err
			}
			if taskRecord != nil && taskRecord.AgentSession != session {
				taskRecord.AgentSession = session
				if err := app.Tasks.Save(taskRecord); err != nil {
					return err
				}
			}
			if jsonOutput {
				return renderArtifactPreparationJSON(app, intent)
			}
			fmt.Fprintf(app.Out, "PREPARED %s\n", intent.ID)
			fmt.Fprintf(app.Out, "   session   %s:%s\n", intent.Provider, intent.SessionID)
			fmt.Fprintf(app.Out, "   worktree  %s\n", config.Contract(intent.WorktreePath))
			fmt.Fprintf(app.Out, "   head      %s\n", shortOID(intent.Head))
			if len(intent.UnrelatedArtifacts) > 0 {
				fmt.Fprintln(app.Out, "   blockers  unrelated artifacts remain unstaged:")
				for _, path := range intent.UnrelatedArtifacts {
					fmt.Fprintf(app.Out, "             %s\n", path)
				}
			}
			fmt.Fprintln(app.Out, "Exit the agent normally; finalize only after the transcript writer stops.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&session, "session", "", "exact agent session provider:uuid (inferred from task/runtime when unique)")
	f.StringVar(&specStoryPath, "specstory-path", "", "exact SpecStory Markdown path matching the selected session and capture root")
	f.StringVar(&runID, "run-id", "", "outer wrapper run id (default: DEV_AGENT_RUN_ID or generated)")
	f.StringArrayVar(&plans, "plan", nil, "exact .claude/plans path to include (repeatable)")
	f.BoolVar(&allowLarge, "allow-large", false, "acknowledge adding a new untracked transcript over 2 MiB")
	f.StringVar(&closeout, "closeout", "product-first", "handoff lane: product-first or explicit canonical co-commit")
	f.StringVar(&messageFile, "message-file", "", "base commit message file for co-commit, without managed provenance trailers")
	f.StringVar(&helperDir, "closeout-helper", "", "explicit installed canonical helper scripts directory (co-commit only)")
	f.BoolVar(&noPlan, "no-plan", false, "explicitly select no plan for co-commit")
	f.BoolVar(&jsonOutput, "json", false, "emit a versioned preparation result, including retained partial effects")
	return cmd
}

func newArtifactCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "artifact", Short: "Manage coding-agent transcripts and plans", Long: `Preserve coding-agent conversation evidence and selected plans.

The current finalizer reads SpecStory Markdown. SpecStory is the recorder;
--session names the originating agent, such as codex:<uuid> or claude:<uuid>.
Native agent sessions and tool databases have their own backup/resume contracts.`}
	cmd.AddCommand(newArtifactFinalizeCmd(app), newArtifactListCmd(app), newArtifactDiscardCmd(app), newArtifactObserveCmd(app), newArtifactStatusCmd(app), newArtifactSetupCmd(app), newArtifactArchiveCmd(app), newArtifactFindCmd(app), newArtifactMigrateCmd(app), newArtifactSyncCmd(app), newArtifactBackupCmd(app))
	return cmd
}

func newArtifactFinalizeCmd(app *App) *cobra.Command {
	var (
		intentID          string
		runID             string
		settle            time.Duration
		ifPending         bool
		writerStopped     bool
		archivePlan       string
		allowCommit       bool
		reviewFile        string
		rotationConfirmed bool
		previewReview     bool
		revision          string
		jsonOutput        bool
	)
	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Finalize a prepared session after its transcript writer stops",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := artifactStore(app)
			resolvedRunID := firstNonEmpty(runID, os.Getenv("DEV_AGENT_RUN_ID"))
			if ifPending && intentID == "" {
				if _, err := store.FindByRunID(resolvedRunID); errors.Is(err, artifact.ErrIntentNotFound) {
					return nil
				} else if err != nil {
					return err
				}
			}
			service := &artifact.Service{Store: store, ScanStaged: scanAgentArtifacts}
			var selected *artifact.Intent
			var lookupErr error
			if intentID != "" {
				selected, lookupErr = store.Get(intentID)
			} else if resolvedRunID != "" {
				selected, lookupErr = store.FindByRunID(resolvedRunID)
			}
			if lookupErr != nil {
				return lookupErr
			}
			if selected != nil && selected.Destination == "co-commit" {
				if archivePlan != "" {
					return errors.New("co-commit does not consume an archive plan")
				}
				if previewReview {
					if allowCommit || reviewFile != "" || rotationConfirmed {
						return errors.New("--preview-review is read-only and cannot be combined with commit approval or review apply")
					}
					preview, err := service.PreviewCoCommitReview(ctxOf(), selected.ID)
					if err == nil && revision != "" && preview.NativeRevision != revision {
						return renderCoCommitReview(app, nil, artifact.ErrStaleRevision, jsonOutput)
					}
					return renderCoCommitReview(app, preview, err, jsonOutput)
				}
				result, err := service.FinalizeCoCommit(ctxOf(), artifact.CoCommitFinalizeRequest{
					IntentID: selected.ID, Revision: revision, AllowCommit: allowCommit, ReviewFile: reviewFile,
					RotationConfirmed: rotationConfirmed, Guard: (&hygieneCLI{app: app}).guard,
				})
				return renderCoCommitResult(app, result, err, jsonOutput)
			}
			if allowCommit || previewReview || reviewFile != "" || rotationConfirmed {
				return errors.New("co-commit approval/review flags require a co-commit intent")
			}
			intent, err := service.Finalize(ctxOf(), artifact.FinalizeRequest{
				IntentID: intentID, RunID: resolvedRunID, Settle: settle, WriterStopped: writerStopped, ArchivePlanID: archivePlan, Revision: revision, Guard: (&hygieneCLI{app: app}).guard,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return renderArtifactFinalizationJSON(app, intent)
			}
			fmt.Fprintf(app.Out, "FINALIZED %s\n", intent.ID)
			if intent.Destination == "archive" {
				fmt.Fprintf(app.Out, "   archive commit %s\n", shortOID(intent.ArchiveCommit))
			} else {
				fmt.Fprintf(app.Out, "   commit      %s\n", shortOID(intent.ArtifactCommit))
			}
			fmt.Fprintf(app.Out, "   transcript  %s\n", config.Contract(intent.TranscriptPath))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&archivePlan, "archive-plan", "", "reviewed archive plan for a prepared session (required for redaction copies)")
	f.StringVar(&intentID, "intent", "", "artifact intent id")
	f.StringVar(&runID, "run-id", "", "outer wrapper run id")
	f.DurationVar(&settle, "settle", 500*time.Millisecond, "required transcript stability interval")
	f.BoolVar(&ifPending, "if-pending", false, "silently succeed when no armed intent matches the run id")
	f.BoolVar(&writerStopped, "writer-stopped", false, "confirm the outer agent wrapper has returned before finalization")
	f.BoolVar(&allowCommit, "allow-commit", false, "authorize one guarded canonical co-commit attempt or reconciliation")
	f.BoolVar(&previewReview, "preview-review", false, "read the co-commit review receipt without mutating or committing")
	f.StringVar(&reviewFile, "review-file", "", "absolute private per-finding review file for exact co-commit recovery")
	f.BoolVar(&rotationConfirmed, "rotation-confirmed", false, "confirm actual credential rotation for the reviewed co-commit findings")
	f.StringVar(&revision, "revision", "", "require the exact reviewed native intent revision")
	f.BoolVar(&jsonOutput, "json", false, "emit a versioned finalization or review result")
	return cmd
}

func newArtifactDiscardCmd(app *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "discard <intent>",
		Short: "Abandon one failed artifact handoff",
		Long: `Record that an armed handoff will never produce a commit.

An intent whose transcript was never written, or whose HEAD no longer exists
after a rebase, can never be finalized, and until now it blocked integration and
retirement forever with no way out except editing dev state by hand.

Discarding commits nothing and recovers nothing. It states, durably, that the
operator inspected the intent and accepted the loss, which is why it refuses an
intent that is still armed: arm-to-finalize is the path that preserves a
transcript, and only a finalization that already failed is a dead end.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := artifactStore(app)
			record, err := store.GetRecord(args[0])
			if err != nil {
				return err
			}
			intent := record.Intent
			switch intent.Status {
			case artifact.Discarded:
				fmt.Fprintf(app.Out, "%s is already discarded\n", intent.ID)
				return nil
			case artifact.Finalized:
				return fmt.Errorf("%s is finalized; there is nothing to discard", intent.ID)
			case artifact.Armed, artifact.Finalizing:
				return fmt.Errorf("%s is %s; run `dev artifact finalize --intent %s` first, and discard only if that fails",
					intent.ID, intent.Status, intent.ID)
			}
			s := app.outStyle()
			fmt.Fprintf(app.Out, "%s\n", s.warning("DISCARDING "+intent.ID))
			fmt.Fprintf(app.Out, "   session    %s:%s\n", intent.Provider, intent.SessionID)
			fmt.Fprintf(app.Out, "   branch     %s\n", intent.Branch)
			fmt.Fprintf(app.Out, "   worktree   %s\n", config.Contract(intent.WorktreePath))
			fmt.Fprintf(app.Out, "   head       %s\n", shortOID(intent.Head))
			for _, plan := range intent.PlanPaths {
				fmt.Fprintf(app.Out, "   plan       %s\n", config.Contract(plan))
			}
			fmt.Fprintln(app.Out, "   No transcript will be committed. This cannot be undone.")
			if !yes {
				if !app.interactive() {
					return fmt.Errorf("discarding %s destroys a transcript handoff; pass --yes to confirm", intent.ID)
				}
				if !confirm(app, bufio.NewReader(app.In), "discard artifact intent "+intent.ID) {
					fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
					return nil
				}
			}
			if _, err := store.UpdateIfRevision(ctxOf(), intent.ID, record.Revision, func(current *artifact.Intent) error {
				current.Status = artifact.Discarded
				return nil
			}); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "DISCARDED %s\n", intent.ID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm discarding the intent without prompting")
	return cmd
}

func newArtifactListCmd(app *App) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending and completed artifact handoffs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			intents, err := artifactStore(app).List()
			if err != nil {
				return err
			}
			return renderArtifactHandoffs(app, intents, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit versioned handoffs with separate read-only helper observations")
	return cmd
}

func newArtifactObserveCmd(app *App) *cobra.Command {
	var runID string
	cmd := &cobra.Command{
		Use:    "observe-session-end",
		Short:  "Record SessionEnd for an already armed run",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runID = firstNonEmpty(runID, os.Getenv("DEV_AGENT_RUN_ID"))
			if runID == "" {
				return nil
			}
			return (&artifact.Service{Store: artifactStore(app)}).ObserveSessionEnd(ctxOf(), runID, time.Now())
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "outer wrapper run id")
	return cmd
}

func prepareTarget(app *App, args []string) (string, *task.Task, error) {
	if len(args) == 1 {
		expanded := config.Expand(args[0])
		if info, err := os.Stat(expanded); err == nil && info.IsDir() {
			return expanded, nil, nil
		}
		taskRecord, err := app.Tasks.Resolve(args[0])
		if err != nil {
			return "", nil, err
		}
		return checkoutOf(taskRecord), taskRecord, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	if taskRecord, taskErr := app.Tasks.FindByWorktree(cwd); taskErr == nil {
		return checkoutOf(taskRecord), taskRecord, nil
	}
	return cwd, nil, nil
}

func inferAgentSession(ctx context.Context, rt interface {
	List(context.Context) ([]runtime.Session, error)
}, worktree string) (string, error) {
	sessions, err := rt.List(ctx)
	if err != nil {
		return "", err
	}
	set := make(map[string]bool)
	for _, session := range sessions {
		if session.Covers(worktree) {
			for _, id := range session.AgentSessions {
				set[id] = true
			}
		}
	}
	if len(set) != 1 {
		return "", fmt.Errorf("cannot infer one agent session for %s; pass --session provider:uuid", config.Contract(worktree))
	}
	for id := range set {
		return id, nil
	}
	panic("unreachable")
}

func scanAgentArtifacts(ctx context.Context, repo string, paths []string) error {
	script := findArtifactScanner(repo)
	if script == "" {
		return fmt.Errorf("agent-history-hygiene scan-staged.sh not found")
	}
	redactor := filepath.Join(filepath.Dir(filepath.Dir(script)), "assets", "redact_secrets.py")
	if info, err := os.Stat(redactor); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("agent-history-hygiene redactor not found")
	}
	parents := make(map[string]bool)
	for _, path := range paths {
		rel, err := filepath.Rel(repo, filepath.Dir(path))
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("artifact path is outside repository")
		}
		parents[filepath.ToSlash(rel)] = true
	}
	redactArgs := []string{redactor, "--fix", "--paths"}
	for parent := range parents {
		redactArgs = append(redactArgs, parent)
	}
	if err := runSanitized(ctx, repo, "python3", redactArgs...); err != nil {
		return fmt.Errorf("artifact redactor failed: %w", err)
	}
	if _, err := gitx.Run(ctx, repo, append([]string{"add", "--"}, paths...)...); err != nil {
		return fmt.Errorf("restage redacted artifacts")
	}
	if err := runSanitized(ctx, repo, "bash", script); err != nil {
		return fmt.Errorf("artifact scanner failed: %w", err)
	}
	return nil
}

func runSanitized(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("exit %d", exitErr.ExitCode())
		}
		return fmt.Errorf("execute %s", name)
	}
	return nil
}

func findArtifactScanner(repo string) string {
	candidates := []string{
		filepath.Join(repo, ".claude", "skills", "agent-history-hygiene", "scripts", "scan-staged.sh"),
		filepath.Join(repo, ".agents", "skills", "agent-history-hygiene", "scripts", "scan-staged.sh"),
		filepath.Join(os.Getenv("HOME"), ".agents", "skills", "agent-history-hygiene", "scripts", "scan-staged.sh"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func shortOID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func ensureArtifactsFinalized(app *App, worktree string) error {
	store := artifactStore(app)
	intents, err := store.List()
	if err != nil {
		return err
	}
	canonical, err := pathx.Canonical(worktree)
	if err != nil {
		return err
	}
	proof, e := artifact.InspectHistoryReadiness(context.Background(), store, worktree)
	if e != nil {
		return e
	}
	if proof != nil && !proof.Ready {
		return errors.New("ignored agent history still needs a verified external archive before integration or retirement")
	}
	for _, intent := range intents {
		intentPath, canonicalErr := pathx.Canonical(intent.WorktreePath)
		if canonicalErr != nil || intentPath != canonical {
			continue
		}
		if intent.Status == artifact.Discarded {
			continue
		}
		if intent.Status != artifact.Finalized {
			return fmt.Errorf("artifact intent for %s is %s; finalize it, or discard it with `dev artifact discard %s`, before integration or retirement",
				config.Contract(worktree), intent.Status, intent.ID)
		}
		reconciled, reconcileErr := (&artifact.Service{Store: store}).Finalize(context.Background(), artifact.FinalizeRequest{IntentID: intent.ID})
		if reconcileErr != nil {
			return reconcileErr
		}
		intent = *reconciled
		if (intent.ArtifactCommit == "" && intent.ArchiveCommit == "") || !artifactCommitReachable(intent) {
			return fmt.Errorf("artifact commit for %s is no longer reachable; restore or re-finalize intent %s before integration or retirement",
				config.Contract(worktree), intent.ID)
		}
	}
	return nil
}

func artifactCommitReachable(intent artifact.Intent) bool {
	return artifact.CommitReachable(context.Background(), intent)
}

func artifactStatuses(app *App) (map[string]string, error) {
	inspections, err := artifact.InspectWorktrees(context.Background(), artifactStore(app))
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]string, len(inspections))
	for path, inspection := range inspections {
		statuses[path] = string(inspection.Status)
	}
	return statuses, nil
}

func artifactStatusForPath(statuses map[string]string, path string) string {
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return ""
	}
	return statuses[canonical]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
