package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func treeSnapshot(host string, names ...string) fleet.HostResult {
	s := &fleet.Snapshot{SchemaVersion: 1, Host: host, GeneratedAt: time.Now().UTC()}
	for _, name := range names {
		known := true
		s.Repositories = append(s.Repositories, fleet.RepoSnapshot{Name: name, Display: name, Path: "/src/" + name, GitKnown: &known})
	}
	return fleet.HostResult{Name: host, State: fleet.HostOK, Snapshot: s}
}

func treeHosts() FleetHostsResult {
	return FleetHostsResult{MaxParallel: 2, Hosts: []FleetHostDescriptor{
		{Key: "local", Name: "laptop", Local: true, EndpointID: "local", OS: "darwin", Target: "this machine"},
		{Key: "a", Name: "alpha", EndpointID: "a1", Target: "ssh-alpha", OS: "posix"},
		{Key: "b", Name: "beta", EndpointID: "b1", Target: "ssh-beta", OS: "windows"},
	}}
}

func treeModel(a Actions) Model {
	if a.LoadFleetHosts == nil {
		a.LoadFleetHosts = func(context.Context) (FleetHostsResult, error) { return treeHosts(), nil }
	}
	m := New(a, nil, []RepoRow{{Repo: repo.Repo{Name: "local-repo", Path: "/src/local-repo"}, GitKnown: true}})
	m.view = ViewFleet
	return m
}

func treeSend(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func treeRun(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			m = treeRun(t, m, child)
		}
		return m
	}
	next, follow := treeSend(m, msg)
	return treeRun(t, next, follow)
}

func treeAccept(m Model, hosts FleetHostsResult) (Model, tea.Cmd) {
	return treeSend(m, fleetHostsMsg{generation: m.fleetTree.generation, result: hosts})
}

func treeSelect(t *testing.T, m *Model, key, path string) {
	t.Helper()
	for i, r := range m.visibleFleet() {
		if r.HostKey == key && ((r.Repository == nil && path == "") || (r.Repository != nil && r.Repository.Path == path)) {
			m.fleetCursor = i
			return
		}
	}
	t.Fatalf("missing tree selection %s %s: %+v", key, path, m.visibleFleet())
}

func TestFleetTreeDescriptorsDoNotWaitForLocalRepositories(t *testing.T) {
	reads := 0
	m := treeModel(Actions{LoadFleetHost: func(context.Context, FleetHostDescriptor) (fleet.HostResult, error) {
		reads++
		return treeSnapshot("alpha", "remote-repo"), nil
	}})
	m.beginViewLoad(ViewRepos, loadInitial)
	m, cmd := treeAccept(m, treeHosts())
	if cmd != nil || reads != 0 {
		t.Fatal("descriptor load contacted remote")
	}
	rows := m.visibleFleet()
	if len(rows) != 4 || !rows[0].Local || !rows[0].Expanded || rows[1].Repository == nil || rows[2].Expanded || rows[3].Expanded {
		t.Fatalf("tree rows=%+v", rows)
	}
	if !strings.Contains(m.View(), "alpha") || strings.Contains(m.View(), "waiting for local repositories") {
		t.Fatal(m.View())
	}
	treeSelect(t, &m, "a", "")
	next, cmd := m.toggleFleetHost()
	m = treeRun(t, next.(Model), cmd)
	if reads != 1 || m.fleetTree.hosts[1].result.Snapshot == nil {
		t.Fatal("remote load depended on local inventory")
	}
	if got := m.fleetLocalResult(m.fleetTree.hosts[0].descriptor); got.Snapshot == nil || len(got.Snapshot.Repositories) != 1 || !got.FromCache {
		t.Fatalf("local accepted snapshot not reused: %+v", got)
	}
}

func TestFleetTreeInitialConfigErrorKeepsLocalDescriptor(t *testing.T) {
	m := treeModel(Actions{})
	hosts := treeHosts()
	hosts.Hosts = hosts.Hosts[:1]
	m, _ = treeSend(m, fleetHostsMsg{generation: 1, result: hosts, err: errors.New("invalid remotes")})
	if len(m.fleetTree.hosts) != 1 || !strings.Contains(m.View(), "local-repo") || !strings.Contains(m.View(), "invalid remotes") {
		t.Fatal(m.View())
	}
	m, _ = treeSend(m, fleetHostsMsg{generation: 1, err: errors.New("still invalid")})
	if len(m.fleetTree.hosts) != 1 {
		t.Fatal("failed refresh removed local host")
	}
}

