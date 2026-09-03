package cli

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func newDoneCmd(app *App) *cobra.Command {
	var (
		ff            bool
		pr            bool
		merged        bool
		deleteBranch  bool
		keepWorktree  bool
		push          bool
		dirtyPolicy   string
		message       string
		yes           bool
		baseRef       string
		confirmSquash string
	)
	cmd := &cobra.Command{
		Use:   "done [task]",
		Short: "Integrate a finished change stream without destroying its workspace",
		Long: `Finish integration while leaving runtime and filesystem cleanup to an
external coordinator.

  --ff       rebase onto the base and fast-forward it locally. Use when the
             branch's commits are worth keeping in the base's history.
  --pr       push and open a pull/merge request with the detected forge CLI,
             leaving the merge to review and CI.
  --merged   verify an externally merged branch against --base-ref, for a merge
             performed outside dev.

For branch/worktree tasks, omitting every mode opens a finish wizard on an
interactive terminal. A non-interactive caller still gets a report and must
pass a mode explicitly. Direct tasks need no integration mode.

A dirty checkout is analyzed against the base, not rejected as one opaque
condition. Interactive use can commit everything, discard everything, or
cancel. Unique content requires typing DROP before discard. Scripts select an
explicit --dirty policy; destructive discard also requires --yes.

A successful local or externally verified merge records DONE, meaning MERGED
with cleanup possibly pending. It never closes the invoking runtime, removes a
worktree, or deletes a branch. Run dev retire later from outside the workspace.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modes := 0
			for _, selected := range []bool{ff, pr, merged} {
				if selected {
					modes++
				}
			}
			if modes > 1 {
				return fmt.Errorf("--ff, --pr and --merged are alternatives; pick one")
			}
			if !merged && (baseRef != "" || confirmSquash != "") {
				return fmt.Errorf("--base-ref and --confirm-squash require --merged")
			}
			if deleteBranch {
				return fmt.Errorf("--delete-branch is cleanup; run dev retire --delete-branch after integration")
			}
			if keepWorktree {
				app.warnf("--keep-worktree is deprecated: dev done always keeps runtime and worktree state")
			}
			integration := doneIntegrationNone
			switch {
			case ff:
				integration = doneIntegrationFF
			case pr:
				integration = doneIntegrationPR
			case merged:
				integration = doneIntegrationMerged
			}
			return runDone(ctxOf(), app, args, doneOptions{
				Integration: integration, DirtyPolicy: doneDirtyPolicy(dirtyPolicy),
				Message: message, Yes: yes, Push: push,
				BaseRef: baseRef, ConfirmSquash: confirmSquash,
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&ff, "ff", false, "rebase onto the base and fast-forward it")
	f.BoolVar(&pr, "pr", false, "push and open a pull/merge request instead of merging locally")
	f.BoolVar(&merged, "merged", false, "verify an externally merged branch and mark the task done")
	f.StringVar(&baseRef, "base-ref", "", "base ref used to verify --merged (default: recorded base)")
	f.StringVar(&confirmSquash, "confirm-squash", "", "attest that this contained commit represents a squash merge")
	f.BoolVar(&push, "push", false, "push the resulting base (direct mode pushes its current branch)")
	f.StringVar(&dirtyPolicy, "dirty", string(doneDirtyAuto), "dirty checkout policy: auto, fail, commit or discard")
	f.StringVarP(&message, "message", "m", "", "commit message for --dirty=commit")
	f.BoolVarP(&yes, "yes", "y", false, "confirm the selected finish plan (required for non-interactive discard)")
	registerFlagCompletion(cmd, "dirty", fixedCompletions("auto", "fail", "commit", "discard"))
	// Both remain accepted so existing scripts fail loudly with a pointer to
	// dev retire rather than silently skipping cleanup they still expect.
	f.BoolVar(&keepWorktree, "keep-worktree", false, "deprecated: worktrees are always kept until dev retire")
	f.BoolVar(&deleteBranch, "delete-branch", false, "deprecated here: use dev retire --delete-branch")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
}
