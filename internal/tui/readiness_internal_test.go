package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestRepositoryRematchUsesCurrentCatalogTryInventory(t *testing.T) {
	model := New(Actions{}, nil, nil)
	model.tries = []TryRow{{Item: experiment.Item{
		ID: "try-1", Name: "scratch", RemoteIdentity: "github.com/owner/scratch",
		Live: experiment.LiveFacts{Present: true, CurrentPath: "/tries/scratch"},
	}}}
	model.remotes = []RemoteRow{{Repo: forge.RemoteRepo{
		Forge: forge.GitHub, Name: "scratch", FullName: "owner/scratch",
		CloneURL: "https://github.com/owner/scratch.git",
	}}}
	model.matchRemoteLocals()
	if got := model.remotes[0]; got.LocalPath != "/tries/scratch" || got.LocalKind != catalog.KindTry {
		t.Fatalf("catalog-only Try was not matched: %+v", got)
	}
	model.tries = nil
	model.matchRemoteLocals()
	if got := model.remotes[0]; got.LocalPath != "" || got.LocalKind != "" {
		t.Fatalf("removed catalog Try remained matched: %+v", got)
	}
}

func TestRemoteMatchRejectsOrdinaryAndTryCloneAmbiguity(t *testing.T) {
	model := New(Actions{}, nil, nil)
	model.repos = []RepoRow{{
		Repo:        repo.Repo{Name: "ordinary", Path: "/repos/ordinary"},
		RemoteForge: forge.GitHub, RemoteName: "owner/shared",
	}}
	model.tries = []TryRow{{Item: experiment.Item{
		ID: "try-1", Name: "scratch", RemoteIdentity: "github.com/owner/shared",
		Live: experiment.LiveFacts{Present: true, CurrentPath: "/tries/scratch"},
	}}}
	model.remotes = []RemoteRow{{Repo: forge.RemoteRepo{
		Forge: forge.GitHub, Name: "shared", FullName: "owner/shared",
		CloneURL: "https://github.com/owner/shared.git",
	}}}
	model.matchRemoteLocals()
	if got := model.remotes[0]; got.LocalPath != "" || got.LocalKind != "" {
		t.Fatalf("ambiguous ordinary/Try clones selected one checkout: %+v", got)
	}
}

func TestStaleNoteOverlayStillResolvesRepositoryGeneration(t *testing.T) {
	fresh := RepoRow{Repo: repo.Repo{Name: "fresh", Path: "/src/fresh"}, SizeTarget: diskusage.Plain("/src/fresh")}
	sizeLoads := 0
	actions := Actions{
		ReloadRepos: func(context.Context) ([]RepoRow, error) { return []RepoRow{fresh}, nil },
		Notes:       NoteActions{List: func(context.Context, NoteTarget) ([]*note.Note, error) { return nil, nil }},
		Sizes: SizeActions{Start: func(context.Context, []diskusage.Target, bool) diskusage.Load {
			sizeLoads++
			return diskusage.Load{}
		}},
	}
	model := New(actions, nil, []RepoRow{{Repo: repo.Repo{Name: "old", Path: "/src/old"}}})
	model.mode = modeNoteBrowse
	model.noteTarget = NoteTarget{Repo: repo.Repo{Name: "old", Path: "/src/old"}}
	model, command := model.beginNoteLoad(true)
	model.mode = modeList
	model.noteRequest++

	updated, _ := model.Update(command())
	got := updated.(Model)
	if got.viewLoad(ViewRepos).loading || len(got.repos) != 1 || got.repos[0].Repo.Name != "fresh" || sizeLoads != 1 {
		t.Fatalf("stale note result stranded REPOS: loading=%v repos=%+v sizeLoads=%d",
			got.viewLoad(ViewRepos).loading, got.repos, sizeLoads)
	}
}

