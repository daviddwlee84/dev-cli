package gitx_test

import (
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"testing"
)

func TestNetworkCloneURL(t *testing.T) {
	for _, s := range []string{"https://host/org/repo.git", "ssh://git@host/org/repo.git", "git@host:org/repo.git", "host.example:repo", "git://host/repo"} {
		if err := gitx.NetworkCloneURL(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "/local/repo", "../repo", "file:///repo", "C:/repo", "https://user@host/repo", "https://user:password@host/repo", "ssh://user:password@host/repo", "https://host/repo?token=example", "https://host/repo#fragment", "https://host/repo\nnext", "-bad:repo", "ext::anything"} {
		if err := gitx.NetworkCloneURL(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestFetchCloneSourcesNoNetworkAndOriginPreference(t *testing.T) {
	r := gittest.New(t)
	r.Git("remote", "add", "origin", "git@host.example:team/repo.git")
	r.Git("remote", "add", "upstream", "https://host.example/other/repo.git")
	r.Git("remote", "add", "local", "/local/repo")
	s, err := gitx.FetchCloneSources(t.Context(), r.Root)
	if err != nil || len(s) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	p := gitx.PreferredCloneSources(s)
	if len(p) != 1 || p[0].Remote != "origin" {
		t.Fatal(p)
	}
}
