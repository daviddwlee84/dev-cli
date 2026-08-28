// Package summary builds a current, machine-wide development snapshot. Unlike
// journal it has no time window: it describes what exists and needs attention
// now, without persisting another copy of derivable Git/runtime facts.
package summary

import (
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
)

const SchemaVersion = 1

type Detail string

const (
	DetailAuto    Detail = "auto"
	DetailCompact Detail = "compact"
	DetailFull    Detail = "full"
)

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Host          string       `json:"host"`
	Capabilities  Capabilities `json:"capabilities"`
	Totals        Totals       `json:"totals"`
	Projects      []Project    `json:"projects"`
	Warnings      []string     `json:"warnings,omitempty"`
}

type Capabilities struct {
	RuntimeCollected bool `json:"runtime_collected"`
	SizesCollected   bool `json:"sizes_collected"`
}

type Totals struct {
	Projects     int            `json:"projects"`
	Repositories int            `json:"repositories"`
	Tries        int            `json:"tries"`
	Checkouts    int            `json:"checkouts"`
	Worktrees    int            `json:"worktrees"`
	Dirty        int            `json:"dirty"`
	Conflicted   int            `json:"conflicted"`
	Live         int            `json:"live"`
	Attention    int            `json:"attention"`
	NoRemote     int            `json:"no_remote"`
	LocalOnly    int            `json:"local_only"`
	TasksByState map[string]int `json:"tasks_by_state"`
}

type Project struct {
	Kind             string           `json:"kind"`
	ID               string           `json:"id,omitempty"`
	Name             string           `json:"name"`
	Category         string           `json:"category,omitempty"`
	Path             string           `json:"path,omitempty"`
	DisplayPath      string           `json:"-"`
	Phase            string           `json:"phase,omitempty"`
	Present          bool             `json:"present"`
	Tags             []string         `json:"tags,omitempty"`
	Note             string           `json:"note,omitempty"`
	LatestActivity   time.Time        `json:"latest_activity,omitempty"`
	RecentCommits    []Commit         `json:"recent_commits,omitempty"`
	Git              *Git             `json:"git,omitempty"`
	Recovery         *Recovery        `json:"recovery,omitempty"`
	Checkouts        []Checkout       `json:"checkouts,omitempty"`
	Tasks            []Task           `json:"tasks,omitempty"`
	Sessions         []Session        `json:"sessions,omitempty"`
	Size             *diskusage.Usage `json:"size,omitempty"`
	SizeError        string           `json:"size_error,omitempty"`
	Active           bool             `json:"active"`
	ActiveReasons    []string         `json:"active_reasons,omitempty"`
	AttentionReasons []string         `json:"attention_reasons,omitempty"`
}

type Commit struct {
	OID        string    `json:"oid,omitempty"`
	ShortOID   string    `json:"short_oid,omitempty"`
	Subject    string    `json:"subject"`
	AuthoredAt time.Time `json:"authored_at,omitempty"`
	Source     string    `json:"source,omitempty"`
}

