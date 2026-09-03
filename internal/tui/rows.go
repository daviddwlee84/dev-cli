package tui

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/textmatch"
)

// RepoRow is one repository as the dashboard shows it: what it is, plus how
// much of it is currently in flight.
//
// The repository view exists because the task list alone answers only half the
// question. On a machine with forty repositories and no tasks recorded yet,
// a task-only dashboard is empty — and the honest answer to "what do I have
// here?" has to come before "what am I working on?"
// RepoActions groups catalog metadata mutations for ordinary repositories.
type RepoActions struct {
	Patch func(ctx context.Context, row RepoRow, tags []string, note string) (string, error)
}

type RepoRow struct {
	Repo        repo.Repo
	Status      gitx.Status
	Topology    gitx.RecoveryTopology
	TopologyErr error
	SizeTarget  diskusage.Target
	Usage       *diskusage.Usage
	SizeError   error
	// Asset is catalog metadata joined without persisting an otherwise
	// unobserved repository. A Try asset lets callers suppress or label it.
	Asset *catalog.Entry
	// Context is the canonical checkout plus every linked worktree, enriched
	// with task and runtime state. It is shared by the tree, copy actions and
	// `dev repo context`, so those surfaces cannot disagree.
	Context inventory.RepoContext
	// LastActivity is the newest commit time in this checkout. It is a durable,
	// cheap approximation of "when was this repo last touched" and is sortable.
	LastActivity time.Time
	// Worktrees is the number of linked worktrees, excluding the main checkout.
	Worktrees int
	// Tasks are the recorded tasks belonging to this repository.
	Tasks []*task.Task
	// NoteCount/LatestNote are joined from the durable sidecar store when this
	// repo already has a catalog identity.
	NoteCount  int
	LatestNote *note.Note
	// Live reports a runtime session sitting in this repository.
	Live          bool
	Runtime       string
	RuntimeHandle string
	RuntimeStatus string
	// RemoteForge / RemoteName identify origin for matching the REMOTE view to
	// this local checkout without rescanning every repo when a cache opens.
	RemoteForge forge.Kind
	RemoteName  string
}

// Sessions returns every runtime session attached to any checkout in this
// repository.
func (r RepoRow) Sessions() []runtime.Session { return r.Context.Sessions() }

// HotTasks counts the repository's tasks that are currently hot.
// IsTry reports an ungraduated Try that should live in the dedicated TRY view,
// while remaining in the model's local snapshot for REMOTE matching.
func (r RepoRow) IsTry() bool {
	return r.Asset != nil && r.Asset.Kind == catalog.KindTry && r.Asset.Experiment != nil &&
		(r.Asset.Experiment.Phase == catalog.PhaseActive || r.Asset.Experiment.Phase == catalog.PhaseDeprecated)
}

func (r RepoRow) HotTasks() int {
	n := 0
	for _, t := range r.Tasks {
		if t.State == task.Hot {
			n++
		}
	}
	return n
}

// StateSummary renders the repository's tasks as a compact per-state tally,
// which is what makes the list scannable at forty repositories.
func (r RepoRow) StateSummary() string {
	if len(r.Tasks) == 0 {
		return ""
	}
	counts := map[task.State]int{}
	for _, t := range r.Tasks {
		counts[t.State]++
	}
	var parts []string
	for _, s := range task.States {
		if counts[s] > 0 {
			parts = append(parts, s.Icon()+strconv.Itoa(counts[s]))
		}
	}
	return strings.Join(parts, " ")
}

// searchText is what a filter query matches against.
func (r RepoRow) searchText() string {
	var b strings.Builder
	b.WriteString(r.Repo.Display())
	b.WriteString(" ")
	b.WriteString(r.Status.Branch)
	b.WriteString(" ")
	b.WriteString(r.Topology.Summary())
	for _, branch := range r.Topology.Branches {
		b.WriteString(" ")
		b.WriteString(branch.Branch)
		b.WriteString(" ")
		b.WriteString(branch.Upstream)
	}
	if r.Asset != nil {
		b.WriteString(" ")
		b.WriteString(string(r.Asset.Kind))
		b.WriteString(" ")
		b.WriteString(r.Asset.Note)
		b.WriteString(" ")
		b.WriteString(strings.Join(r.Asset.Tags, " "))
	}
	for _, t := range r.Tasks {
		b.WriteString(" ")
		b.WriteString(t.Title())
		b.WriteString(" ")
		b.WriteString(t.Branch)
	}
	for _, checkout := range r.Context.Checkouts {
		b.WriteString(" ")
		b.WriteString(checkout.Worktree.Path)
		b.WriteString(" ")
		b.WriteString(checkout.Branch())
		b.WriteString(" ")
		b.WriteString(string(checkout.Ownership))
	}
	return strings.ToLower(b.String())
}

