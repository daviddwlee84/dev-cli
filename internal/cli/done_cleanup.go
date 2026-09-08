package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	retiredomain "github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

const retireHandoffTTL = 2 * time.Minute
const retireHandoffVersion = 2

type retireHandoffIntent struct {
	PreviewAuthority         map[string]string `json:"preview_authority,omitempty"`
	ProcessClosures          map[string]string `json:"process_closures,omitempty"`
	CallerPaneID             string            `json:"caller_pane_id,omitempty"`
	CallerPID                int               `json:"caller_pid,omitempty"`
	CallerShellPID           int               `json:"caller_shell_pid,omitempty"`
	CallerProcessFingerprint string            `json:"caller_process_fingerprint,omitempty"`
	Version                  int               `json:"version"`
	CreatedAt                time.Time         `json:"created_at"`
	ExpiresAt                time.Time         `json:"expires_at"`
	TaskID                   string            `json:"task_id"`
	TaskRevision             string            `json:"task_revision"`
	CheckoutPath             string            `json:"checkout_path"`
	HeadOID                  string            `json:"head_oid"`
	PreviewFingerprint       string            `json:"preview_fingerprint"`
	DeleteBranch             bool              `json:"delete_branch"`
	CloseUnknown             bool              `json:"close_unknown"`
}

func shouldOfferDoneCleanup(selected task.Task, opts doneOptions, interactive bool, action flow.Action) bool {
	return interactive && opts.Integration == doneIntegrationNone &&
		selected.EffectiveMode() == task.ModeWorktree && selected.WorktreePath != "" &&
		action != flow.ReviewHandoff
}

func runDoneCleanupWizard(ctx context.Context, app *App, p *prompter, final task.Task) error {
	rt := runtimeForTask(app, &final)
	authority, err := captureRetirementAuthority(ctx, app, retireCommandTarget{Task: &final}, flow.RetireOptions{})
	if err != nil {
		return err
	}
	preview, err := retiredomain.InspectForExternalCoordinator(ctx, rt, final.WorktreePath, retiredomain.Options{})
	if err != nil {
		app.warnf("retirement preview failed; cleanup was not attempted: %v", err)
		printRetireFallback(app, final, false, false)
		return nil
	}
	renderRetirementPreview(app, rt, preview)

	choice, promptErr := p.choice("Cleanup (k=keep, r=retire, d=retire+delete branch)", "keep",
		"keep (k), retire (r), retire and delete branch (d)", map[string]string{
			"k": "keep", "keep": "keep",
			"r": "retire", "retire": "retire",
			"d": "delete", "delete": "delete",
			"q": "keep", "cancel": "keep",
		})
	if errors.Is(promptErr, errPromptCanceled) || choice == "keep" {
		fmt.Fprintln(app.Out, "   cleanup kept · the task remains DONE")
		printRetireFallback(app, final, false, false)
		return nil
	}
	if promptErr != nil {
		return promptErr
	}
	deleteBranch := choice == "delete"
	options, fresh, canceled, err := confirmRetirement(ctx, app, p, rt, preview, flow.RetireOptions{DeleteBranch: deleteBranch, Timeout: 5 * time.Second, PreviewAuthority: authority})
	if err != nil {
		app.warnf("retirement was not attempted: %v", err)
		printRetireFallback(app, final, deleteBranch, options.CloseUnknown)
		return nil
	}
	if canceled {
		return nil
	}
	preview = fresh
	closeUnknown := options.CloseUnknown
	if rt.Name() == "herdr" && preview.CallerContained && len(preview.Sessions) > 0 {
		return launchExternalRetireCoordinator(ctx, app, rt, final, preview, deleteBranch, closeUnknown, options.ProcessClosures.Map(), options.PreviewAuthority)
	}
	if rt.Name() == "herdr" && !preview.CallerContained {
		return retireTaskWithTaskflow(ctx, app, &final, options, deleteBranch)
	}

	if err := app.retireDirective(final.RepoPath, final.ID, deleteBranch, closeUnknown); err != nil {
		app.warnf("integration is complete, but automatic retirement needs a refreshed dev shell wrapper: %v", err)
		printRetireFallback(app, final, deleteBranch, closeUnknown)
		return err
	}
	fmt.Fprintln(app.Out, "   retirement handoff queued · the shell will leave this checkout and revalidate cleanup")
	return nil
}

