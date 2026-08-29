package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// familyTLDR holds one ASCII orientation block per command family, keyed by
// command path. Cobra help answers "what are the flags"; these answer "what is
// the shape of this workflow", which was previously only documented for the
// default task loop in workflowTLDR.
//
// ASCII only, like workflowTLDR: this text lands in terminals whose encoding
// dev does not control. assertASCIIDiagram enforces it.
var familyTLDR = map[string]string{
	"dev wt": `TL;DR: the checkout is disposable, the branch is not

  dev start --mode worktree --> paths.worktree_path/<repo>/<branch>
                                  |
                                  +-- dev wt list       what exists, and how dirty
                                  +-- dev wt open       attach a runtime to one
                                  +-- dev wt provision  re-run installs after a pull
                                  |
  dev done --ff --> MERGED --> dev retire --> checkout gone, branch kept

  dev wt rm removes a checkout. It never removes a branch or its commits.`,

	"dev tries": `TL;DR: the experiment keeps its identity after the directory moves

  dev try <name> --> a dated scratch directory  (create or open)
                       |
                       +-- dev tries mark       tags and a note
                       +-- dev tries archive    hide it, keep the ID
                       +-- dev tries restore    bring it back into view
                       +-- dev graduate         promote it to a real project

  The catalog ID survives archive, restore and graduation. The path does not.`,

	"dev note": `TL;DR: thoughts that outlive the checkout

  dev note add <repo> -m "..."  --> Markdown under the state dir  (durable)
                                      |
                                      +-- dev note list / show
                                      +-- dev note search    <- SQLite full text
                                      +-- dev note edit / delete

  Markdown is the truth. The search index is disposable: dev note reindex.`,

	"dev fleet": `TL;DR: read other machines without sharing their filesystem

  remotes.toml --> dev fleet status   can each host be reached?
                     |
                     +-- dev fleet list    repos, tasks and runtimes per host
                     +-- dev fleet sync    fast-forward clean matching checkouts
                     +-- dev fleet open    Herdr, or an SSH login shell

  Every host runs its own dev with its own config and its own paths. A host
  that is unreachable degrades to its cached snapshot; the fleet still answers.`,

	"dev skill": `TL;DR: what the agents on this machine already know

  dev skill list     project and global skills, their agents and update state
  dev skill add      install one through the external skills provider
  dev skill update   refresh a skill that provider manages

  dev's own bundled skill ships inside the binary and updates with dev itself,
  never through the provider.`,

	"dev retire": `TL;DR: integrate, exit, then clean up from outside

  dev done --ff --> MERGED   (runtime and worktree deliberately kept)
                      |
    the agent exits,  |  dev prepare arms its transcript to finalize later
    or another shell  v
  dev retire <task> --> close runtime --> remove worktree --> keep the branch

  dev retire refuses to run inside the workspace it would delete, and refuses
  a live agent, dirty state, or an unfinalized artifact.`,
}

// helpTopics links a command family to the quick-reference page behind it.
// Cobra help documents syntax; `dev help <topic>` documents when and why.
// Before this map neither one mentioned the other.
var helpTopics = map[string]string{
	"dev wt":        "worktrees",
	"dev note":      "notes",
	"dev fleet":     "fleet",
	"dev journal":   "journal",
	"dev summary":   "summary",
	"dev retire":    "retirement",
	"dev artifact":  "retirement",
	"dev prepare":   "retirement",
	"dev done":      "retirement",
	"dev bootstrap": "bootstrap",
	"dev adopt":     "adopting",
	"dev park":      "parking",
	"dev resume":    "parking",
	"dev cache":     "storage",
	"dev config":    "storage",
	"dev tui":       "tui",
	"dev start":     "branching",
	"dev tries":     "tries",
	"dev try":       "tries",
	"dev skill":     "skills",
	"dev status":    "git-status",
}

// topicForCommand resolves a bare command name or alias to its help topic, so
// `dev help wt` reaches the worktrees page instead of failing a filename match.
func topicForCommand(name string) (string, bool) {
	topic, ok := helpTopics["dev "+name]
	return topic, ok
}

// annotateHelp attaches the family diagram and the topic cross-reference to
// each command's Long text. Doing it in one pass over the assembled tree keeps
// the pointers from drifting out of sync with the map above.
func annotateHelp(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		annotateHelp(child)
	}
	path := cmd.CommandPath()
	long := cmd.Long
	if long == "" {
		long = cmd.Short
	}
	if diagram, ok := familyTLDR[path]; ok {
		long = strings.TrimRight(long, "\n") + "\n\n" + diagram
	}
	if topic, ok := helpTopics[path]; ok {
		long = strings.TrimRight(long, "\n") + fmt.Sprintf("\n\nSee also: dev help %s", topic)
	}
	if long != cmd.Long {
		cmd.Long = long
	}
}