type Git struct {
	Branch     string `json:"branch,omitempty"`
	Detached   bool   `json:"detached"`
	Dirty      bool   `json:"dirty"`
	Conflicted int    `json:"conflicted"`
	Changed    int    `json:"changed"`
	Staged     int    `json:"staged"`
	Unstaged   int    `json:"unstaged"`
	Untracked  int    `json:"untracked"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	Upstream   string `json:"upstream,omitempty"`
	Summary    string `json:"summary"`
}

type Recovery struct {
	Remotes           []string `json:"remotes,omitempty"`
	LocalOnlyBranches []string `json:"local_only_branches,omitempty"`
	UpstreamRemotes   []string `json:"upstream_remotes,omitempty"`
	NoRemote          bool     `json:"no_remote"`
	MultipleRemotes   bool     `json:"multiple_remotes"`
	MultipleUpstreams bool     `json:"multiple_upstreams"`
	Error             string   `json:"error,omitempty"`
}

type Checkout struct {
	Path        string  `json:"path"`
	DisplayPath string  `json:"-"`
	Branch      string  `json:"branch,omitempty"`
	Ownership   string  `json:"ownership"`
	Exists      bool    `json:"exists"`
	Prunable    bool    `json:"prunable"`
	Locked      bool    `json:"locked"`
	Git         *Git    `json:"git,omitempty"`
	LastCommit  *Commit `json:"last_commit,omitempty"`
}

type Task struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	State  string   `json:"state"`
	Branch string   `json:"branch"`
	Next   string   `json:"next,omitempty"`
	Note   string   `json:"note,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

type Session struct {
	Runtime       string   `json:"runtime"`
	Handle        string   `json:"handle,omitempty"`
	Status        string   `json:"status,omitempty"`
	AgentSessions []string `json:"agent_sessions,omitempty"`
}

type Options struct {
	Query     string
	Attention bool
}

func Build(host string, projects []Project, capabilities Capabilities, warnings []string, opts Options) Report {
	report := Report{
		SchemaVersion: SchemaVersion, GeneratedAt: time.Now(), Host: host,
		Capabilities: capabilities, Warnings: warnings,
		Totals: Totals{TasksByState: map[string]int{}},
	}
	for i := range projects {
		classify(&projects[i])
		if !matches(projects[i], opts.Query) || (opts.Attention && len(projects[i].AttentionReasons) == 0) {
			continue
		}
		report.Projects = append(report.Projects, projects[i])
	}
	sort.SliceStable(report.Projects, func(i, j int) bool {
		a, b := report.Projects[i], report.Projects[j]
		if a.Active != b.Active {
			return a.Active
		}
		if ar, br := projectRank(a), projectRank(b); ar != br {
			return ar < br
		}
		if !a.LatestActivity.Equal(b.LatestActivity) {
			return a.LatestActivity.After(b.LatestActivity)
		}
		if a.Category != b.Category {
			return strings.ToLower(a.Category) < strings.ToLower(b.Category)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for _, project := range report.Projects {
		addTotals(&report.Totals, project)
	}
	return report
}

func projectRank(project Project) int {
	for rank, reason := range []string{"conflicted", "live", "hot-task", "warm-task", "dirty"} {
		if hasReason(project.ActiveReasons, reason) {
			return rank
		}
	}
	return 5
}

func classify(project *Project) {
	active := map[string]bool{}
	attention := map[string]bool{}
	add := func(reason string, isActive bool) {
		attention[reason] = true
		if isActive {
			active[reason] = true
		}
	}
	if len(project.Sessions) > 0 {
		add("live", true)
	}
	for _, task := range project.Tasks {
		switch task.State {
		case "hot":
			add("hot-task", true)
		case "warm":
			add("warm-task", true)
		}
	}
	for _, checkout := range project.Checkouts {
		if !checkout.Exists {
			add("missing-checkout", false)
		}
		if checkout.Prunable {
			add("prunable-checkout", false)
		}
		if checkout.Git != nil {
			if checkout.Git.Dirty {
				add("dirty", true)
			}
			if checkout.Git.Conflicted > 0 {
				add("conflicted", true)
			}
		}
	}
	if project.Git != nil {
		if project.Git.Dirty {
			add("dirty", true)
		}
		if project.Git.Conflicted > 0 {
			add("conflicted", true)
		}
	}
	if project.Recovery != nil {
		if project.Recovery.Error != "" {
			add("topology-error", false)
		}
		if project.Recovery.NoRemote {
			add("no-remote", false)
		}
		if len(project.Recovery.LocalOnlyBranches) > 0 {
			add("local-only-branches", false)
		}
	}
	project.ActiveReasons = sortedKeys(active)
	project.AttentionReasons = sortedKeys(attention)
	project.Active = len(project.ActiveReasons) > 0
}

func addTotals(t *Totals, project Project) {
	t.Projects++
	if project.Kind == "try" {
		t.Tries++
	} else {
		t.Repositories++
	}
	t.Checkouts += len(project.Checkouts)
	if len(project.Checkouts) > 1 {
		t.Worktrees += len(project.Checkouts) - 1
	}
	if project.Active {
		t.Live += boolInt(hasReason(project.ActiveReasons, "live"))
	}
	if len(project.AttentionReasons) > 0 {
		t.Attention++
	}
	if hasReason(project.ActiveReasons, "dirty") {
		t.Dirty++
	}
	if hasReason(project.ActiveReasons, "conflicted") {
		t.Conflicted++
	}
	if project.Recovery != nil {
		t.NoRemote += boolInt(project.Recovery.NoRemote)
		t.LocalOnly += boolInt(len(project.Recovery.LocalOnlyBranches) > 0)
	}
	for _, task := range project.Tasks {
		t.TasksByState[task.State]++
	}
}

func matches(project Project, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	values := []string{project.Kind, project.ID, project.Name, project.Category, project.Path,
		project.Phase, project.Note, strings.Join(project.Tags, " ")}
	if project.Git != nil {
		values = append(values, project.Git.Branch, project.Git.Upstream)
	}
	for _, task := range project.Tasks {
		values = append(values, task.ID, task.Title, task.State, task.Branch, task.Next, task.Note, strings.Join(task.Tags, " "))
	}
	for _, checkout := range project.Checkouts {
		values = append(values, checkout.Path, checkout.Branch, checkout.Ownership)
	}
	if project.Recovery != nil {
		values = append(values, strings.Join(project.Recovery.Remotes, " "),
			strings.Join(project.Recovery.LocalOnlyBranches, " "))
	}
	haystack := strings.ToLower(strings.Join(values, " "))
	for _, term := range strings.Fields(query) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func hasReason(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
