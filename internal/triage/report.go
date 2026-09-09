package triage

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type Finding struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Rank   int    `json:"rank"`
	Work   bool   `json:"work"`
}
type Action struct {
	Name         string `json:"name"`
	Remote       string `json:"remote,omitempty"`
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
}
type RuntimeLink struct {
	Backend string `json:"backend"`
	Handle  string `json:"handle"`
	Label   string `json:"label"`
	Agent   bool   `json:"agent"`
}
type Item struct {
	ID                    string            `json:"id"`
	RepositoryID          string            `json:"repository_id"`
	RepositoryPath        string            `json:"repository_path"`
	Path                  string            `json:"path"`
	Name                  string            `json:"name"`
	Kind                  string            `json:"kind"`
	Scope                 string            `json:"scope"`
	CatalogID             string            `json:"catalog_id,omitempty"`
	Phase                 string            `json:"phase,omitempty"`
	History               bool              `json:"history"`
	Branch                *gitx.BranchState `json:"branch,omitempty"`
	Status                *gitx.Status      `json:"status,omitempty"`
	DisposableDirs        []string          `json:"disposable_directories"`
	Ignored               []gitx.LocalPath  `json:"ignored,omitempty"`
	Tasks                 []*task.Task      `json:"tasks,omitempty"`
	Runtimes              []RuntimeLink     `json:"runtimes,omitempty"`
	Findings              []Finding         `json:"findings"`
	Actions               []Action          `json:"actions"`
	LastActivity          time.Time         `json:"last_activity"`
	Complete              bool              `json:"complete"`
	Deferred              string            `json:"deferred,omitempty"`
	Fingerprint           string            `json:"fingerprint"`
	Worktree              *gitx.Worktree    `json:"-"`
	ContentsFingerprint   string            `json:"-"`
	PreferenceFingerprint string            `json:"-"`
}
type Source struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
	Detail   string `json:"detail,omitempty"`
}
type Report struct {
	SchemaVersion  int       `json:"schema_version"`
	GeneratedAt    time.Time `json:"generated_at"`
	Complete       bool      `json:"complete"`
	Sources        []Source  `json:"sources"`
	Items          []Item    `json:"items"`
	RemoteEvidence string    `json:"remote_evidence"`
}

func (i Item) HasWork() bool {
	for _, f := range i.Findings {
		if f.Work {
			return true
		}
	}
	return false
}
func (i Item) Quick() bool {
	for _, a := range i.Actions {
		if a.Availability == "candidate" && a.Name != "fetch" {
			return true
		}
	}
	return false
}
func (i Item) Rank() int {
	r := 9
	for _, f := range i.Findings {
		if f.Rank < r {
			r = f.Rank
		}
	}
	return r
}
func (i Item) Action(name string) (Action, bool) {
	for _, a := range i.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}
func (i *Item) add(code, detail string, rank int, work bool) {
	i.Findings = append(i.Findings, Finding{code, detail, rank, work})
}

func SortItems(items []Item, quick bool) {
	sort.SliceStable(items, func(a, b int) bool {
		i, j := items[a], items[b]
		if quick && i.Quick() != j.Quick() {
			return i.Quick()
		}
		if (i.Deferred != "") != (j.Deferred != "") {
			return i.Deferred == ""
		}
		if i.Rank() != j.Rank() {
			return i.Rank() < j.Rank()
		}
		if !i.LastActivity.Equal(j.LastActivity) {
			return i.LastActivity.Before(j.LastActivity)
		}
		return i.ID < j.ID
	})
}

func SafeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func WriteReport(w io.Writer, r Report) error {
	if _, err := fmt.Fprintf(w, "Local triage · %d items · complete=%t\nRemote comparisons: %s\n", len(r.Items), r.Complete, r.RemoteEvidence); err != nil {
		return err
	}
	for _, source := range r.Sources {
		if !source.Complete {
			fmt.Fprintf(w, "! %s: %s\n", SafeText(source.Name), SafeText(source.Detail))
		}
	}
	for _, i := range r.Items {
		branch := ""
		if i.Branch != nil {
			branch = " " + strings.TrimPrefix(i.Branch.Ref, "refs/heads/")
		}
		fmt.Fprintf(w, "\n[%s/%s] %s%s  %s\n", i.Kind, i.Scope, SafeText(i.Name), SafeText(branch), SafeText(i.Path))
		if i.Deferred != "" {
			fmt.Fprintf(w, "  intent: %s\n", i.Deferred)
		}
		for _, f := range i.Findings {
			fmt.Fprintf(w, "  %s: %s\n", f.Code, SafeText(f.Detail))
		}
		for _, a := range i.Actions {
			fmt.Fprintf(w, "  → %s %s [%s] %s\n", a.Name, SafeText(a.Remote), a.Availability, SafeText(a.Reason))
		}
	}
	return nil
}
