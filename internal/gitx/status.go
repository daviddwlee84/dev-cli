package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Status is the live git state of one checkout — everything dev's inventory
// shows about a task without persisting any of it.
type Status struct {
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	// Upstream is the tracking ref (e.g. "origin/feat/auth"), empty when the
	// branch has never been published.
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	// Changed counts unique changed paths. Staged and Unstaged are index /
	// working-tree views and can overlap when one path has both kinds of
	// changes; Changed does not double-count it.
	Changed   int `json:"changed"`
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
	// Added, Modified, Deleted and Renamed classify status letters. One path
	// can contribute to two when its staged and unstaged states differ.
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
	Renamed  int `json:"renamed"`
	// LatestChange is the newest mtime among changed files that still exist.
	// Deleted paths fall back to the checkout's last commit time at the repo
	// aggregation layer.
	LatestChange time.Time `json:"latest_change,omitempty"`
	// Conflicted counts unmerged paths; a non-zero value means the checkout is
	// mid-merge or mid-rebase and must not be touched automatically.
	Conflicted int `json:"conflicted"`
}

// Dirty reports uncommitted work of any kind.
func (s Status) Dirty() bool { return s.Changed > 0 }

// Published reports whether the branch has an upstream at all.
func (s Status) Published() bool { return s.Upstream != "" }

// Synced reports a branch that is published and exactly level with upstream.
func (s Status) Synced() bool { return s.Published() && s.Ahead == 0 && s.Behind == 0 }

// Summary renders the compact, starship-like column used throughout dev:
//
//	⇕⇡2⇣1 =1 +3 !2 ?4
//
// Divergence, conflicts, staged, unstaged and untracked paths remain separate
// so "dirty" says how and by how much, rather than collapsing to one ●.
func (s Status) Summary() string {
	var parts []string
	switch {
	case s.Ahead > 0 && s.Behind > 0:
		parts = append(parts, "⇕⇡"+strconv.Itoa(s.Ahead)+"⇣"+strconv.Itoa(s.Behind))
	case s.Ahead > 0:
		parts = append(parts, "⇡"+strconv.Itoa(s.Ahead))
	case s.Behind > 0:
		parts = append(parts, "⇣"+strconv.Itoa(s.Behind))
	}
	if s.Conflicted > 0 {
		parts = append(parts, "="+strconv.Itoa(s.Conflicted))
	}
	if s.Staged > 0 {
		parts = append(parts, "+"+strconv.Itoa(s.Staged))
	}
	if s.Unstaged > 0 {
		parts = append(parts, "!"+strconv.Itoa(s.Unstaged))
	}
	if s.Untracked > 0 {
		parts = append(parts, "?"+strconv.Itoa(s.Untracked))
	}
	if len(parts) == 0 {
		if !s.Published() {
			return "local"
		}
		return "clean"
	}
	return strings.Join(parts, " ")
}

// Breakdown renders the detailed change count shown by `dev status` and the
// TUI detail pane. Changed is the unique-path total; staged and unstaged may
// overlap, which is stated by presenting them as categories rather than a sum.
func (s Status) Breakdown() string {
	if s.Changed == 0 {
		return "0 changed paths"
	}
	var parts []string
	if s.Staged > 0 {
		parts = append(parts, "+"+strconv.Itoa(s.Staged)+" staged")
	}
	if s.Unstaged > 0 {
		parts = append(parts, "!"+strconv.Itoa(s.Unstaged)+" unstaged")
	}
	if s.Untracked > 0 {
		parts = append(parts, "?"+strconv.Itoa(s.Untracked)+" untracked")
	}
	if s.Conflicted > 0 {
		parts = append(parts, "="+strconv.Itoa(s.Conflicted)+" conflicted")
	}
	return strconv.Itoa(s.Changed) + " changed paths (" + strings.Join(parts, ", ") + ")"
}

// TypeBreakdown classifies the status letters when that detail is available.
func (s Status) TypeBreakdown() string {
	var parts []string
	for _, item := range []struct {
		n    int
		name string
	}{
		{s.Added, "added"}, {s.Modified, "modified"}, {s.Deleted, "deleted"},
		{s.Renamed, "renamed/copied"},
	} {
		if item.n > 0 {
			parts = append(parts, strconv.Itoa(item.n)+" "+item.name)
		}
	}
	return strings.Join(parts, ", ")
}