func TestFleetTreeCacheIsIncrementalAndWarmupWaitsForIt(t *testing.T) {
	var reads []string
	a := Actions{LoadFleetHostCache: func(_ context.Context, d FleetHostDescriptor) (*fleet.HostResult, bool, error) {
		cached := treeSnapshot(d.Name, "old-"+d.Key)
		cached.FromCache = true
		cached.Snapshot.GeneratedAt = time.Now().Add(-48 * time.Hour)
		return &cached, d.Key == "a", nil
	}, LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		reads = append(reads, d.Key)
		return treeSnapshot(d.Name, "live-"+d.Key), nil
	}}
	m := treeModel(a)
	m, cacheCmd := treeAccept(m, treeHosts())
	if len(m.visibleFleet()) != 4 || m.fleetTree.hosts[1].result.Snapshot != nil {
		t.Fatal("descriptors waited on cache")
	}
	m, warmCmd := treeSend(m, fleetWarmupMsg{})
	if warmCmd != nil || len(reads) != 0 {
		t.Fatal("warmup raced cache eligibility")
	}
	m = treeRun(t, m, cacheCmd)
	if !reflect.DeepEqual(reads, []string{"b"}) {
		t.Fatalf("reads=%v", reads)
	}
	m.filter = "old-a"
	if rows := m.visibleFleet(); len(rows) != 2 || rows[1].Repository.Name != "old-a" || !rows[1].FromCache {
		t.Fatalf("old cache not searchable: %+v", rows)
	}
}

func TestFleetTreeWarmupGateAndDisabledConfiguration(t *testing.T) {
	reads := 0
	m := treeModel(Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		reads++
		return treeSnapshot(d.Name), nil
	}})
	m, _ = treeAccept(m, treeHosts())
	command := m.fleetWarmupAfterFrame()
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()
	select {
	case <-result:
		t.Fatal("warmup passed first-frame gate")
	default:
	}
	_ = m.View()
	select {
	case msg := <-result:
		var cmd tea.Cmd
		m, cmd = treeSend(m, msg)
		m = treeRun(t, m, cmd)
	case <-time.After(time.Second):
		t.Fatal("frame did not release warmup")
	}
	if reads != 2 {
		t.Fatalf("warmup reads=%d", reads)
	}
	m, cmd := treeSend(m, fleetWarmupMsg{})
	m = treeRun(t, m, cmd)
	if reads != 2 {
		t.Fatal("warmup repeated per-run attempts")
	}
	disabled := treeModel(m.actions).WithFleetBackgroundRefresh(false)
	disabled, _ = treeAccept(disabled, treeHosts())
	disabled, cmd = treeSend(disabled, fleetWarmupMsg{})
	if cmd != nil || disabled.fleetTree.warmReady {
		t.Fatal("disabled setting still warmed")
	}
	treeSelect(t, &disabled, "a", "")
	next, cmd := disabled.toggleFleetHost()
	treeRun(t, next.(Model), cmd)
	if reads != 3 {
		t.Fatal("disabled background also disabled explicit load")
	}
}

func TestFleetTreeExplicitReadPriorityDedupAndCollapse(t *testing.T) {
	a := Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		return treeSnapshot(d.Name, "repo-"+d.Key), nil
	}}
	m := treeModel(a)
	hosts := treeHosts()
	hosts.MaxParallel = 1
	m, _ = treeAccept(m, hosts)
	m, warm := treeSend(m, fleetWarmupMsg{})
	if warm == nil || !m.fleetTree.hosts[1].loading || m.fleetTree.hosts[2].loading {
		t.Fatal("warmup did not run one at a time")
	}
	treeSelect(t, &m, "a", "")
	next, cmd := m.toggleFleetHost()
	m = next.(Model)
	if cmd != nil {
		t.Fatal("expansion duplicated in-flight read")
	}
	next, cmd = m.toggleFleetHost()
	m = next.(Model)
	if cmd != nil || !m.fleetTree.hosts[1].loading {
		t.Fatal("collapse invalidated useful read")
	}
	m.enqueueFleetRead("b", true, false)
	if len(m.fleetTree.queue) != 1 || !m.fleetTree.queue[0].explicit {
		t.Fatalf("explicit read not promoted: %+v", m.fleetTree.queue)
	}
	m = treeRun(t, m, warm)
	if m.fleetTree.hosts[1].expanded || m.fleetTree.hosts[1].result.Snapshot == nil || m.fleetTree.hosts[2].result.Snapshot == nil {
		t.Fatal("completion lost collapsed cache or queued host")
	}
}

