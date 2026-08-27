package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Layout is what `dev config init` discovered about a machine, so a generated
// config describes the paths that actually exist rather than the author's own.
type Layout struct {
	// ScanRoots are directories that already hold repositories.
	ScanRoots []string
	// ProjectRoot is the best candidate for new and graduated projects: the
	// scan root with the most repositories in it.
	ProjectRoot string
	// TriesRoot is where scratch experiments live.
	TriesRoot string
	// WorktreeRoot is where linked worktrees will go.
	WorktreeRoot string
	// Found records how many repositories were seen per candidate root, for
	// the comment written into the generated config.
	Found map[string]int
}

// candidateRoots are the conventional places people keep repositories, in no
// particular order — ranking is by how many repos each actually holds.
var candidateRoots = []string{
	"~/Documents/Program", "~/Documents/Projects", "~/Documents/Code",
	"~/src", "~/code", "~/Code", "~/dev", "~/Developer",
	"~/projects", "~/Projects", "~/work", "~/Work",
	"~/git", "~/repos", "~/workspace", "~/ghq",
}

// candidateTries are where an experiment directory is conventionally kept.
var candidateTries = []string{"~/src/tries", "~/tries", "~/scratch", "~/sandbox"}

// DetectLayout probes the machine for existing repository roots.
//
// A generated config that points at directories which do not exist is worse
// than no config: dev would silently discover nothing and the user would have
// no signal about why.
func DetectLayout() Layout {
	l := Layout{Found: map[string]int{}}

	// ghq keeps a configurable root and is worth honouring when present.
	roots := append([]string{}, candidateRoots...)
	if ghq := os.Getenv("GHQ_ROOT"); ghq != "" {
		for _, p := range filepath.SplitList(ghq) {
			roots = append(roots, p)
		}
	}

	seen := map[string]bool{}
	for _, cand := range roots {
		p := Expand(cand)
		if seen[p] {
			continue
		}
		seen[p] = true
		n, ok := countRepos(p)
		if !ok {
			continue
		}
		l.Found[cand] = n
		if n > 0 {
			l.ScanRoots = append(l.ScanRoots, cand)
		}
	}

	// Most repositories wins: that is where the user actually keeps projects,
	// and so where a new one belongs.
	best, bestN := "", -1
	for _, r := range l.ScanRoots {
		if l.Found[r] > bestN {
			best, bestN = r, l.Found[r]
		}
	}
	l.ProjectRoot = best

	// try-cli's own environment variable takes priority, then convention.
	if tp := os.Getenv("TRY_PATH"); tp != "" {
		l.TriesRoot = Contract(Expand(tp))
	} else {
		for _, cand := range candidateTries {
			if info, err := os.Stat(Expand(cand)); err == nil && info.IsDir() {
				l.TriesRoot = cand
				break
			}
		}
	}

	// Sort for a stable generated file.
	sort.Slice(l.ScanRoots, func(i, j int) bool {
		if l.Found[l.ScanRoots[i]] != l.Found[l.ScanRoots[j]] {
			return l.Found[l.ScanRoots[i]] > l.Found[l.ScanRoots[j]]
		}
		return l.ScanRoots[i] < l.ScanRoots[j]
	})

	// A tries root is also a scan root: experiments are repositories too, and
	// leaving them out would hide half the work in progress.
	if l.TriesRoot != "" && !contains(l.ScanRoots, l.TriesRoot) {
		if n, ok := countRepos(Expand(l.TriesRoot)); ok {
			l.Found[l.TriesRoot] = n
			l.ScanRoots = append(l.ScanRoots, l.TriesRoot)
		}
	}

	// Drop roots already covered by an ancestor: scanning ~/src and
	// ~/src/tries both would report every experiment twice and make the
	// generated config look confused about its own layout.
	l.ScanRoots = dropNested(l.ScanRoots)

	l.WorktreeRoot = "~/Worktrees"
	return l
}

// Fallbacks fills in the built-in defaults for anything detection missed, so
// a generated config is always complete and usable.
func (l Layout) Fallbacks() Layout {
	d := Default()
	if len(l.ScanRoots) == 0 {
		l.ScanRoots = d.Paths.ScanRoots
	}
	if l.ProjectRoot == "" {
		l.ProjectRoot = l.ScanRoots[0]
	}
	if l.TriesRoot == "" {
		l.TriesRoot = d.Paths.TriesRoot
	}
	if l.WorktreeRoot == "" {
		l.WorktreeRoot = d.Paths.WorktreeRoot
	}
	return l
}

// countRepos reports how many repositories sit under root, and whether root
// exists at all.
//
// It walks to the same depth as the real discovery pass, because layouts vary:
// <root>/<Repo>, <root>/<Category>/<Repo>, and ghq's
// <root>/<host>/<owner>/<Repo> all have to be recognised. It stays a heuristic
// for generating a config, so it stops at a repository rather than descending
// into one.
func countRepos(root string) (int, bool) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 0, false
	}
	return countReposDepth(root, 3), true
}

func countReposDepth(dir string, remaining int) int {
	if remaining <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if isRepo(child) {
			n++ // never descend into a repository
			continue
		}
		n += countReposDepth(child, remaining-1)
	}
	return n
}

func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// dropNested removes any root that lives under another root in the list. The
// discovery walk descends far enough to reach a nested root's repositories
// from its ancestor, so keeping both adds nothing but duplication.
func dropNested(roots []string) []string {
	out := roots[:0:0]
	for _, r := range roots {
		covered := false
		for _, other := range roots {
			if other == r {
				continue
			}
			if strings.HasPrefix(Expand(r), Expand(other)+string(filepath.Separator)) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, r)
		}
	}
	return out
}
