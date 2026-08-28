package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/spf13/cobra"
)

func newGitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Guarded Git transactions that need receipts or recovery",
		Long: `Wrap only multi-step Git operations whose failure recovery is easy to get
wrong. Simple aliases such as git status or git add remain shell/plugin concerns.`,
	}
	cmd.AddCommand(
		newGitUncommitCmd(app), newGitRecommitCmd(app), newGitPullRebaseCmd(app),
		newGitAmendAllCmd(app), newGitSetupCmd(app),
	)
	return cmd
}

func newGitUncommitCmd(app *App) *cobra.Command {
	var rewritePublished bool
	cmd := &cobra.Command{
		Use:   "uncommit",
		Short: "Soft-reset one commit and save a message receipt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			receipt, err := gitx.Uncommit(ctxOf(), mustGetwd(), rewritePublished)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "UNCOMMITTED %s\n", shortOID(receipt.OldOID))
			fmt.Fprintf(app.Out, "   changes are staged; restore the full message with `dev git recommit`\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&rewritePublished, "rewrite-published", false, "acknowledge rewriting a commit already contained in the upstream")
	return cmd
}

func newGitRecommitCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "recommit",
		Short: "Commit staged changes with the saved uncommit message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			commit, err := gitx.Recommit(ctxOf(), mustGetwd())
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "RECOMMITTED %s\n", shortOID(commit))
			return nil
		},
	}
}

func newGitPullRebaseCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "pull-rebase",
		Short: "Pull with rebase while restoring one exact local-work stash",
		Long: `Preserve staged, unstaged and untracked work in a uniquely identified
stash, pull with rebase, then restore the exact stash with --index. Existing or
concurrent worktree stashes are never selected by position. Any pull or restore
conflict retains the stash for explicit recovery.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := gitx.PullRebase(ctxOf(), mustGetwd())
			if err != nil {
				return err
			}
			fmt.Fprintln(app.Out, "PULLED WITH REBASE")
			if result.HadLocalWork {
				fmt.Fprintf(app.Out, "   restored  %s", shortOID(result.StashOID))
				if result.Dropped {
					fmt.Fprintln(app.Out, " (exact stash dropped)")
				} else {
					fmt.Fprintln(app.Out, " (stash retained as a safety copy)")
				}
			}
			return nil
		},
	}
}

func newGitAmendAllCmd(app *App) *cobra.Command {
	var (
		rewritePublished bool
		excludeArtifacts bool
		allowUnscanned   bool
	)
	cmd := &cobra.Command{
		Use:   "amend-all",
		Short: "Stage every change and amend HEAD without editing its message",
		Long: `Run git add -A followed by git commit --amend --no-edit with normal hooks.

Agent artifacts are included by default. If they are present, dev requires a
project pre-commit/gitleaks configuration unless --allow-unscanned-artifacts is
explicit. Use --exclude-agent-artifacts to leave recognized transcripts, plans
and derived SpecStory statistics unstaged and print what was excluded.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := mustGetwd()
			if !excludeArtifacts && !allowUnscanned {
				paths, err := gitx.ChangedPaths(ctxOf(), dir)
				if err != nil {
					return err
				}
				for _, path := range paths {
					if gitx.IsAgentArtifact(path) && !projectHasArtifactScanner(dir) {
						return fmt.Errorf("agent artifacts would be amended but this repo has no .pre-commit-config.yaml or .gitleaks.toml; use --exclude-agent-artifacts or --allow-unscanned-artifacts")
					}
				}
			}
			commit, excluded, err := gitx.AmendAll(ctxOf(), dir, gitx.AmendOptions{
				RewritePublished: rewritePublished, ExcludeArtifacts: excludeArtifacts,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "AMENDED %s\n", shortOID(commit))
			for _, path := range excluded {
				fmt.Fprintf(app.Out, "   excluded  %s\n", path)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&rewritePublished, "rewrite-published", false, "acknowledge rewriting a commit already contained in the upstream")
	f.BoolVar(&excludeArtifacts, "exclude-agent-artifacts", false, "leave recognized agent artifacts unstaged")
	f.BoolVar(&allowUnscanned, "allow-unscanned-artifacts", false, "include agent artifacts without a detected project scanner")
	return cmd
}

func newGitSetupCmd(app *App) *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Print optional aliases for transactional dev git commands",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !printOnly {
				return fmt.Errorf("setup never edits Git config; pass --print to review the optional aliases")
			}
			fmt.Fprintln(app.Out, `# Optional only — dev intentionally does not replace ordinary Git aliases.
git config --global alias.dev-uncommit '!dev git uncommit'
git config --global alias.dev-recommit '!dev git recommit'
git config --global alias.dev-pull-rebase '!dev git pull-rebase'
git config --global alias.dev-amend-all '!dev git amend-all'`)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print reviewed setup commands without applying them")
	return cmd
}

func projectHasArtifactScanner(dir string) bool {
	repository, err := gitx.Discover(context.Background(), dir)
	if err != nil {
		return false
	}
	hookPath := filepath.Join(repository.GitCommonDir, "hooks", "pre-commit")
	if configured, err := gitx.Run(context.Background(), repository.Root, "config", "--path", "--get", "core.hooksPath"); err == nil && configured != "" {
		if filepath.IsAbs(configured) {
			hookPath = configured
		} else {
			hookPath = filepath.Join(repository.Root, configured)
		}
		hookPath = filepath.Join(hookPath, "pre-commit")
	}
	info, err := os.Stat(hookPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return false
	}
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		return false
	}
	hookText := strings.ToLower(string(hook))
	if _, lookupErr := exec.LookPath("gitleaks"); lookupErr == nil &&
		configContains(filepath.Join(repository.Root, ".gitleaks.toml"), "") && strings.Contains(hookText, "gitleaks") {
		return true
	}
	if _, lookupErr := exec.LookPath("pre-commit"); lookupErr == nil &&
		configContains(filepath.Join(repository.Root, ".pre-commit-config.yaml"), "gitleaks", "redact-agent-secrets") && strings.Contains(hookText, "pre-commit") {
		return true
	}
	return false
}

func configContains(path string, needles ...string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(needles) == 0 || len(needles) == 1 && needles[0] == "" {
		return true
	}
	text := strings.ToLower(string(content))
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
