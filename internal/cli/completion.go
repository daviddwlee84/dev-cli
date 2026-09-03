package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/help"
	"github.com/daviddwlee84/dev-cli/internal/ignore"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

const noFileCompletion = cobra.ShellCompDirectiveNoFileComp

var completionDescriptionSanitizer = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

// completionInvocation reports commands that build or serve completion data.
// Neither path should run the normal eager App.Load: script generation needs no
// config, while __complete must wait until Cobra has parsed the target command's
// persistent flags (notably --config).
func completionInvocation(cmd *cobra.Command) bool {
	if cmd.Name() == cobra.ShellCompRequestCmd || cmd.CalledAs() == cobra.ShellCompNoDescRequestCmd {
		return true
	}
	for cur := cmd; cur != nil; cur = cur.Parent() {
		if cur.Name() == "completion" {
			return true
		}
	}
	return false
}

// loadCompletion prepares local providers after Cobra has parsed the command
// line. Invalid config should remove dynamic candidates, not break command and
// flag completion entirely. Cobra invokes one custom provider per request, so
// loading here avoids state leaking when a command tree is reused.
func (a *App) loadCompletion() bool { return a.Load() == nil }

func completePromptAgents(app *App, mode promptMode) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if !app.loadCompletion() {
			return nil, noFileCompletion
		}
		handoffMode, ok := promptHandoffMode(mode)
		if !ok {
			return nil, noFileCompletion
		}
		agents := append([]config.Agent(nil), app.Cfg.Agents...)
		sort.Slice(agents, func(i, j int) bool {
			left, right := strings.ToLower(agents[i].Name), strings.ToLower(agents[j].Name)
			if left == right {
				return agents[i].Name < agents[j].Name
			}
			return left < right
		})
		out := make([]string, 0, len(agents))
		for _, agent := range agents {
			if !promptAgentLauncher(agent, handoffMode).Configured() {
				continue
			}
			parts := make([]string, 0, 3)
			if agent.Default {
				parts = append(parts, "default")
			}
			parts = append(parts, string(handoffMode))
			if description := strings.TrimSpace(agent.Description); description != "" {
				parts = append(parts, description)
			}
			out = addCompletion(out, agent.Name, strings.Join(parts, " · "), toComplete)
		}
		return out, noFileCompletion
	}
}

func completeTasks(app *App, states ...task.State) cobra.CompletionFunc {
	allowed := make(map[task.State]bool, len(states))
	for _, state := range states {
		allowed[state] = true
	}
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 || !app.loadCompletion() {
			return nil, noFileCompletion
		}
		tasks, err := app.Tasks.List()
		if err != nil {
			return nil, noFileCompletion
		}
		out := make([]string, 0, len(tasks))
		for _, t := range tasks {
			if !allowed[t.State] {
				continue
			}
			value := t.ID
			if toComplete != "" && !strings.HasPrefix(value, toComplete) {
				for _, ref := range []string{t.Title(), t.Branch} {
					if candidate, ok := taskCompletionRef(tasks, t, ref, toComplete); ok {
						value = candidate
						break
					}
				}
			}
			description := fmt.Sprintf("%s · %s · %s · %s · %s", t.State, t.Title(), t.Repo, t.Branch, t.ID)
			out = addCompletion(out, value, description, toComplete)
		}
		return out, noFileCompletion
	}
}

func taskCompletionRef(all []*task.Task, target *task.Task, ref, typed string) (string, bool) {
	if len(typed) > len(ref) || !strings.EqualFold(ref[:len(typed)], typed) {
		return "", false
	}
	candidate := typed + ref[len(typed):]
	var exact []*task.Task
	for _, t := range all {
		if t.ID == candidate || t.Branch == candidate {
			exact = append(exact, t)
		}
	}
	if len(exact) > 0 {
		return candidate, len(exact) == 1 && exact[0].ID == target.ID
	}
	needle := strings.ToLower(candidate)
	var match *task.Task
	for _, t := range all {
		haystack := strings.ToLower(t.ID + " " + t.Name + " " + t.Branch + " " + t.Repo)
		if !strings.Contains(haystack, needle) {
			continue
		}
		if match != nil {
			return "", false
		}
		match = t
	}
	return candidate, match != nil && match.ID == target.ID
}

// completeRepos supplies positional repository references. Commands resolve a
// unique display name, while duplicate displays use their absolute path so the
// selected token still identifies exactly one checkout.
func completeRepos(app *App) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, noFileCompletion
		}
		return repositoryCompletions(app, toComplete, false)
	}
}

// completeRepoFlag is the same domain without a positional-argument guard: a
// flag may legally appear after another argument, e.g. wt open branch --repo.
func completeRepoFlag(app *App) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return repositoryCompletions(app, toComplete, false)
	}
}

// completeRepoNameFlag returns the name stored in tasks and stats, not the
// Category/Name display reference used by repository commands.
func completeRepoNameFlag(app *App) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return repositoryCompletions(app, toComplete, true)
	}
}