func renderRetirementPreview(app *App, rt runtime.Runtime, preview retiredomain.Inspection) {
	s := app.outStyle()
	fmt.Fprintln(app.Out, "\n"+s.title("Cleanup preview"))
	fmt.Fprintf(app.Out, "  %s  %s\n", s.label("runtime"), rt.Name())
	fmt.Fprintf(app.Out, "  target checkout %s\n  KEEP parent workspace and other tasks\n", config.Contract(preview.Target))
	fmt.Fprintln(app.Out, foregroundLimit)
	if len(preview.Sessions) == 0 {
		fmt.Fprintln(app.Out, "  sessions  none covering the worktree")
		return
	}
	sessions := append([]retiredomain.Session(nil), preview.Sessions...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Runtime.Handle < sessions[j].Runtime.Handle })
	for _, session := range sessions {
		label := session.Runtime.Handle
		if session.Runtime.Label != "" {
			label += " (" + session.Runtime.Label + ")"
		}
		disposition := "CLOSE candidate"
		if session.Protection != "" || len(session.Mixed) > 0 {
			disposition = "KEEP / BLOCKED"
		}
		paneIDs := make(map[string]bool)
		for _, pane := range append(append([]runtime.Pane(nil), session.Panes...), session.Mixed...) {
			paneIDs[pane.ID] = true
		}
		fmt.Fprintf(app.Out, "  %s workspace %s (%d pane(s))\n", disposition, runtime.DisplayText(label), len(paneIDs))
		for _, pane := range session.Panes {
			renderForegroundPane(app, pane)
		}
		if session.Protection != "" {
			fmt.Fprintf(app.Out, "    preserved: %s\n", session.Protection)
		}
		if len(session.Mixed) > 0 {
			fmt.Fprintln(app.Out, "    preserved panes outside the target (mixed workspace blocks retirement):")
			for _, pane := range session.Mixed {
				renderForegroundPane(app, pane)
			}
		}

	}
}

func printRetireFallback(app *App, final task.Task, deleteBranch, closeUnknown bool) {
	flags := ""
	if closeUnknown {
		flags += " --close-unknown"
	}
	if deleteBranch {
		flags += " --delete-branch"
	}
	fmt.Fprintf(app.Out, "   cleanup pending · cd %s && dev retire%s -- %s\n",
		shellQuote(final.RepoPath), flags, shellQuote(final.ID))
}

func launchExternalRetireCoordinator(
	ctx context.Context,
	app *App,
	rt runtime.Runtime,
	final task.Task,
	preview retiredomain.Inspection,
	deleteBranch, closeUnknown bool,
	processClosures map[string]string,
	previewAuthority flow.Fields,
) (err error) {
	if err := validateRetirementAuthority(ctx, app, final, previewAuthority); err != nil {
		return err
	}
	opener, ok := rt.(runtime.ExternalCoordinatorOpener)
	if !ok {
		return fmt.Errorf("runtime %s cannot create an external retirement coordinator", rt.Name())
	}
	runner, ok := rt.(runtime.PaneRunner)
	if !ok {
		return fmt.Errorf("runtime %s cannot run an external retirement coordinator", rt.Name())
	}
	record, err := app.Tasks.GetRecord(final.ID)
	if err != nil {
		return err
	}
	head, err := gitx.Run(ctx, final.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve retirement checkout HEAD: %w", err)
	}
	id, err := randomHandoffID()
	if err != nil {
		return err
	}
	dir := retireHandoffDir(app, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create retirement handoff: %w", err)
	}
	lease, err := lockx.AcquireDir(ctx, dir, "retirement handoff")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lease.Close())
		if err != nil {
			_ = os.RemoveAll(dir)
			_ = os.Remove(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".lock"))
		}
	}()
	intent := retireHandoffIntent{
		Version: retireHandoffVersion, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(retireHandoffTTL),
		TaskID: final.ID, TaskRevision: record.Revision, CheckoutPath: final.WorktreePath,
		HeadOID: strings.TrimSpace(head), PreviewFingerprint: preview.Fingerprint(),
		DeleteBranch: deleteBranch, CloseUnknown: closeUnknown, ProcessClosures: processClosures, PreviewAuthority: previewAuthority.Map(),
	}
	bindRetireCaller(&intent, preview)
	intent.PreviewFingerprint = retireHandoffFingerprint(preview, intent.CallerPaneID)
	if err := writeRetireHandoffIntent(dir, intent); err != nil {
		return err
	}
	opened, err := opener.OpenExternalCoordinator(ctx, final.RepoPath, "retire "+final.Title())
	if err != nil {
		return err
	}
	if !opened.Created || opened.RootPaneID == "" || opened.Handle == "" {
		if opened.Handle != "" {
			_ = rt.Close(ctx, opened.Handle)
		}
		return fmt.Errorf("external retirement coordinator did not return a new exact root pane")
	}
	command, err := retireCoordinatorCommand(app, id)
	if err != nil {
		_ = rt.Close(ctx, opened.Handle)
		return err
	}
	if err := runner.RunInPane(ctx, opened.RootPaneID, command); err != nil {
		_ = rt.Close(ctx, opened.Handle)
		return err
	}
	fmt.Fprintf(app.Out, "   retirement handed to external %s workspace %s\n", rt.Name(), opened.Handle)
	fmt.Fprintln(app.Out, "   it will revalidate, close the original workspace, then remove the worktree and task")
	return nil
}

