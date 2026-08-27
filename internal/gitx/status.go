package gitx

import (
	"context"
	"strconv"
	"strings"
)

// Status is the live git state of one checkout — everything dev's inventory
// shows about a task without persisting any of it.
type Status struct {
	Branch   string
	Detached bool
	// Upstream is the tracking ref (e.g. "origin/feat/auth"), empty when the
	// branch has never been published.
	Upstream string
	Ahead    int
	Behind   int
	// Staged, Unstaged and Untracked count changed paths.
	Staged    int
	Unstaged  int
	Untracked int
	// Conflicted counts unmerged paths; a non-zero value means the checkout is
	// mid-merge or mid-rebase and must not be touched automatically.
	Conflicted int
}

// Dirty reports uncommitted work of any kind.
func (s Status) Dirty() bool {
	return s.Staged+s.Unstaged+s.Untracked+s.Conflicted > 0
}

// Published reports whether the branch has an upstream at all.
func (s Status) Published() bool { return s.Upstream != "" }

// Synced reports a branch that is published and exactly level with upstream.
func (s Status) Synced() bool { return s.Published() && s.Ahead == 0 && s.Behind == 0 }

// Summary renders the compact column used by `dev ls`: "clean", "↑2 ↓1 ●",
// "local", "!conflict".
func (s Status) Summary() string {
	var parts []string
	if s.Conflicted > 0 {
		parts = append(parts, "!conflict")
	}
	if s.Ahead > 0 {
		parts = append(parts, "↑"+strconv.Itoa(s.Ahead))
	}
	if s.Behind > 0 {
		parts = append(parts, "↓"+strconv.Itoa(s.Behind))
	}
	if s.Dirty() {
		parts = append(parts, "●")
	}
	if len(parts) == 0 {
		if !s.Published() {
			return "local"
		}
		return "clean"
	}
	return strings.Join(parts, " ")
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
	var s Status
	for _, rec := range nulLines(out) {
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			parseBranchHeader(rec, &s)
		case '1', '2':
			// "1 XY ..." ordinary change, "2 XY ..." rename/copy. Fields 2 is
			// the XY state pair: X staged, Y unstaged.
			f := strings.Fields(rec)
			if len(f) < 2 || len(f[1]) < 2 {
				continue
			}
			if f[1][0] != '.' {
				s.Staged++
			}
			if f[1][1] != '.' {
				s.Unstaged++
			}
		case 'u':
			s.Conflicted++
		case '?':
			s.Untracked++
		}
	}
	return s, nil
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
