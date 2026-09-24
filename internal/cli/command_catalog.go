package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const shortcutTargetAnnotation = "dev.shortcut-target"
const shortcutDetailAnnotation = "dev.shortcut-detail"

type commandShortcut struct {
	target  string
	factory func(*App) *cobra.Command
	detail  string
}

// Each route gets its own command and flag storage. Cobra commands have exactly
// one parent, and sharing a command between paths also shares parsed flag state.
var commandShortcuts = []commandShortcut{
	{"work list", newListCmd, ""},
	{"work start", newStartCmd, ""},
	{"work park", newParkCmd, ""},
	{"work resume", newResumeCmd, ""},
	{"work done", newDoneCmd, ""},
	{"work adopt", newAdoptCmd, ""},
	{"work retire", newRetireCmd, ""},
	{"work sweep", newSweepCmd, ""},
	{"repo bootstrap", newBootstrapCmd, ""},
	{"repo note", newNoteCmd, ""},
	{"repo flow", newFlowCmd, ""},
	{"repo browse", newRepoBrowseCmd, ""},
	{"tries try", newTryCmd, "create or open; no argument lists Tries"},
	{"tries graduate", newGraduateCmd, ""},
	{"git ignore", newGitignoreCmd, ""},
	{"git worktree", newWorktreeCmd, ""},
	{"git submodule", newSubmoduleCmd, ""},
	{"git hygiene", newHygieneCmd, ""},
	{"agent skill", newSkillCmd, ""},
	{"agent mcp", newMCPCmd, ""},
	{"agent instructions", newInstructionsCmd, ""},
	{"agent prompt", newPromptCmd, ""},
	{"agent artifact", newArtifactFamilyCmd, ""},
	{"agent artifact prepare", newPrepareCmd, ""},
	{"activity journal", newJournalCmd, ""},
	{"activity stats", newStatsCmd, ""},
	{"self config", newConfigCmd, ""},
	{"self config edit", newEditCmd, ""},
	{"self cache", newCacheCmd, ""},
	{"self doctor", newDoctorCmd, ""},
	{"self version", newVersionCmd, ""},
	{"self upgrade", newUpgradeCmd, ""},
	{"self completion", newCompletionCmd, ""},
	{"self shell-init", newShellInitCmd, ""},
	{"self feedback", newFeedbackCmd, ""},
	{"snippet", func(app *App) *cobra.Command { return newSnippetCmd(app, true) }, "GitHub only (--forge github)"},
}

func commandFamily(name, summary string, children ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{Use: name, Short: summary}
	cmd.AddCommand(children...)
	return cmd
}

func renameCommand(cmd *cobra.Command, name string) *cobra.Command {
	_, args, found := strings.Cut(cmd.Use, " ")
	cmd.Use = name
	if found {
		cmd.Use += " " + args
	}
	aliases := cmd.Aliases[:0]
	for _, alias := range cmd.Aliases {
		if alias != name {
			aliases = append(aliases, alias)
		}
	}
	cmd.Aliases = aliases
	return cmd
}

func newArtifactFamilyCmd(app *App) *cobra.Command {
	cmd := newArtifactCmd(app)
	cmd.AddCommand(newPrepareCmd(app))
	return cmd
}