func randomHandoffID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create retirement handoff id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func retireHandoffDir(app *App, id string) string {
	return filepath.Join(app.Cfg.StateDir(), "retire-handoffs", "v1", id)
}

func writeRetireHandoffIntent(dir string, intent retireHandoffIntent) error {
	body, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".intent.tmp")
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write retirement handoff: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "intent.json")); err != nil {
		return fmt.Errorf("publish retirement handoff: %w", err)
	}
	return nil
}

func retireCoordinatorCommand(app *App, id string) (string, error) {
	self, err := os.Executable()
	if err != nil || self == "" {
		return "", fmt.Errorf("resolve dev executable for retirement coordinator")
	}
	parts := []string{shellQuote(self)}
	if app.Cfg.Source != "" {
		parts = append(parts, "--config", shellQuote(app.Cfg.Source))
	}
	parts = append(parts, "__retire-coordinator", shellQuote(id))
	return strings.Join(parts, " "), nil
}

func newRetireCoordinatorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:    "__retire-coordinator <handoff-id>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRetireCoordinator(cmd.Context(), app, args[0])
		},
	}
}

func runRetireCoordinator(ctx context.Context, app *App, id string) (err error) {
	if len(id) != 32 {
		return errors.New("invalid retirement handoff id")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return errors.New("invalid retirement handoff id")
	}
	dir := retireHandoffDir(app, id)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lease, err := lockx.AcquireDir(waitCtx, dir, "retirement handoff")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lease.Close())
		_ = os.RemoveAll(dir)
		_ = os.Remove(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".lock"))
	}()
	body, err := os.ReadFile(filepath.Join(dir, "intent.json"))
	if err != nil {
		return fmt.Errorf("read retirement handoff: %w", err)
	}
	if err := os.Remove(filepath.Join(dir, "intent.json")); err != nil {
		return fmt.Errorf("consume retirement handoff: %w", err)
	}
	var intent retireHandoffIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		return fmt.Errorf("decode retirement handoff: %w", err)
	}
	if intent.Version != retireHandoffVersion || time.Now().UTC().After(intent.ExpiresAt) {
		return errors.New("retirement handoff expired or has an unsupported version; rerun dev done or dev retire")
	}
	record, err := app.Tasks.GetRecord(intent.TaskID)
	if err != nil {
		return err
	}
	if record.Revision != intent.TaskRevision {
		return errors.New("retirement handoff is stale: task revision changed")
	}
	selected := record.Task
	if selected.WorktreePath != intent.CheckoutPath {
		return errors.New("retirement handoff is stale: checkout path changed")
	}
	head, err := gitx.Run(ctx, selected.WorktreePath, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != intent.HeadOID {
		return errors.New("retirement handoff is stale: checkout HEAD changed")
	}
	rt := runtimeForTask(app, &selected)
	preview, err := awaitRetireCaller(ctx, rt, selected.WorktreePath, intent)
	if err != nil {
		return err
	}
	if retireHandoffFingerprint(preview, intent.CallerPaneID) != intent.PreviewFingerprint {
		return errors.New("retirement handoff is stale: runtime workspace or foreground state changed")
	}

	if !preview.Ready() {
		return fmt.Errorf("retirement blocked: %s", strings.Join(preview.Blockers, "; "))
	}
	return retireTaskWithTaskflow(ctx, app, &selected, flow.RetireOptions{
		CloseUnknown: intent.CloseUnknown, DeleteBranch: intent.DeleteBranch, Timeout: 5 * time.Second,
		ProcessClosures: flow.NewFields(intent.ProcessClosures), RuntimeFingerprint: preview.Fingerprint(), PreviewAuthority: flow.NewFields(intent.PreviewAuthority),
	}, intent.DeleteBranch)
}
