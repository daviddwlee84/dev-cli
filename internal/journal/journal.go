// Package journal derives a human- and agent-readable development journal
// from Git, durable activity observations and current checkout context.
package journal

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

const SchemaVersion = 1

type Granularity string

const (
	GranularityAuto   Granularity = "auto"
	GranularityRepo   Granularity = "repo"
	GranularityBranch Granularity = "branch"
	GranularityCommit Granularity = "commit"
)

type Options struct {
	Since, Until  time.Time // half-open [Since, Until)
	Authors       []string
	AllAuthors    bool
	Granularity   Granularity
	MaxCommits    int // zero means unlimited
	Metrics       bool
	IncludeMerges bool
	LocalContext  bool
	StatsPath     string
	Tasks         []*task.Task
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Window        Window       `json:"window"`
	Authors       []string     `json:"authors,omitempty"`
	AllAuthors    bool         `json:"all_authors"`
	Granularity   Granularity  `json:"granularity"`
	Summary       Summary      `json:"summary"`
	Repositories  []Repository `json:"repositories"`
	Warnings      []string     `json:"warnings,omitempty"`
}

type Window struct {
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Timezone string    `json:"timezone"`
}

type Summary struct {
	Repositories   int     `json:"repositories"`
	Branches       int     `json:"branches"`
	Commits        int     `json:"commits"`
	ShownCommits   int     `json:"shown_commits"`
	OmittedCommits int     `json:"omitted_commits"`
	Metrics        Metrics `json:"metrics,omitempty"`
}

type Repository struct {
	Name           string     `json:"name"`
	DisplayName    string     `json:"display_name"`
	Path           string     `json:"path"`
	LatestActivity time.Time  `json:"latest_activity,omitempty"`
	CommitCount    int        `json:"commit_count"`
	Metrics        Metrics    `json:"metrics,omitempty"`
	Activity       []Activity `json:"activity,omitempty"`
	Branches       []Branch   `json:"branches,omitempty"`
}

type Branch struct {
	Name           string        `json:"name"`
	Attribution    string        `json:"attribution,omitempty"`
	LatestActivity time.Time     `json:"latest_activity,omitempty"`
	CommitCount    int           `json:"commit_count"`
	ShownCommits   int           `json:"shown_commits"`
	OmittedCommits int           `json:"omitted_commits"`
	Metrics        Metrics       `json:"metrics,omitempty"`
	Task           *TaskContext  `json:"task,omitempty"`
	Current        []CurrentWork `json:"current,omitempty"`
	Commits        []Commit      `json:"commits,omitempty"`
}

