package stats

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// SampleInterval is how much time one sampler run attributes to each active
// session. It must match how often the sampler actually runs, so the caller
// passes it explicitly rather than relying on a default that silently drifts.
const SampleInterval = 5 * time.Minute

// Sample attributes one interval of time to every live session whose working
// directory sits inside a known repository.
//
// Only sessions with a *working* agent, or any session when includeIdle is
// set, are counted. A workspace left open for three weeks is not three weeks
// of work, and counting it that way would make the whole chart meaningless.
func Sample(ctx context.Context, s *Store, rt runtime.Runtime, repos []repo.Repo, interval time.Duration, includeIdle bool) (int, error) {
	sessions, err := rt.List(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	seen := map[string]bool{}
	var entries []Entry

	for _, sess := range sessions {
		if !includeIdle && !isActive(sess.AgentStatus) {
			continue
		}
		for _, dir := range sess.Dirs {
			r, ok := repoContaining(ctx, repos, dir)
			if !ok {
				continue
			}
			// One session covering several panes in the same repo is still one
			// person working, so credit it once.
			key := sess.Handle + "\x00" + r.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			branch := ""
			if st, err := gitx.StatusOf(ctx, dir); err == nil {
				branch = st.Branch
			}
			entries = append(entries, Entry{
				Day: now, Repo: r.Name, Branch: branch,
				Source: SourceSession, Seconds: int(interval.Seconds()),
			})
		}
	}
	if err := s.Add(entries...); err != nil {
		return 0, err
	}
	return len(entries), s.MarkCollected("session", now)
}

// isActive reports a session doing something worth counting. Backends that
// report no status at all are counted, since the alternative is recording
// nothing on tmux.
func isActive(status string) bool {
	switch strings.ToLower(status) {
	case "working", "running", "busy", "":
		return true
	}
	return false
}

func repoContaining(ctx context.Context, repos []repo.Repo, dir string) (repo.Repo, bool) {
	var best repo.Repo
	bestLen := 0
	for _, r := range repos {
		if dir == r.Path || strings.HasPrefix(dir, r.Path+"/") {
			if len(r.Path) > bestLen {
				best, bestLen = r, len(r.Path)
			}
		}
	}
	if discovered, err := gitx.Discover(ctx, dir); err == nil {
		for _, r := range repos {
			if r.CommonDir != "" && r.CommonDir == discovered.GitCommonDir {
				return r, true
			}
		}
	}
	return best, bestLen > 0
}

// CommitWeight is the time credited to one commit when backfilling from git
// history. Commits are evidence that work happened on a day, not a measure of
// how long it took, so this is a coarse stand-in — enough to shade a heatmap
// cell, not to bill against.
const CommitWeight = 20 * time.Minute

// BackfillGit reads commit history and records one CommitWeight per commit,
// per day, per repo.
//
// This is what makes a heatmap useful on the day dev is installed rather than
// three months later, and what lets the chart survive losing stats.db.
func BackfillGit(ctx context.Context, s *Store, repos []repo.Repo, since time.Time, author string) (int, error) {
	var entries []Entry
	for _, r := range repos {
		if r.Bare {
			continue
		}
		args := []string{"log", "--all", "--no-merges",
			"--since=" + since.Format("2006-01-02"), "--format=%at%x09%aE"}
		out, err := gitx.Run(ctx, r.Path, args...)
		if err != nil || out == "" {
			continue
		}
		// Commits land in buckets by day; several commits on one day add up.
		perDay := map[string]int{}
		for _, line := range strings.Split(out, "\n") {
			ts, email, _ := strings.Cut(line, "\t")
			if author != "" && !strings.EqualFold(email, author) {
				continue
			}
			unix, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
			if err != nil {
				continue
			}
			perDay[time.Unix(unix, 0).Format(dayFormat)]++
		}
		for day, count := range perDay {
			d, err := time.ParseInLocation(dayFormat, day, time.Local)
			if err != nil {
				continue
			}
			entries = append(entries, Entry{
				Day: d, Repo: r.Name, Source: SourceGit,
				Seconds: count * int(CommitWeight.Seconds()),
			})
		}
	}
	// Set, not Add: re-running a backfill over the same window must not double
	// the numbers.
	if err := s.Set(entries...); err != nil {
		return 0, err
	}
	return len(entries), s.MarkCollected("git", time.Now())
}