func TestFleetTreeStaleHostAndCacheResultsCannotReplaceNewerRead(t *testing.T) {
	contexts := []context.Context{}
	a := Actions{LoadFleetHost: func(ctx context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		contexts = append(contexts, ctx)
		return treeSnapshot(d.Name, "current"), nil
	}}
	m := treeModel(a)
	m, _ = treeAccept(m, treeHosts())
	m.enqueueFleetRead("a", true, false)
	oldCmd := m.pumpFleetReads()
	oldRequest := m.fleetTree.hosts[1].request
	oldMessage := oldCmd().(fleetHostMsg)
	m.enqueueFleetRead("a", true, true)
	newCmd := m.pumpFleetReads()
	if contexts[0].Err() == nil {
		t.Fatal("refresh did not cancel predecessor")
	}
	m = treeRun(t, m, newCmd)
	oldMessage.result = treeSnapshot("alpha", "obsolete")
	m, _ = treeSend(m, oldMessage)
	cache := treeSnapshot("alpha", "late-cache")
	cache.Snapshot.GeneratedAt = time.Now().Add(time.Hour)
	m, _ = treeSend(m, fleetHostCacheMsg{key: "a", endpoint: "a1", generation: 1, request: oldRequest, result: &cache, fresh: true})
	if got := m.fleetTree.hosts[1].result.Snapshot.Repositories[0].Name; got != "current" {
		t.Fatalf("newer result overwritten: %s", got)
	}
	m.fleetTree.generation++
	hosts := treeHosts()
	hosts.Hosts[1].EndpointID = "a2"
	m, _ = treeAccept(m, hosts)
	m, _ = treeSend(m, oldMessage)
	if m.fleetTree.hosts[1].result.Snapshot != nil {
		t.Fatal("old endpoint repopulated replacement")
	}
}

func TestFleetTreeFailureRetainsCacheIncludingNoDev(t *testing.T) {
	cached := treeSnapshot("alpha", "remembered")
	cached.FromCache = true
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	m := treeModel(Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		return fleet.HostResult{State: fleet.HostNoDev, Error: "dev is missing"}, nil
	}})
	m, _ = treeAccept(m, hosts)
	treeSelect(t, &m, "a", "")
	next, cmd := m.refreshSelectedFleetHost()
	m = treeRun(t, next.(Model), cmd)
	m.filter = "remembered"
	rows := m.visibleFleet()
	if len(rows) != 2 || !rows[1].FromCache || rows[0].Error != "dev is missing" {
		t.Fatalf("failure removed cache: %+v", rows)
	}
	if m.fleetTree.hosts[1].expanded {
		t.Fatal("search changed saved expansion")
	}
	m.filter = ""
	if len(m.visibleFleet()) != 4 {
		t.Fatal("clearing filter did not restore collapsed host")
	}
}

func TestFleetTreeAsyncSelectionSurvivesOffViewAndChildRemoval(t *testing.T) {
	for _, offView := range []bool{false, true} {
		t.Run(fmt.Sprint(offView), func(t *testing.T) {
			first, second := treeSnapshot("alpha", "one"), treeSnapshot("beta", "selected")
			hosts := treeHosts()
			hosts.Hosts[1].Cached = &first
			hosts.Hosts[2].Cached = &second
			m := treeModel(Actions{})
			m, _ = treeAccept(m, hosts)
			m.copyFleetHosts()
			m.fleetTree.hosts[1].expanded = true
			m.fleetTree.hosts[2].expanded = true
			treeSelect(t, &m, "b", "/src/selected")
			if offView {
				m.view = ViewRepos
			}
			newRepos := []RepoRow{{Repo: repo.Repo{Name: "another-local", Path: "/src/another"}, GitKnown: true}, {Repo: repo.Repo{Name: "local-repo", Path: "/src/local-repo"}, GitKnown: true}}
			m, _ = treeSend(m, reposMsg{rows: newRepos, valid: true})
			probe := m
			probe.view = ViewFleet
			selected, _ := probe.currentFleet()
			if selected.Repository == nil || selected.Repository.Name != "selected" {
				t.Fatalf("offview update moved selection: %+v", selected)
			}
			m.copyFleetHosts()
			m.fleetTree.hosts[2].loading = true
			m.fleetTree.hosts[2].request = 8
			m, _ = treeSend(m, fleetHostMsg{key: "b", endpoint: "b1", request: 8, result: treeSnapshot("beta")})
			probe = m
			probe.view = ViewFleet
			selected, _ = probe.currentFleet()
			if selected.HostKey != "b" || selected.Repository != nil {
				t.Fatalf("removed child did not select parent: %+v", selected)
			}
		})
	}
}