type Commit struct {
	OID         string    `json:"oid"`
	ShortOID    string    `json:"short_oid"`
	AuthoredAt  time.Time `json:"authored_at"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	Subject     string    `json:"subject"`
	Metrics     Metrics   `json:"metrics,omitempty"`
}

type Metrics struct {
	Files     int `json:"files,omitempty"`
	Additions int `json:"additions,omitempty"`
	Deletions int `json:"deletions,omitempty"`
	Churn     int `json:"churn,omitempty"`
}

type Activity struct {
	Branch  string       `json:"branch,omitempty"`
	Source  stats.Source `json:"source"`
	Seconds int          `json:"seconds"`
}

type TaskContext struct {
	Title string     `json:"title"`
	State task.State `json:"state"`
	Next  string     `json:"next,omitempty"`
	Note  string     `json:"note,omitempty"`
}

type CurrentWork struct {
	Path         string    `json:"path"`
	Branch       string    `json:"branch,omitempty"`
	LatestChange time.Time `json:"latest_change,omitempty"`
	Changed      int       `json:"changed"`
	Staged       int       `json:"staged"`
	Unstaged     int       `json:"unstaged"`
	Untracked    int       `json:"untracked"`
}

type commitRef struct {
	repoIndex, branchIndex, commitIndex int
	when                                time.Time
}

func Collect(ctx context.Context, repos []repo.Repo, opts Options) (Report, error) {
	if opts.Granularity == "" {
		opts.Granularity = GranularityAuto
	}
	report := Report{
		SchemaVersion: SchemaVersion, GeneratedAt: time.Now(), Authors: opts.Authors,
		AllAuthors: opts.AllAuthors, Granularity: opts.Granularity,
		Window: Window{Since: opts.Since, Until: opts.Until, Timezone: opts.Since.Location().String()},
	}
	activity := loadActivity(opts, &report)

	for _, r := range repos {
		if !r.HasGit {
			continue
		}
		emails := opts.Authors
		if !opts.AllAuthors && len(emails) == 0 {
			email, err := gitx.Run(ctx, r.Path, "config", "user.email")
			if err != nil || strings.TrimSpace(email) == "" {
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s: no effective git user.email", r.Display()))
				continue
			}
			emails = []string{strings.TrimSpace(email)}
		}
		repoReport, err := collectRepo(ctx, r, emails, opts, activity[r.Name])
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", r.Display(), err))
			continue
		}
		if repoReport.CommitCount > 0 || len(repoReport.Activity) > 0 || hasCurrentOrTask(repoReport) {
			report.Repositories = append(report.Repositories, repoReport)
		}
	}

	sort.Slice(report.Repositories, func(i, j int) bool {
		if !report.Repositories[i].LatestActivity.Equal(report.Repositories[j].LatestActivity) {
			return report.Repositories[i].LatestActivity.After(report.Repositories[j].LatestActivity)
		}
		return strings.ToLower(report.Repositories[i].DisplayName) < strings.ToLower(report.Repositories[j].DisplayName)
	})
	applyGranularity(&report, opts)
	return report, nil
}

func loadActivity(opts Options, report *Report) map[string][]Activity {
	out := map[string][]Activity{}
	if !opts.LocalContext || opts.StatsPath == "" {
		return out
	}
	store, err := stats.OpenReadOnly(opts.StatsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			report.Warnings = append(report.Warnings, "stats: "+err.Error())
		}
		return out
	}
	defer store.Close()
	rows, err := store.ActivityTotals(stats.Query{Since: opts.Since, Until: opts.Until.Add(-time.Nanosecond)})
	if err != nil {
		report.Warnings = append(report.Warnings, "stats: "+err.Error())
		return out
	}
	for _, row := range rows {
		if row.Source == stats.SourceGit {
			continue
		}
		out[row.Repo] = append(out[row.Repo], Activity{Branch: row.Branch, Source: row.Source, Seconds: row.Seconds})
	}
	return out
}

func collectRepo(ctx context.Context, r repo.Repo, emails []string, opts Options, activity []Activity) (Repository, error) {
	result := Repository{Name: r.Name, DisplayName: r.Display(), Path: r.Path, Activity: activity}
	args := []string{"log", "--branches", "--remotes", "--source"}
	if !opts.IncludeMerges {
		args = append(args, "--no-merges")
	}
	args = append(args,
		"--since="+opts.Since.Format(time.RFC3339), "--before="+opts.Until.Format(time.RFC3339),
		"--format=%H%x1f%aI%x1f%an%x1f%aE%x1f%S%x1f%s")
	out, err := gitx.Run(ctx, r.Path, args...)
	if err != nil {
		return result, err
	}
	taskOIDs := taskCommitBranches(ctx, r, opts.Tasks, opts)
	branches := map[string]*Branch{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\x1f", 6)
		if len(fields) != 6 || !emailAllowed(fields[3], emails, opts.AllAuthors) {
			continue
		}
		when, err := time.Parse(time.RFC3339, fields[1])
		if err != nil {
			continue
		}
		branch, attribution := normalizeSource(fields[4])
		if taskBranch := taskOIDs[fields[0]]; taskBranch != "" {
			branch, attribution = taskBranch, "task"
		}
		if branch == "" {
			branch, attribution = "(unattributed)", "best-effort"
		}
		b := branches[branch]
		if b == nil {
			b = &Branch{Name: branch, Attribution: attribution}
			branches[branch] = b
		}
		c := Commit{OID: fields[0], ShortOID: shortOID(fields[0]), AuthoredAt: when,
			AuthorName: fields[2], AuthorEmail: fields[3], Subject: fields[5]}
		if opts.Metrics {
			c.Metrics = commitMetrics(ctx, r.Path, c.OID)
			addMetrics(&b.Metrics, c.Metrics)
			addMetrics(&result.Metrics, c.Metrics)
		}
		b.Commits = append(b.Commits, c)
		b.CommitCount++
		result.CommitCount++
		if when.After(b.LatestActivity) {
			b.LatestActivity = when
		}
		if when.After(result.LatestActivity) {
			result.LatestActivity = when
		}
	}

	if opts.LocalContext {
		attachCurrentContext(ctx, r, opts, branches, &result)
	}
	for _, a := range activity {
		if a.Branch != "" {
			ensureBranch(branches, a.Branch)
		}
	}
	for _, b := range branches {
		sort.Slice(b.Commits, func(i, j int) bool { return b.Commits[i].AuthoredAt.After(b.Commits[j].AuthoredAt) })
		result.Branches = append(result.Branches, *b)
	}
	sort.Slice(result.Branches, func(i, j int) bool {
		if !result.Branches[i].LatestActivity.Equal(result.Branches[j].LatestActivity) {
			return result.Branches[i].LatestActivity.After(result.Branches[j].LatestActivity)
		}
		return result.Branches[i].Name < result.Branches[j].Name
	})
	return result, nil
}

func taskCommitBranches(ctx context.Context, r repo.Repo, tasks []*task.Task, opts Options) map[string]string {
	out := map[string]string{}
	for _, t := range tasksForRepo(tasks, r) {
		if t.Branch == "" || t.Base == "" {
			continue
		}
		args := []string{"rev-list", "--since=" + opts.Since.Format(time.RFC3339), "--before=" + opts.Until.Format(time.RFC3339)}
		if !opts.IncludeMerges {
			args = append(args, "--no-merges")
		}
		args = append(args, t.Base+".."+t.Branch)
		text, err := gitx.Run(ctx, r.Path, args...)
		if err != nil {
			continue
		}
		for _, oid := range strings.Fields(text) {
			if prior := out[oid]; prior == "" || prior == t.Branch {
				out[oid] = t.Branch
			} else {
				out[oid] = ""
			}
		}
	}
	return out
}

func attachCurrentContext(ctx context.Context, r repo.Repo, opts Options, branches map[string]*Branch, result *Repository) {
	tasks := tasksForRepo(opts.Tasks, r)
	context := inventory.CollectRepoContext(ctx, r, tasks, nil, "")
	for _, checkout := range context.Checkouts {
		branch := checkout.Branch()
		if branch == "" {
			branch = "(detached)"
		}
		b := ensureBranch(branches, branch)
		if checkout.Status.Dirty() {
			current := CurrentWork{Path: checkout.Worktree.Path, Branch: branch,
				LatestChange: checkout.Status.LatestChange, Changed: checkout.Status.Changed,
				Staged: checkout.Status.Staged, Unstaged: checkout.Status.Unstaged, Untracked: checkout.Status.Untracked}
			if !current.LatestChange.IsZero() && !current.LatestChange.Before(opts.Since) && current.LatestChange.Before(opts.Until) {
				b.Current = append(b.Current, current)
				if current.LatestChange.After(b.LatestActivity) {
					b.LatestActivity = current.LatestChange
				}
				if current.LatestChange.After(result.LatestActivity) {
					result.LatestActivity = current.LatestChange
				}
			}
		}
	}
	for _, t := range tasks {
		b := ensureBranch(branches, t.Branch)
		if b.Task == nil {
			b.Task = &TaskContext{Title: t.Title(), State: t.State, Next: t.Next, Note: t.Note}
		}
		if !t.Updated.Before(opts.Since) && t.Updated.Before(opts.Until) {
			if t.Updated.After(b.LatestActivity) {
				b.LatestActivity = t.Updated
			}
			if t.Updated.After(result.LatestActivity) {
				result.LatestActivity = t.Updated
			}
		}
	}
}

func applyGranularity(report *Report, opts Options) {
	var refs []commitRef
	for ri := range report.Repositories {
		r := &report.Repositories[ri]
		for bi := range r.Branches {
			b := &r.Branches[bi]
			report.Summary.Branches++
			report.Summary.Commits += b.CommitCount
			addMetrics(&report.Summary.Metrics, b.Metrics)
			for ci := range b.Commits {
				refs = append(refs, commitRef{ri, bi, ci, b.Commits[ci].AuthoredAt})
			}
		}
	}
	report.Summary.Repositories = len(report.Repositories)
	if opts.Granularity == GranularityRepo {
		for i := range report.Repositories {
			report.Repositories[i].Branches = nil
		}
		return
	}
	if opts.Granularity == GranularityBranch {
		for ri := range report.Repositories {
			for bi := range report.Repositories[ri].Branches {
				report.Repositories[ri].Branches[bi].Commits = nil
			}
		}
		return
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].when.After(refs[j].when) })
	keep := map[string]bool{}
	limit := len(refs)
	if opts.MaxCommits > 0 && opts.MaxCommits < limit {
		limit = opts.MaxCommits
	}
	for _, ref := range refs[:limit] {
		c := report.Repositories[ref.repoIndex].Branches[ref.branchIndex].Commits[ref.commitIndex]
		keep[report.Repositories[ref.repoIndex].Path+"\x00"+c.OID] = true
	}
	for ri := range report.Repositories {
		for bi := range report.Repositories[ri].Branches {
			b := &report.Repositories[ri].Branches[bi]
			shown := b.Commits[:0]
			for _, c := range b.Commits {
				if keep[report.Repositories[ri].Path+"\x00"+c.OID] {
					shown = append(shown, c)
				}
			}
			b.Commits = shown
			b.ShownCommits = len(shown)
			b.OmittedCommits = b.CommitCount - b.ShownCommits
			report.Summary.ShownCommits += b.ShownCommits
		}
	}
	report.Summary.OmittedCommits = report.Summary.Commits - report.Summary.ShownCommits
}

func commitMetrics(ctx context.Context, dir, oid string) Metrics {
	out, err := gitx.Run(ctx, dir, "show", "--format=", "--numstat", "--find-renames", oid)
	if err != nil {
		return Metrics{}
	}
	files := map[string]bool{}
	var m Metrics
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		files[parts[2]] = true
		if parts[0] != "-" {
			n, _ := strconv.Atoi(parts[0])
			m.Additions += n
		}
		if parts[1] != "-" {
			n, _ := strconv.Atoi(parts[1])
			m.Deletions += n
		}
	}
	m.Files, m.Churn = len(files), m.Additions+m.Deletions
	return m
}

func addMetrics(dst *Metrics, src Metrics) {
	dst.Files += src.Files
	dst.Additions += src.Additions
	dst.Deletions += src.Deletions
	dst.Churn += src.Churn
}

func normalizeSource(source string) (string, string) {
	switch {
	case strings.HasPrefix(source, "refs/heads/"):
		return strings.TrimPrefix(source, "refs/heads/"), "current-ref"
	case strings.HasPrefix(source, "refs/remotes/"):
		return strings.TrimPrefix(source, "refs/remotes/"), "current-ref"
	}
	return source, "best-effort"
}

func emailAllowed(email string, allowed []string, all bool) bool {
	if all {
		return true
	}
	for _, value := range allowed {
		if strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func tasksForRepo(tasks []*task.Task, r repo.Repo) []*task.Task {
	var out []*task.Task
	for _, t := range tasks {
		if samePath(t.RepoPath, r.Path) || samePath(t.RepoPath, r.RealPath) || t.Repo == r.Name || t.Repo == r.Display() {
			out = append(out, t)
		}
	}
	return out
}

func samePath(a, b string) bool {
	return a != "" && b != "" && strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func ensureBranch(branches map[string]*Branch, name string) *Branch {
	b := branches[name]
	if b == nil {
		b = &Branch{Name: name, Attribution: "current-context"}
		branches[name] = b
	}
	return b
}

func hasCurrentOrTask(r Repository) bool {
	for _, b := range r.Branches {
		if len(b.Current) > 0 || (b.Task != nil && !b.LatestActivity.IsZero()) {
			return true
		}
	}
	return false
}

func shortOID(oid string) string {
	if len(oid) > 10 {
		return oid[:10]
	}
	return oid
}
