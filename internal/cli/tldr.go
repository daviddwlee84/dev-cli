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
	"dev repo": `TL;DR: acquire the checkout, then choose how ready it should be

  dev repo new/create --> scaffold --> optional upstream --> cd/open/start
  dev repo clone      --> checkout --> optional setup    --> cd/open/start
  dev repo setup      --> idempotent files/skills/hooks --> review or commit

  Local-only is the default. Project-owned executable config must be trusted
  by its exact content hash before dev runs it.`,

	"dev pr": `TL;DR: two surfaces, because they answer different questions

  --scope account --> gh search prs      --> whole account, 2 calls
                                             no branch, no review, no checks
  --scope local   --> gh pr list --repo  --> per engaged repo, 1 call each
                                             branch --> joins to a worktree
  --scope all     --> both, local upgrades account

  dev agent prompt render/run/open pr-triage hands the queue to the generic prompt family.
  Nothing here approves, merges, or removes anything.`,

	"dev agent prompt": `TL;DR: escalate only as far as the situation needs

  dev agent prompt render <recipe>  --> inspect or copy the exact prompt
  dev agent prompt run <recipe>     --> one-shot, no user stdin, bounded timeout
  dev agent prompt open <recipe>    --> foreground TTY, user can answer questions

  Recipes collect deterministic facts. Agents explain and prioritize them;
  done, park, sweep and retire remain the lifecycle authorities.`,

	"dev git worktree": `TL;DR: the checkout is disposable, the branch is not

  dev work start --> paths.worktree_path/<repo>/<branch>  (default mode)
                                  |
                                  +-- dev git worktree list       what exists, and how dirty
                                  +-- dev git worktree open       attach a runtime to one
                                  +-- dev git worktree provision  re-run installs after a pull
                                  |
  dev work done --ff --> MERGED --> dev work retire --> checkout gone, branch kept

  dev git worktree rm removes a checkout. It never removes a branch or its commits.`,

	"dev tries": `TL;DR: the experiment keeps its identity after the directory moves

  dev tries try <name> --> a dated scratch directory  (create or open)
                       |
                       +-- dev tries mark       tags and a note
                       +-- dev tries archive    hide it, keep the ID
                       +-- dev tries restore    bring it back into view
                       +-- dev tries graduate         promote it to a real project

  The catalog ID survives archive, restore and graduation. The path does not.`,

	"dev repo note": `TL;DR: thoughts that outlive the checkout

  dev repo note add --repo <repo> "..."  --> Markdown under the state dir  (durable)
                                      |
                                      +-- dev repo note list / show
                                      +-- dev repo note search    <- SQLite full text
                                      +-- dev repo note edit / delete

  Markdown is the truth. The search index is disposable: dev repo note reindex.`,

	"dev fleet": `TL;DR: read other machines without sharing their filesystem

  remotes.toml --> dev fleet status   can each host be reached?
                     |
                     +-- dev fleet list    repos, tasks and runtimes per host
                     +-- dev fleet sync    fast-forward clean matching checkouts
                     +-- dev fleet open    Herdr, or an SSH login shell

  Every host runs its own dev with its own config and its own paths. A host
  that is unreachable degrades to its cached snapshot; the fleet still answers.`,

	"dev ssh": `TL;DR: OpenSSH stays the source of truth

  dev ssh init --apply --> Include ~/.ssh/dev.d/*.conf
                                  |
  dev ssh setup <alias> ----------+--> public-key login --> optional --fleet
          |                       |
          +-- dev ssh list/show   +-- dev ssh probe
          +-- dev ssh remove      removes owned fragments, never keys

  dev ssh manage       select fleet / Herdr registration and profile actions
  dev ssh format       preview four-space indentation
  dev ssh organize     opt into Host-block/comment groups, preserving order
  dev ssh restore      preview recovery from a private receipt

  Listing is static. Explicit management/file actions are plans until --apply.
  Setup and probe may run OpenSSH; setup --dry-run never does.`,

	"dev agent skill": `TL;DR: what the agents on this machine already know

  dev agent skill list        current checkout and global native inventory
  dev agent skill list --all  every canonical repository, then global once
  dev agent skill add/update  explicit mutation through a direct skills executable

  dev's own bundled skill ships inside the binary and updates with dev itself,
  never through the provider.`,

	"dev agent mcp": `TL;DR: declared capabilities, not live connections

  dev agent mcp list         current checkout plus user/system declarations
  dev agent mcp list --all   every canonical repository, then user/system once
  dev agent mcp list --json  sanitized servers, diagnostics and coverage

  Static inventory never starts a server, runs a helper, resolves credentials,
  or claims that a declaration is connected, healthy or effective.`,

	"dev work retire": `TL;DR: integrate, exit, then clean up from outside

  dev work done --ff --> MERGED   (runtime and worktree deliberately kept)
                      |
    the agent exits,  |  dev agent artifact prepare arms its transcript to finalize later
    or another shell  v
  dev work retire <task> --> close runtime --> remove worktree --> keep the branch

  dev work retire refuses to run inside the workspace it would delete, and refuses
  a live agent, dirty state, or an unfinalized artifact.`,
}

// helpTopics links a command family to the quick-reference page behind it.
// Cobra help documents syntax; `dev help <topic>` documents when and why.
// Before this map neither one mentioned the other.
var helpTopics = map[string]string{
	"dev git worktree":            "worktrees",
	"dev repo":                    "repositories",
	"dev repo note":               "notes",
	"dev fleet":                   "fleet",
	"dev dotfile":                 "dotfile",
	"dev ssh":                     "ssh",
	"dev activity journal":        "journal",
	"dev summary":                 "summary",
	"dev work retire":             "retirement",
	"dev agent artifact":          "ai-artifacts",
	"dev specstory":               "ai-artifacts",
	"dev git hygiene":             "hygiene",
	"dev agent artifact finalize": "retirement",
	"dev agent artifact status":   "ai-artifacts",
	"dev agent artifact setup":    "ai-artifacts",
	"dev agent artifact archive":  "ai-artifacts",
	"dev agent artifact find":     "ai-artifacts",
	"dev agent artifact migrate":  "ai-artifacts",
	"dev agent artifact sync":     "ai-artifacts",
	"dev agent artifact backup":   "ai-artifacts",

	"dev agent artifact prepare": "retirement",
	"dev work done":              "retirement",
	"dev repo bootstrap":         "bootstrap",
	"dev work adopt":             "adopting",
	"dev work park":              "parking",
	"dev work resume":            "parking",
	"dev self cache":             "storage",
	"dev self config":            "storage",
	"dev tui":                    "tui",
	"dev work start":             "branching",
	"dev tries":                  "tries",
	"dev tries try":              "tries",
	"dev agent skill":            "skills",
	"dev agent mcp":              "mcp",
	"dev status":                 "git-status",
	"dev pr":                     "pull-requests",
	"dev agent prompt":           "prompts",
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
	path := canonicalCommandPath(cmd)
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
