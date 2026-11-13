package tui

import (
	"context"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func visiblePaths(m Model) []string {
	var out []string
	for _, item := range m.visibleRepoItems() {
		out = append(out, item.Repo.Repo.Path)
	}
	return out
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cachedRow(name string, live bool) RepoRow {
	return RepoRow{Repo: repo.Repo{Name: name, Path: "/" + name}, Pending: "cached", CachedLive: live}
}

// Streaming progress fills sort keys one repository at a time. The order
// captured at the cache frame must hold until the complete snapshot, which
// re-sorts once while keeping the cursor on the same repository.
func TestRepoOrderFreezesWhileStreamingAndSortsOnceOnCompletion(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view, m.width, m.height = ViewRepos, 100, 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	a, b, c := cachedRow("a", false), cachedRow("b", true), cachedRow("c", false)
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{a, b, c}, Valid: true})
	initial := visiblePaths(m)
	if !samePaths(initial, []string{"/b", "/a", "/c"}) {
		t.Fatalf("cached live hint did not order the first frame: %v", initial)
	}
	m.setAt(2)
	enriched := RepoRow{Repo: repo.Repo{Name: "c", Path: "/c"}, Tasks: []*task.Task{{ID: "c", State: task.Hot}}}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Repos: []RepoRow{enriched}, Valid: true})
	if got := visiblePaths(m); !samePaths(got, initial) {
		t.Fatalf("enrichment reordered the streaming list: %v", got)
	}
	liveB := RepoRow{Repo: repo.Repo{Name: "b", Path: "/b"}, Live: true}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "runtime", Repos: []RepoRow{liveB}, Valid: true})
	late := RepoRow{Repo: repo.Repo{Name: "d", Path: "/d"}, Pending: "loading"}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Repos: []RepoRow{late}, Valid: true})
	// d has no keys yet, so activity order slots it after a; c keeps its frozen slot.
	if got := visiblePaths(m); !samePaths(got, []string{"/b", "/a", "/d", "/c"}) {
		t.Fatalf("late discovery must slot in without moving frozen rows: %v", got)
	}
	m.repos = append([]RepoRow(nil), m.repos...)
	m.repos[len(m.repos)-1].Live = true // a later observation never moves a placed row
	if got := visiblePaths(m); !samePaths(got, []string{"/b", "/a", "/d", "/c"}) {
		t.Fatalf("placed row moved after observation: %v", got)
	}
	if item, ok := m.currentRepoItem(); !ok || item.Repo.Repo.Path != "/c" {
		t.Fatalf("cursor moved during streaming: %+v", item.Repo.Repo.Path)
	}
	complete := []RepoRow{{Repo: repo.Repo{Name: "a", Path: "/a"}}, liveB, enriched, {Repo: repo.Repo{Name: "d", Path: "/d"}}}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Repos: complete, Valid: true})
	if got := visiblePaths(m); !samePaths(got, []string{"/c", "/b", "/a", "/d"}) {
		t.Fatalf("complete snapshot did not apply the full ordering once: %v", got)
	}
	if item, ok := m.currentRepoItem(); !ok || item.Repo.Repo.Path != "/c" {
		t.Fatalf("completion lost the selected repository: %+v", item.Repo.Repo.Path)
	}
	if m.repoOrder != nil || m.repoOrderFrozen() {
		t.Fatal("frozen order survived completion")
	}
}