func TestFleetTreeFilterAndEmptyMenuNeverFetch(t *testing.T) {
	reads := 0
	cached := treeSnapshot("alpha", "find-me")
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	m := treeModel(Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		reads++
		return treeSnapshot(d.Name), nil
	}})
	m, _ = treeAccept(m, hosts)
	m.filter = "find-me"
	rows := m.visibleFleet()
	if len(rows) != 2 || rows[0].HostKey != "a" || reads != 0 {
		t.Fatalf("filter=%+v reads=%d", rows, reads)
	}
	m.filter = "no-matches"
	next, cmd := m.openActionMenuCommand()
	m = next.(Model)
	if cmd != nil {
		t.Fatal("empty menu queried host metadata")
	}
	index := -1
	for i := 0; i < m.overlay.optionCount; i++ {
		if m.overlay.options[i].action == listActionFleetRefreshAll {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("no update-all under zero-match filter")
	}
	m.overlay.optionIndex = index
	next, cmd = m.runOverlayAction()
	m = treeRun(t, next.(Model), cmd)
	if reads != 2 {
		t.Fatalf("explicit all-host refresh reads=%d", reads)
	}
}

func TestFleetTreeHostMenuIsLazyAndEndpointBound(t *testing.T) {
	lists, runs := 0, 0
	m := treeModel(Actions{ListFleetHostActions: func(context.Context, FleetHostDescriptor) ([]FleetHostAction, error) {
		lists++
		return []FleetHostAction{{ID: "ssh", Label: "SSH"}}, nil
	}, RunFleetHostAction: func(context.Context, FleetHostDescriptor, string) (*exec.Cmd, error) { runs++; return nil, nil }})
	m, _ = treeAccept(m, treeHosts())
	if lists != 0 {
		t.Fatal("descriptor read fetched action metadata")
	}
	treeSelect(t, &m, "a", "")
	next, command := m.openActionMenuCommand()
	m = next.(Model)
	m = treeRun(t, m, command)
	if lists != 1 {
		t.Fatal("menu did not fetch metadata")
	}
	index := -1
	for i := 0; i < m.overlay.optionCount; i++ {
		if m.overlay.options[i].fleetID == "ssh" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing dynamic action")
	}
	m.overlay.optionIndex = index
	hosts := treeHosts()
	hosts.Hosts[1].EndpointID = "replaced"
	m.acceptFleetHosts(hosts)
	next, command = m.runOverlayAction()
	m = treeRun(t, next.(Model), command)
	if runs != 0 || m.err == nil {
		t.Fatal("stale host menu executed")
	}
}

func TestFleetTreeLocalProjectionKeepsIdentityAndUnknownGit(t *testing.T) {
	m := treeModel(Actions{})
	m.repos[0].GitKnown = false
	m.repos[0].LastActivity = time.Now().Add(-time.Hour)
	m.repos[0].Topology.Remotes = []gitx.RemoteInfo{{Name: "origin", FetchURLs: []string{"https://github.com/acme/local-repo.git"}}}
	m, _ = treeAccept(m, treeHosts())
	row := m.visibleFleet()[1]
	if row.GitKnown || row.Repository.LastActivity.IsZero() || len(row.Repository.RemoteIdentities) != 1 {
		t.Fatalf("local projection=%+v", row)
	}
	m.fleetCursor = 1
	if !strings.Contains(m.renderDetail(), "unknown") {
		t.Fatal(m.renderDetail())
	}
	for _, width := range []int{60, 80, 120, 160} {
		m.width = width
		if got := m.View(); !strings.Contains(got, "FLEET") && !strings.Contains(got, "3FLT") {
			t.Fatalf("width %d: %s", width, got)
		}
	}
}

func TestFleetTreeDisablingBackgroundCancelsOnlyAutomaticRead(t *testing.T) {
	m := treeModel(Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		return treeSnapshot(d.Name), nil
	}})
	m, _ = treeAccept(m, treeHosts())
	m, automatic := treeSend(m, fleetWarmupMsg{})
	m.enqueueFleetRead("b", true, false)
	explicit := m.pumpFleetReads()
	if automatic == nil || explicit == nil {
		t.Fatal("expected concurrent explicit and automatic reads")
	}
	oldRequest := m.fleetTree.hosts[1].request
	m.setFleetBackgroundRefresh(false)
	if m.fleetTree.hosts[1].loading || !m.fleetTree.hosts[2].loading || m.fleetTree.hosts[1].request == oldRequest {
		t.Fatal("disable changed wrong requests")
	}
	m = treeRun(t, m, automatic)
	if m.fleetTree.hosts[1].result.Snapshot != nil {
		t.Fatal("canceled automatic result was accepted")
	}
	m = treeRun(t, m, explicit)
	if m.fleetTree.hosts[2].result.Snapshot == nil {
		t.Fatal("explicit result was dropped")
	}
}