func TestTryOnlyRefreshDoesNotCancelRepositoryReload(t *testing.T) {
	actions := Actions{
		Tries: TryActions{Reload: func(ctx context.Context, _ bool) ([]TryRow, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		ReloadRepos: func(ctx context.Context) ([]RepoRow, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []RepoRow{{Repo: repo.Repo{Name: "fresh", Path: "/src/fresh"}}}, nil
		},
	}
	model := New(actions, nil, nil)
	model.beginTryLoads(true, loadAction)
	old := model.reloadTries(true)
	model.beginTryLoads(false, loadRefresh)
	updated, _ := model.Update(old())
	got := updated.(Model)
	if got.viewLoad(ViewRepos).loading || len(got.repos) != 1 || got.repos[0].Repo.Name != "fresh" {
		t.Fatalf("Try cancellation contaminated REPOS: state=%+v repos=%+v", got.viewLoad(ViewRepos), got.repos)
	}
}

func TestLocalCycleCloseStartsSizesAfterOneViewWasSuperseded(t *testing.T) {
	sizeLoads := 0
	model := New(Actions{Sizes: SizeActions{Start: func(_ context.Context, targets []diskusage.Target, _ bool) diskusage.Load {
		sizeLoads++
		if len(targets) != 1 {
			t.Fatalf("size targets = %d", len(targets))
		}
		return diskusage.Load{}
	}}}, nil, []RepoRow{{
		Repo: repo.Repo{Name: "api", Path: "/src/api"}, SizeTarget: diskusage.Plain("/src/api"),
	}})
	model.localGeneration = 3
	model.loads[int(ViewTries)].generation = 9
	updated, _ := model.Update(localMsg{done: true, load: LocalLoad{Request: LocalLoadRequest{
		CycleGeneration: 3, TriesGeneration: 1,
	}}})
	_ = updated.(Model)
	if sizeLoads != 1 {
		t.Fatalf("shared cycle close size loads = %d", sizeLoads)
	}
}

func TestOnlyAcceptedConfigGenerationCommitsPreparedState(t *testing.T) {
	model := New(Actions{}, nil, nil)
	model.configGeneration = 2
	commits := 0
	stale := configMsg{generation: 1, update: ConfigUpdate{Apply: func() { commits++ }}}
	updated, _ := model.Update(stale)
	model = updated.(Model)
	if commits != 0 {
		t.Fatal("stale config generation committed prepared state")
	}
	current := configMsg{generation: 2, update: ConfigUpdate{Apply: func() { commits++ }}}
	updated, _ = model.Update(current)
	if commits != 1 {
		t.Fatalf("accepted config commits = %d", commits)
	}
}

func TestStartingRepositoryGenerationSupersedesDependentFleet(t *testing.T) {
	model := New(Actions{
		ReloadFleetWithRepos: func(context.Context, []RepoRow) ([]FleetRow, error) { return nil, nil },
	}, nil, []RepoRow{{Repo: repo.Repo{Name: "old", Path: "/src/old"}}})
	model.view = ViewFleet
	oldGeneration := model.beginViewLoad(ViewFleet, loadVisit)
	oldContext := model.viewContext(ViewFleet)
	model.beginViewLoad(ViewRepos, loadRefresh)
	if oldContext.Err() == nil {
		t.Fatal("new REPOS generation did not cancel dependent FLEET")
	}
	fleet := model.viewLoad(ViewFleet)
	if fleet.generation <= oldGeneration || !fleet.loading || fleet.freshness != "" {
		t.Fatalf("dependent fleet state = %+v", fleet)
	}
}

func TestFleetFailsClosedWhenRepositoryInventoryAlreadyFailed(t *testing.T) {
	model := New(Actions{
		ReloadFleetWithRepos: func(context.Context, []RepoRow) ([]FleetRow, error) { return nil, nil },
	}, nil, nil)
	generation := model.beginViewLoad(ViewRepos, loadInitial)
	repoErr := errors.New("repo discovery failed")
	model.applyViewResult(ViewRepos, generation, false, "", "", 0, repoErr, false)
	model.view = ViewFleet
	updated, command := model.afterViewSwitch()
	got := updated.(Model)
	if command != nil || got.viewLoad(ViewFleet).loading || got.viewError(ViewFleet) == nil {
		t.Fatalf("fleet waited forever after terminal REPOS failure: state=%+v err=%v cmd=%v",
			got.viewLoad(ViewFleet), got.viewError(ViewFleet), command)
	}
}