func completeTaskRepoNameFlag(app *App) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if !app.loadCompletion() {
			return nil, noFileCompletion
		}
		tasks, err := app.Tasks.List()
		if err != nil {
			return nil, noFileCompletion
		}
		seen := make(map[string]bool, len(tasks))
		var out []string
		for _, t := range tasks {
			if !seen[t.Repo] {
				seen[t.Repo] = true
				out = addCompletion(out, t.Repo, "tracked task repository", toComplete)
			}
		}
		return out, noFileCompletion
	}
}

func repositoryCompletions(app *App, toComplete string, namesOnly bool) ([]string, cobra.ShellCompDirective) {
	if !app.loadCompletion() {
		return nil, noFileCompletion
	}
	repos, err := repo.Discover(ctxOf(), app.Cfg.DiscoveryRoots(), repo.CompletionOptions())
	if err != nil {
		return nil, noFileCompletion
	}
	displayCount := make(map[string]int, len(repos))
	for _, r := range repos {
		displayCount[r.Display()]++
	}
	seen := make(map[string]bool, len(repos))
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		value := r.Display()
		description := config.Contract(r.Path)
		switch {
		case namesOnly:
			value = r.Name
			description = r.Display() + " · " + description
		case displayCount[value] > 1:
			value = r.Path
			description = r.Display() + " · " + description
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = addCompletion(out, value, description, toComplete)
	}
	return out, noFileCompletion
}

func completeWorktrees(app *App, includeMain bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 || !app.loadCompletion() {
			return nil, noFileCompletion
		}
		repoRef, _ := cmd.Flags().GetString("repo")
		var repoPath string
		if repoRef == "" {
			var err error
			repoPath, _, err = repoContext(app, "")
			if err != nil {
				return nil, noFileCompletion
			}
		} else {
			r, _, err := repo.ResolveCompletion(ctxOf(), app.Cfg.DiscoveryRoots(), repoRef)
			if err != nil {
				return nil, noFileCompletion
			}
			repoPath = r.Path
		}
		worktrees, err := gitx.Worktrees(ctxOf(), repoPath)
		if err != nil {
			return nil, noFileCompletion
		}
		out := make([]string, 0, len(worktrees))
		for _, wt := range worktrees {
			if wt.Branch == "" || wt.Detached || wt.Bare || (!includeMain && wt.Main) {
				continue
			}
			description := config.Contract(wt.Path)
			if wt.Main {
				description += " (main checkout)"
			}
			out = addCompletion(out, wt.Branch, description, toComplete)
		}
		return out, noFileCompletion
	}
}

func completeHelpTopics(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFileCompletion
	}
	topics, err := help.List()
	if err != nil {
		return nil, noFileCompletion
	}
	out := make([]string, 0, len(topics))
	for _, topic := range topics {
		out = addCompletion(out, topic.Name, topic.Summary, toComplete)
	}
	return out, noFileCompletion
}

func completeGitignoreNames(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	used := make(map[string]bool, len(args))
	for _, name := range args {
		used[strings.ToLower(name)] = true
	}
	names := ignore.BundledNames()
	out := make([]string, 0, len(names))
	for _, name := range names {
		value := strings.ToLower(name)
		if !used[value] {
			out = addCompletion(out, value, name+" bundled template", toComplete)
		}
	}
	return out, noFileCompletion
}

func fixedCompletions(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = addCompletion(out, value, "", toComplete)
		}
		return out, noFileCompletion
	}
}

func commaSeparatedCompletions(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix, current := "", toComplete
		if i := strings.LastIndexByte(toComplete, ','); i >= 0 {
			prefix, current = toComplete[:i+1], toComplete[i+1:]
		}
		used := make(map[string]bool)
		for _, value := range strings.Split(strings.TrimSuffix(prefix, ","), ",") {
			used[value] = value != ""
		}
		out := make([]string, 0, len(values))
		for _, value := range values {
			candidate := prefix + value
			if !used[value] && strings.HasPrefix(value, current) {
				out = append(out, candidate)
			}
		}
		return out, noFileCompletion
	}
}

func runtimeCompletions() cobra.CompletionFunc {
	backends := runtime.All()
	values := make([]string, 1, len(backends)+1)
	values[0] = "auto"
	for _, backend := range backends {
		values = append(values, backend.Name())
	}
	return fixedCompletions(values...)
}

func taskStateNames() []string {
	values := make([]string, len(task.States))
	for i, state := range task.States {
		values[i] = string(state)
	}
	return values
}

func taskStateCompletions() cobra.CompletionFunc {
	return fixedCompletions(taskStateNames()...)
}

func taskStateSliceCompletions() cobra.CompletionFunc {
	return commaSeparatedCompletions(taskStateNames()...)
}

func statsSourceCompletions() cobra.CompletionFunc {
	return commaSeparatedCompletions(
		string(stats.SourceSession), string(stats.SourceGit), string(stats.SourceWakaTime),
	)
}

func registerFlagCompletion(cmd *cobra.Command, name string, fn cobra.CompletionFunc) {
	if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
		panic(err)
	}
}

func addCompletion(out []string, value, description, toComplete string) []string {
	if toComplete != "" && !strings.HasPrefix(value, toComplete) {
		return out
	}
	if description != "" {
		description = completionDescriptionSanitizer.Replace(description)
		value = cobra.CompletionWithDesc(value, description)
	}
	return append(out, value)
}