func TestRepoOrderChangesResortImmediatelyWhileStreaming(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view, m.width, m.height = ViewRepos, 100, 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	rows := []RepoRow{cachedRow("b", false), cachedRow("a", true)}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: rows, Valid: true})
	if got := visiblePaths(m); !samePaths(got, []string{"/a", "/b"}) {
		t.Fatalf("unexpected first frame: %v", got)
	}
	next, _ := m.runListAction(listActionCycleSort) // activity → latest; equal latest falls back to name
	m = next.(Model)
	if !m.repoOrderFrozen() || !samePaths(visiblePaths(m), []string{"/a", "/b"}) {
		t.Fatalf("sort cycle must re-freeze with the new order: %v", visiblePaths(m))
	}
	next, _ = m.runListAction(listActionReverseSort)
	m = next.(Model)
	if got := visiblePaths(m); !samePaths(got, []string{"/b", "/a"}) {
		t.Fatalf("reverse did not apply during streaming: %v", got)
	}
	next, _ = m.cycleTableSort("repo")
	m = next.(Model)
	if got := visiblePaths(m); !samePaths(got, []string{"/a", "/b"}) {
		t.Fatalf("column sort did not apply during streaming: %v", got)
	}
	m.filter = "b"
	if got := visiblePaths(m); !samePaths(got, []string{"/b"}) {
		t.Fatalf("filter during streaming: %v", got)
	}
	m.filter = ""
	m.beginViewLoad(ViewRepos, loadRefresh)
	if m.repoOrder != nil {
		t.Fatal("a new load must drop the previous frozen order")
	}
}

func TestRepoOrderWithoutCacheFreezesArrivalOrder(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view, m.width, m.height = ViewRepos, 100, 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	first := RepoRow{Repo: repo.Repo{Name: "z", Path: "/z"}, Pending: "loading"}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Repos: []RepoRow{first}, Valid: true})
	hot := RepoRow{Repo: repo.Repo{Name: "a", Path: "/a"}, Tasks: []*task.Task{{ID: "a", State: task.Hot}}}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Repos: []RepoRow{hot}, Valid: true})
	if got := visiblePaths(m); !samePaths(got, []string{"/a", "/z"}) {
		t.Fatalf("a row arriving after the freeze takes its sorted slot once: %v", got)
	}
	m.repos = append([]RepoRow(nil), m.repos...)
	m.repos[0].Tasks = hot.Tasks // z becomes hot later; the frozen order holds
	if got := visiblePaths(m); !samePaths(got, []string{"/a", "/z"}) {
		t.Fatalf("frozen row moved: %v", got)
	}
}

func TestDrainLocalBatchesBufferedResultsAndReportsClose(t *testing.T) {
	results := make(chan LocalResult, 4)
	for i := range 3 {
		results <- LocalResult{View: ViewRepos, Generation: uint64(i + 1)}
	}
	msg := drainLocal(results, 0, localBatchLimit)
	if len(msg.results) != 3 || msg.done {
		t.Fatalf("drained %d results, done=%v", len(msg.results), msg.done)
	}
	results <- LocalResult{View: ViewRepos, Generation: 9}
	close(results)
	msg = drainLocal(results, 0, localBatchLimit)
	if len(msg.results) != 1 || msg.done {
		t.Fatalf("a message must carry results or done, never both: %d results, done=%v", len(msg.results), msg.done)
	}
	msg = drainLocal(results, 0, localBatchLimit)
	if len(msg.results) != 0 || !msg.done {
		t.Fatal("closed channel must report done without results")
	}
	limited := make(chan LocalResult, 4)
	for range 4 {
		limited <- LocalResult{}
	}
	if msg = drainLocal(limited, 0, 2); len(msg.results) != 2 {
		t.Fatalf("limit ignored: %d", len(msg.results))
	}
	window := make(chan LocalResult, 1)
	go func() {
		window <- LocalResult{}
		time.Sleep(5 * time.Millisecond)
		window <- LocalResult{}
	}()
	if msg = drainLocal(window, time.Second, localBatchLimit); len(msg.results) != 2 {
		t.Fatalf("window did not gather the follow-up result: %d", len(msg.results))
	}
}

func TestDrainSizeBatchesResults(t *testing.T) {
	results := make(chan diskusage.Result, 3)
	results <- diskusage.Result{Key: "a"}
	results <- diskusage.Result{Key: "b"}
	close(results)
	msg := drainSize(7, results, 0, localBatchLimit)
	if msg.loadID != 7 || len(msg.results) != 2 || msg.done {
		t.Fatalf("size drain = %+v", msg)
	}
	if msg = drainSize(7, results, 0, localBatchLimit); !msg.done || len(msg.results) != 0 {
		t.Fatalf("size close = %+v", msg)
	}
}