// StatusOf reads the porcelain v2 status of the checkout at dir. Porcelain v2
// is the documented machine-readable format: it reports branch, upstream and
// ahead/behind in the same pass as the file states, so one process gives dev
// everything it needs.
func StatusOf(ctx context.Context, dir string) (Status, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal", "-z")
	if err != nil {
		return Status{}, err
	}
	return statusFromOutput(dir, out), nil
}

func statusFromOutput(dir, out string) Status {
	var s Status
	for _, rec := range nulLines(out) {
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			parseBranchHeader(rec, &s)
		case '1', '2':
			// "1 XY ..." ordinary change, "2 XY ..." rename/copy. X is
			// staged, Y unstaged. This record is one unique changed path even
			// when both sides are non-dot.
			f := strings.Fields(rec)
			if len(f) < 2 || len(f[1]) < 2 {
				continue
			}
			s.Changed++
			x, y := f[1][0], f[1][1]
			if x != '.' {
				s.Staged++
			}
			if y != '.' {
				s.Unstaged++
			}
			classifyChange(x, &s)
			classifyChange(y, &s)
			updateLatestChange(dir, statusPath(rec), &s)
		case 'u':
			s.Changed++
			s.Conflicted++
			updateLatestChange(dir, statusPath(rec), &s)
		case '?':
			s.Changed++
			s.Untracked++
			updateLatestChange(dir, strings.TrimPrefix(rec, "? "), &s)
		}
	}
	return s
}

// statusPath extracts the final pathname from a porcelain-v2 record while
// preserving spaces in it. Rename records have one extra score field; their
// old path arrives as the next NUL record and is intentionally ignored.
func statusPath(rec string) string {
	if rec == "" {
		return ""
	}
	var fields int
	switch rec[0] {
	case '1':
		fields = 9
	case '2':
		fields = 10
	case 'u':
		fields = 11
	default:
		return ""
	}
	parts := strings.SplitN(rec, " ", fields)
	if len(parts) != fields {
		return ""
	}
	return parts[fields-1]
}

func updateLatestChange(dir, path string, s *Status) {
	if path == "" {
		return
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path)))
	if err == nil && info.ModTime().After(s.LatestChange) {
		s.LatestChange = info.ModTime()
	}
}

func classifyChange(code byte, s *Status) {
	switch code {
	case 'A':
		s.Added++
	case 'M', 'T':
		s.Modified++
	case 'D':
		s.Deleted++
	case 'R', 'C':
		s.Renamed++
	}
}

func parseBranchHeader(rec string, s *Status) {
	f := strings.Fields(rec)
	if len(f) < 3 {
		return
	}
	switch f[1] {
	case "branch.head":
		if f[2] == "(detached)" {
			s.Detached = true
		} else {
			s.Branch = f[2]
		}
	case "branch.upstream":
		s.Upstream = f[2]
	case "branch.ab":
		// "# branch.ab +2 -1"
		if len(f) < 4 {
			return
		}
		s.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
		s.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
	}
}

// LastCommit reports the author timestamp of HEAD as a Unix epoch, and the
// subject line. Used to age a task in `dev sweep`.
func LastCommit(ctx context.Context, dir string) (unix int64, subject string, err error) {
	out, err := run(ctx, dir, "log", "-1", "--format=%at%x00%s")
	if err != nil || out == "" {
		return 0, "", err
	}
	ts, subj, _ := strings.Cut(out, "\x00")
	unix, _ = strconv.ParseInt(ts, 10, 64)
	return unix, subj, nil
}

// WipCommit stages everything and records a checkpoint commit.
//
// A checkpoint commit is deliberately preferred over `git stash`: a stash is
// invisible in the log, easy to forget, and cannot be pushed — so it can never
// travel to another machine, which is exactly what parking a task needs.
func WipCommit(ctx context.Context, dir, message string) (bool, error) {
	st, err := StatusOf(ctx, dir)
	if err != nil {
		return false, err
	}
	if !st.Dirty() {
		return false, nil
	}
	if st.Conflicted > 0 {
		return false, &Error{Args: []string{"commit"}, Dir: dir,
			Stderr: "checkout has unmerged paths; resolve the conflict before parking"}
	}
	if _, err := run(ctx, dir, "add", "--all"); err != nil {
		return false, err
	}
	if message == "" {
		message = "wip: checkpoint"
	}
	if _, err := run(ctx, dir, "commit", "--no-verify", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}
