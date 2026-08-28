package gitx_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestRecoveryTopologyReportsRepositoryWithoutRemote(t *testing.T) {
	repository := gittest.New(t)
	topology, err := gitx.RecoveryTopologyOf(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if topology.HasRemote() || len(topology.Remotes) != 0 {
		t.Fatalf("unexpected remotes: %+v", topology.Remotes)
	}
	if !slices.Equal(topology.LocalOnlyBranches, []string{"main"}) {
		t.Fatalf("local-only branches = %v", topology.LocalOnlyBranches)
	}
	if got := topology.Summary(); got != "none · local:1" {
		t.Errorf("Summary = %q", got)
	}
}

func TestRecoveryTopologySeparatesMultipleRemotesAndBranchUpstreams(t *testing.T) {
	repository := gittest.New(t)
	origin := repository.WithRemote()
	upstream := filepath.Join(filepath.Dir(repository.Root), "upstream.git")
	repository.GitIn(filepath.Dir(repository.Root), "init", "--bare", "--initial-branch=main", upstream)
	repository.Git("remote", "add", "upstream", upstream)

	repository.Git("switch", "-c", "feature/local", "main")
	repository.Git("switch", "-c", "release", "main")
	repository.Git("push", "-u", "upstream", "release")

	topology, err := gitx.RecoveryTopologyOf(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !topology.MultipleRemotes() || !topology.MultipleUpstreams() {
		t.Fatalf("multiple topology not detected: %+v", topology)
	}
	if !slices.Equal(topology.UpstreamRemotes, []string{"origin", "upstream"}) {
		t.Errorf("upstream remotes = %v", topology.UpstreamRemotes)
	}
	if !slices.Equal(topology.LocalOnlyBranches, []string{"feature/local"}) {
		t.Errorf("local-only branches = %v", topology.LocalOnlyBranches)
	}
	if got := topology.Summary(); got != "origin,upstream · local:1" {
		t.Errorf("Summary = %q", got)
	}
	if len(topology.Remotes) != 2 || topology.Remotes[0].Name != "origin" || topology.Remotes[1].Name != "upstream" {
		t.Fatalf("remote ordering = %+v", topology.Remotes)
	}
	if !slices.Contains(topology.Remotes[0].FetchURLs, origin) || !slices.Contains(topology.Remotes[0].PushURLs, origin) {
		t.Errorf("origin URLs = %+v", topology.Remotes[0])
	}
}

func TestRecoveryTopologyTreatsLocalBranchTrackingAsLocalOnly(t *testing.T) {
	repository := gittest.New(t)
	repository.Branch("child")
	repository.Git("branch", "--set-upstream-to=main", "child")
	topology, err := gitx.RecoveryTopologyOf(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(topology.LocalOnlyBranches, "child") || !slices.Contains(topology.LocalOnlyBranches, "main") {
		t.Fatalf("local branch upstream was treated as remote recovery: %+v", topology)
	}
	for _, branch := range topology.Branches {
		if branch.Branch == "child" && branch.Remote != "." {
			t.Errorf("local upstream remote = %q, want .", branch.Remote)
		}
	}
}

func TestRecoveryTopologyHonorsCancellation(t *testing.T) {
	repository := gittest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gitx.RecoveryTopologyOf(ctx, repository.Root); err == nil {
		t.Fatal("cancelled topology probe succeeded")
	}
}