func TestLocalBatchAppliesEveryResultAndStartsSizesOnce(t *testing.T) {
	starts := 0
	m := New(Actions{Sizes: SizeActions{Start: func(_ context.Context, targets []diskusage.Target, _ bool) diskusage.Load {
		starts++
		results := make(chan diskusage.Result)
		close(results)
		return diskusage.Load{ID: 1, Results: results}
	}}}, nil, nil)
	m.view, m.width, m.height = ViewRepos, 100, 30
	tasks, repos, tries := m.beginLocalLoads(loadInitial)
	load := LocalLoad{ID: 1, Request: LocalLoadRequest{CycleGeneration: m.localGeneration, TasksGeneration: tasks, ReposGeneration: repos, TriesGeneration: tries}}
	a := RepoRow{Repo: repo.Repo{Name: "a", Path: "/a"}, SizeTarget: diskusage.Plain("/a")}
	batch := localMsg{load: load, done: true, results: []LocalResult{
		{View: ViewRepos, Generation: repos, Phase: "cache", Repos: []RepoRow{{Repo: a.Repo, Pending: "cached"}}, Valid: true},
		{View: ViewRepos, Generation: repos, Phase: "enrichment", Repos: []RepoRow{a}, Valid: true},
		{View: ViewRepos, Generation: repos, Repos: []RepoRow{a}, Valid: true},
		{View: ViewTasks, Generation: tasks, Valid: true},
		{View: ViewTries, Generation: tries, Valid: true},
	}}
	m = startupSend(m, batch)
	if len(m.repos) != 1 || m.repos[0].Pending != "" || m.repoProgressPhase != 3 {
		t.Fatalf("batch did not apply every result: %+v phase=%d", m.repos, m.repoProgressPhase)
	}
	if starts != 1 || m.viewLoad(ViewRepos).loading {
		t.Fatalf("sizes started %d times, loading=%v", starts, m.viewLoad(ViewRepos).loading)
	}
}

func TestRepoProgressKeepsCachedSizesByKeyAndPrefersLiveMeasurements(t *testing.T) {
	target := diskusage.Plain("/a")
	m := New(Actions{Sizes: SizeActions{Start: func(context.Context, []diskusage.Target, bool) diskusage.Load {
		t.Fatal("progress must not start a size load")
		return diskusage.Load{}
	}}}, nil, nil)
	m.view, m.width, m.height = ViewRepos, 100, 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	stale := &diskusage.Usage{OwnedBytes: 42, Complete: true, Cached: true, Stale: true}
	cached := RepoRow{Repo: repo.Repo{Name: "a", Path: "/a"}, Pending: "cached", SizeTarget: target, Usage: stale}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{cached}, Valid: true})
	if cell := sizeCell(m.repos[0].Usage, nil, target); cell != "42 B~" {
		t.Fatalf("stale cached size cell = %q", cell)
	}
	enriched := RepoRow{Repo: cached.Repo, SizeTarget: target}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Repos: []RepoRow{enriched}, Valid: true})
	if m.repos[0].Usage == nil || m.repos[0].Usage.OwnedBytes != 42 {
		t.Fatalf("same-key enrichment must retain the cached size: %+v", m.repos[0].Usage)
	}
	m.applySizeResult(diskusage.Result{Key: target.Key, Usage: diskusage.Usage{OwnedBytes: 50, Complete: true}})
	again := RepoRow{Repo: cached.Repo, SizeTarget: target, Usage: stale}
	merged := mergeRepoProgress(m.repos, []RepoRow{again})
	if merged[0].Usage == nil || merged[0].Usage.OwnedBytes != 50 || merged[0].Usage.Cached {
		t.Fatalf("a cache read must not replace this session's measurement: %+v", merged[0].Usage)
	}
	rekeyed := RepoRow{Repo: cached.Repo, SizeTarget: diskusage.Plain("/a-moved")}
	if merged = mergeRepoProgress(m.repos, []RepoRow{rekeyed}); merged[0].Usage != nil {
		t.Fatal("a changed size key must not inherit the old measurement")
	}
}