func addCommandCatalog(root *cobra.Command, app *App) {
	work := commandFamily("work", "Start, pause, resume and finish tracked work",
		renameCommand(newListCmd(app), "list"), newStartCmd(app), newParkCmd(app),
		newResumeCmd(app), newDoneCmd(app), newAdoptCmd(app), newRetireCmd(app), newSweepCmd(app))
	// Keep the familiar short spelling available inside the family too.
	for _, child := range work.Commands() {
		if child.Name() == "list" {
			child.Aliases = []string{"ls"}
		}
	}
	repositories := newRepoCmd(app)
	repositories.AddCommand(newBootstrapCmd(app), newNoteCmd(app), newFlowCmd(app))
	tries := newTriesCmd(app)
	tries.AddCommand(newTryCmd(app))
	git := newGitCmd(app)
	// These services own their own guards. Do not inherit the transaction-only
	// wrapper: ignore --stdout/--list must also work outside a Git checkout.
	git.AddCommand(renameCommand(newGitignoreCmd(app), "ignore"),
		renameCommand(newWorktreeCmd(app), "worktree"), newSubmoduleCmd(app), newHygieneCmd(app))
	agent := commandFamily("agent", "Manage agent skills, MCP, instructions, prompts and history",
		newSkillCmd(app), newMCPCmd(app), newInstructionsCmd(app), newPromptCmd(app), newArtifactFamilyCmd(app))
	activity := commandFamily("activity", "Review development history and recorded activity", newJournalCmd(app), newStatsCmd(app))
	self := commandFamily("self", "Configure, diagnose and maintain dev itself",
		newConfigCmd(app), newCacheCmd(app), newDoctorCmd(app), newVersionCmd(app),
		newUpgradeCmd(app), newCompletionCmd(app), newShellInitCmd(app), newFeedbackCmd(app))
	root.AddCommand(work, repositories, tries, git, agent, activity, self,
		newSnippetCmd(app, false), newSSHCmd(app), newFleetCmd(app), newDotfileCmd(app),
		newPRCmd(app), newSummaryCmd(app), newTriageCmd(app), newStatusCmd(app), newTUICmd(app), newHelpTopicCmd(app),
		newRetireCoordinatorCmd(app))
	for _, route := range commandShortcuts {
		cmd := route.factory(app)
		cmd.Hidden = true
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[shortcutTargetAnnotation] = "dev " + route.target
		cmd.Annotations[shortcutDetailAnnotation] = route.detail
		root.AddCommand(cmd)
	}
	root.CompletionOptions.DisableDefaultCmd = true
}

// canonicalCommandPath also recognizes descendants of a compatibility family.
func canonicalCommandPath(cmd *cobra.Command) string {
	var suffix []string
	for current := cmd; current != nil; current = current.Parent() {
		if target := current.Annotations[shortcutTargetAnnotation]; target != "" {
			for i := len(suffix) - 1; i >= 0; i-- {
				target += " " + suffix[i]
			}
			return target
		}
		suffix = append(suffix, current.Name())
	}
	return cmd.CommandPath()
}

type commandAlias struct{ from, to, detail string }

// commandAliases is the shared inventory for help and the generated skill.
// Hidden protocol helpers are never aliases, even when Cobra gives them names.
func commandAliases(root *cobra.Command) []commandAlias {
	var result []commandAlias
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if target := cmd.Annotations[shortcutTargetAnnotation]; target != "" {
			detail := cmd.Annotations[shortcutDetailAnnotation]
			result = append(result, commandAlias{cmd.CommandPath(), target, detail})
			for _, alias := range cmd.Aliases {
				result = append(result, commandAlias{cmd.Parent().CommandPath() + " " + alias, target, detail})
			}
			return
		}
		if cmd.Hidden {
			return
		}
		if cmd.Parent() != nil {
			for _, alias := range cmd.Aliases {
				result = append(result, commandAlias{cmd.Parent().CommandPath() + " " + alias, cmd.CommandPath(), ""})
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	sort.Slice(result, func(i, j int) bool { return result[i].from < result[j].from })
	return result
}

func newCompletionCmd(app *App) *cobra.Command {
	cmd := commandFamily("completion", "Generate shell completion scripts")
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var noDescriptions bool
		child := &cobra.Command{Use: shell, Short: fmt.Sprintf("Generate the autocompletion script for %s", shell), Args: cobra.NoArgs,
			ValidArgsFunction: cobra.NoFileCompletions,
			RunE: func(c *cobra.Command, _ []string) error {
				switch shell {
				case "bash":
					return c.Root().GenBashCompletionV2(app.Out, !noDescriptions)
				case "zsh":
					if noDescriptions {
						return c.Root().GenZshCompletionNoDesc(app.Out)
					}
					return c.Root().GenZshCompletion(app.Out)
				case "fish":
					return c.Root().GenFishCompletion(app.Out, !noDescriptions)
				default:
					if noDescriptions {
						return c.Root().GenPowerShellCompletion(app.Out)
					}
					return c.Root().GenPowerShellCompletionWithDesc(app.Out)
				}
			}}
		child.Flags().BoolVar(&noDescriptions, "no-descriptions", false, "disable completion descriptions")
		cmd.AddCommand(child)
	}
	return cmd
}