func (r RepoRow) matches(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, term := range strings.Fields(query) {
		key, value, structured := strings.Cut(term, ":")
		if structured {
			switch key {
			case "tag":
				if r.Asset == nil || !r.Asset.HasTag(value) {
					return false
				}
				continue
			case "remote":
				if !strings.Contains(strings.ToLower(r.Topology.Summary()), value) {
					return false
				}
				continue
			case "size":
				if !matchesSize(r.Usage, value) {
					return false
				}
				continue
			case "kind":
				kind := catalog.KindRepository
				if r.Asset != nil {
					kind = r.Asset.Kind
				}
				if string(kind) != value {
					return false
				}
				continue
			}
		}
		if !strings.Contains(r.searchText(), term) {
			return false
		}
	}
	return true
}

// taskSearchText is what a filter query matches a task row against.
func taskSearchText(r inventory.Row) string {
	t := r.Task
	return strings.ToLower(strings.Join([]string{
		t.Title(), t.Repo, t.Branch, t.Next, t.Note, string(t.State),
	}, " "))
}

// matches reports whether every whitespace-separated term of the query appears
// in the haystack.
//
// Term-wise rather than substring so "auth api" finds a task named "api token
// auth" — the order words come to mind in is rarely the order they appear in.
func matches(haystack, query string) bool {
	return textmatch.Terms(haystack, query)
}

// RemoteRow is one repository known to a configured forge, optionally matched to
// a local checkout. The REMOTE view is the bridge between "what exists on the
// forge" and "what is already on this machine".
type RemoteRow struct {
	Repo forge.RemoteRepo `json:"repo"`
	// LocalPath is the checkout under the configured scan roots, empty when the
	// remote has not been cloned here.
	LocalPath string `json:"local_path,omitempty"`
	// LocalName is the discovered repo's display name.
	LocalName string `json:"local_name,omitempty"`
	// LocalKind distinguishes a cataloged Try from an ordinary repository.
	LocalKind catalog.Kind `json:"local_kind,omitempty"`
}

// FleetRow is one host/repository observation. Repository is nil for a host
// health row such as unreachable or no-dev.
type FleetRow struct {
	Host       string
	Local      bool
	State      fleet.HostState
	Repository *fleet.RepoSnapshot
	Error      string
	FromCache  bool
}

func (r FleetRow) searchText() string {
	parts := []string{r.Host, string(r.State), r.Error}
	if r.Repository != nil {
		parts = append(parts, r.Repository.Name, r.Repository.Display, r.Repository.Path,
			r.Repository.Branch, r.Repository.Status.Summary(), strings.Join(r.Repository.RemoteIdentities, " "))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// Cloned reports whether this remote already has a local checkout.
func (r RemoteRow) Cloned() bool { return r.LocalPath != "" }

func (r RemoteRow) searchText() string {
	return strings.ToLower(strings.Join([]string{
		string(r.Repo.Forge), r.Repo.Name, r.Repo.FullName, r.Repo.Description,
		r.Repo.Visibility, r.Repo.DefaultBranch, r.LocalName, string(r.LocalKind),
	}, " "))
}

func (r RemoteRow) matches(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, term := range strings.Fields(query) {
		key, value, structured := strings.Cut(term, ":")
		if structured && (key == "vis" || key == "visibility") {
			if strings.ToLower(r.Repo.Visibility) != value {
				return false
			}
			continue
		}
		if !strings.Contains(r.searchText(), term) {
			return false
		}
	}
	return true
}

// StatsPanel is the repo activity overlay opened by H.
type StatsPanel struct {
	Repo       string
	Heatmap    string
	Seconds    int
	ActiveDays int
	Since      time.Time
	Until      time.Time
}

// NoteTarget identifies the selected local repository. CatalogID may be empty
// until the first note action persists the repository observation.
type NoteTarget struct {
	CatalogID string
	Repo      repo.Repo
}

func (t NoteTarget) Name() string { return t.Repo.Name }
func (t NoteTarget) Path() string { return t.Repo.Path }

// Key identifies the target while an asynchronous load is in flight.
func (t NoteTarget) Key() string {
	for _, value := range []string{t.CatalogID, t.Repo.CommonDir, t.Repo.RealPath, t.Repo.Path} {
		if value != "" {
			return value
		}
	}
	return t.Repo.Name
}

// NoteEdit suspends the TUI in an editor and atomically completes from its
// temporary body after the process exits.
type NoteEdit struct {
	Command *exec.Cmd
	// Complete always runs, even when the editor fails, so temporary files are
	// cleaned. It may replace runErr with validation/index errors.
	Complete func(runErr error) error
}

// NoteActions are the sidecar-note operations available to the dashboard.
type NoteActions struct {
	List   func(ctx context.Context, target NoteTarget) ([]*note.Note, error)
	Search func(ctx context.Context, target NoteTarget, query string) ([]*note.Note, error)
	Add    func(ctx context.Context, target NoteTarget, body string) (string, error)
	Delete func(ctx context.Context, n *note.Note) (string, error)
	Edit   func(n *note.Note) (NoteEdit, error)
}
