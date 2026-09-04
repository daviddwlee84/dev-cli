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

type retireHandoffIntent struct {
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	TaskID             string    `json:"task_id"`
	TaskRevision       string    `json:"task_revision"`
	CheckoutPath       string    `json:"checkout_path"`
	HeadOID            string    `json:"head_oid"`
	PreviewFingerprint string    `json:"preview_fingerprint"`
	DeleteBranch       bool      `json:"delete_branch"`
	CloseUnknown       bool      `json:"close_unknown"`
}

func shouldOfferDoneCleanup(selected task.Task, opts doneOptions, interactive bool, action flow.Action) bool {
	return interactive && opts.Integration == doneIntegrationNone &&
		selected.EffectiveMode() == task.ModeWorktree && selected.WorktreePath != "" &&
		action != flow.ReviewHandoff
}

func runDoneCleanupWizard(ctx context.Context, app *App, p *prompter, final task.Task) error {
	rt := runtimeForTask(app, &final)
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
	closeUnknown := false
	if len(preview.UnknownSessions) > 0 {
		confirmed, confirmErr := p.confirm(fmt.Sprintf(
			"Close %d workspace(s) with unknown or empty agent status?", len(preview.UnknownSessions)), false)
		if errors.Is(confirmErr, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "   cleanup kept · unknown runtime status was not authorized")
			return nil
		}
		if confirmErr != nil {
			return confirmErr
		}
		closeUnknown = true
	}

	preview, err = retiredomain.InspectForExternalCoordinator(ctx, rt, final.WorktreePath, retiredomain.Options{
		CloseUnknown: closeUnknown,
	})
	if err != nil {
		app.warnf("retirement preview changed; cleanup was not attempted: %v", err)
		printRetireFallback(app, final, deleteBranch, closeUnknown)
		return nil
	}
	if !preview.Ready() {
		app.warnf("retirement is blocked: %s", strings.Join(preview.Blockers, "; "))
		printRetireFallback(app, final, deleteBranch, closeUnknown)
		return nil
	}
	if len(preview.Sessions) > 0 {
		confirmed, confirmErr := p.confirm(fmt.Sprintf(
			"Close %d eligible runtime workspace(s) and retire this task?", len(preview.Sessions)), false)
		if errors.Is(confirmErr, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "   cleanup kept · no runtime workspace was closed")
			return nil
		}
		if confirmErr != nil {
			return confirmErr
		}
	}

	if rt.Name() == "herdr" && preview.CallerContained && len(preview.Sessions) > 0 {
		return launchExternalRetireCoordinator(ctx, app, rt, final, preview, deleteBranch, closeUnknown)
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
		fmt.Fprintf(app.Out, "  workspace %s\n", label)
		for _, pane := range session.Panes {
			agent := pane.Agent
			if agent == "" {
				agent = "shell"
			}
			status := pane.AgentStatus
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(app.Out, "    %s  %s · %s · %s\n", pane.ID, agent, status, config.Contract(pane.CWD))
		}
		if len(session.Mixed) > 0 {
			fmt.Fprintf(app.Out, "    %s\n", s.warning(fmt.Sprintf("mixed workspace: %d pane(s) are outside the target", len(session.Mixed))))
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
) (err error) {
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
		Version: 1, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(retireHandoffTTL),
		TaskID: final.ID, TaskRevision: record.Revision, CheckoutPath: final.WorktreePath,
		HeadOID: strings.TrimSpace(head), PreviewFingerprint: preview.Fingerprint(),
		DeleteBranch: deleteBranch, CloseUnknown: closeUnknown,
	}
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
	if intent.Version != 1 || time.Now().UTC().After(intent.ExpiresAt) {
		return errors.New("retirement handoff expired or has an unsupported version")
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
	preview, err := retiredomain.InspectForExternalCoordinator(ctx, rt, selected.WorktreePath, retiredomain.Options{
		CloseUnknown: intent.CloseUnknown,
	})
	if err != nil {
		return err
	}
	if preview.Fingerprint() != intent.PreviewFingerprint {
		return errors.New("retirement handoff is stale: runtime workspace or agent state changed")
	}
	if !preview.Ready() {
		return fmt.Errorf("retirement blocked: %s", strings.Join(preview.Blockers, "; "))
	}
	return retireTaskWithTaskflow(ctx, app, &selected, flow.RetireOptions{
		CloseUnknown: intent.CloseUnknown, DeleteBranch: intent.DeleteBranch, Timeout: 5 * time.Second,
	}, intent.DeleteBranch)
}