func TestFleetTreeAuthenticatedRefreshAcceptsCacheWithoutNewProbe(t *testing.T) {
	probes := 0
	old := treeSnapshot("alpha", "old")
	fresh := treeSnapshot("alpha", "authenticated")
	// Host clock movement must not defeat an explicitly completed refresh.
	fresh.Snapshot.GeneratedAt = old.Snapshot.GeneratedAt.Add(-time.Hour)
	fresh.FromCache = true
	m := treeModel(Actions{LoadFleetHostCache: func(_ context.Context, d FleetHostDescriptor) (*fleet.HostResult, bool, error) {
		if d.Key == "a" {
			return &fresh, true, nil
		}
		return nil, false, nil
	}, LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		probes++
		return treeSnapshot(d.Name), nil
	}}).WithFleetBackgroundRefresh(false)
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &old
	m, _ = treeAccept(m, hosts)
	m.actions.LoadFleetHosts = func(context.Context) (FleetHostsResult, error) { return treeHosts(), nil }
	host := m.fleetTree.hosts[1].descriptor
	m, cmd := treeSend(m, fleetProcessDoneMsg{host: host, action: "authenticated-refresh"})
	m = treeRun(t, m, cmd)
	if probes != 0 || m.fleetTree.hosts[1].result.Snapshot.Repositories[0].Name != "authenticated" {
		t.Fatal("authenticated cache was lost or reprobed")
	}
}

func TestFleetTreeModelCopiesDoNotShareExpansionOrRequests(t *testing.T) {
	original := treeModel(Actions{LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		return treeSnapshot(d.Name), nil
	}})
	original, _ = treeAccept(original, treeHosts())
	treeSelect(t, &original, "a", "")
	next, _ := original.toggleFleetHost()
	changed := next.(Model)
	if original.fleetTree.hosts[1].expanded || original.fleetTree.hosts[1].loading || !changed.fleetTree.hosts[1].expanded {
		t.Fatal("value copy shared mutable host state")
	}
}

func TestFleetTreeOversizedActionListPreservesDisplayedIdentity(t *testing.T) {
	m := treeModel(Actions{ListFleetHostActions: func(context.Context, FleetHostDescriptor) ([]FleetHostAction, error) {
		items := make([]FleetHostAction, 64)
		for i := range items {
			items[i] = FleetHostAction{ID: fmt.Sprintf("action-%d", i), Label: fmt.Sprintf("label-%d", i)}
		}
		return items, nil
	}})
	m, _ = treeAccept(m, treeHosts())
	treeSelect(t, &m, "a", "")
	next, cmd := m.openActionMenuCommand()
	m = treeRun(t, next.(Model), cmd)
	if m.overlay.optionCount != len(m.overlay.options) {
		t.Fatalf("option count=%d", m.overlay.optionCount)
	}
	for i := 0; i < m.overlay.optionCount; i++ {
		option := m.overlay.options[i]
		if option.fleetID != "" && strings.TrimPrefix(option.fleetID, "action-") != strings.TrimPrefix(option.label, "label-") {
			t.Fatalf("mismatched displayed action: %+v", option)
		}
	}
}

func TestFleetTreeUnknownRemoteGitNeverLooksClean(t *testing.T) {
	failed := false
	for _, observation := range []*bool{nil, &failed} {
		t.Run(fmt.Sprint(observation == nil), func(t *testing.T) {
			remote := treeSnapshot("alpha", "legacy-or-failed")
			remote.Snapshot.Repositories[0].GitKnown = observation
			hosts := treeHosts()
			hosts.Hosts[1].Cached = &remote
			m := treeModel(Actions{})
			m, _ = treeAccept(m, hosts)
			m.filter = "legacy-or-failed"
			rows := m.visibleFleet()
			if len(rows) != 2 || rows[1].GitKnown || fleetCell(rows[1], "git").known {
				t.Fatalf("unknown Git promoted to known: %+v", rows)
			}
			m.fleetCursor = 1
			if detail := m.renderDetail(); !strings.Contains(detail, "unknown") {
				t.Fatal(detail)
			}
			if strings.Contains(rows[1].searchText(), rows[1].Repository.Status.Summary()) {
				t.Fatal("unknown status indexed as clean")
			}
		})
	}
}
