package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

const (
	forkTestSource = "https://github.com/upstream/widget.git"
	forkTestTarget = "https://github.com/me/widget.git"
)

type forkTestForge struct {
	plan       forge.ForkPlan
	planCalls  int
	applyCalls int
	sources    []string
	onApply    func()
	applyError error
	unknown    bool
}

func newForkTestForge(exists bool) *forkTestForge {
	repository := func(owner string, id int64) forge.ForkRepository {
		return forge.ForkRepository{
			ID: id, Host: "github.com", FullName: owner + "/widget", Owner: owner,
			URL: "https://github.com/" + owner + "/widget", CloneURL: "https://github.com/" + owner + "/widget.git",
			SSHURL: "git@github.com:" + owner + "/widget.git", DefaultBranch: "main", NetworkID: 1,
		}
	}
	f := &forkTestForge{plan: forge.ForkPlan{Source: repository("upstream", 1), Fork: repository("me", 2), Owner: "me", Exists: exists}}
	f.plan.Fork.IsFork = true
	if !exists {
		f.plan.Fork.ID = 0
	}
	return f
}

func (f *forkTestForge) Kind() forge.Kind { return forge.GitHub }
func (f *forkTestForge) Bin() string      { return "fixture" }
func (f *forkTestForge) Available() bool  { return true }
func (f *forkTestForge) CloneURL(ref string) string {
	return "https://github.com/" + ref + ".git"
}
func (f *forkTestForge) CreatePR(context.Context, string, forge.PRRequest) (string, error) {
	panic("fork must not create a PR")
}
func (f *forkTestForge) CreateRepo(context.Context, string, forge.RepoRequest) (string, error) {
	panic("fork must use optional fork interface")
}
func (f *forkTestForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	panic("fork must not enumerate unrelated repositories")
}
func (f *forkTestForge) PlanFork(_ context.Context, source string) (forge.ForkPlan, error) {
	f.planCalls++
	f.sources = append(f.sources, source)
	return f.plan, nil
}
func (f *forkTestForge) ApplyFork(_ context.Context, plan forge.ForkPlan) (forge.ForkResult, error) {
	f.applyCalls++
	if f.onApply != nil {
		f.onApply()
	}
	if f.applyError != nil {
		return forge.ForkResult{Plan: plan, Unknown: f.unknown}, f.applyError
	}
	result := forge.ForkResult{Plan: plan, Reused: plan.Exists, Created: !plan.Exists}
	result.Plan.Exists = true
	result.Plan.Fork.ID = 2
	f.plan = result.Plan
	return result, nil
}

func isolateForkGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func forkGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(t.Context(), path, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func forkCheckoutFixture(t *testing.T, remote, url string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checkout")
	if _, err := Acquire(t.Context(), AcquireRequest{Kind: AcquireNew, Name: "fixture", Destination: path}); err != nil {
		t.Fatal(err)
	}
	forkGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "base")
	if remote != "" {
		forkGit(t, path, "remote", "add", remote, url)
	}
	return path
}

func TestForkPreservesPullTargetsAndDefaultsPushToPersonalFork(t *testing.T) {
	isolateForkGit(t)
	path := forkCheckoutFixture(t, "origin", forkTestSource)
	forkGit(t, path, "config", "branch.main.remote", "origin")
	forkGit(t, path, "config", "branch.main.merge", "refs/heads/main")
	forkGit(t, path, "config", "branch.main.pushRemote", "origin")
	forkGit(t, path, "config", "remote.pushDefault", "origin")
	beforeHEAD := forkGit(t, path, "rev-parse", "HEAD")
	beforeConfig := forkGit(t, path, "config", "--local", "--null", "--list")
	f := newForkTestForge(false)
	plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if f.applyCalls != 0 || forkGit(t, path, "config", "--local", "--null", "--list") != beforeConfig {
		t.Fatal("preview mutated the fork or repository")
	}
	result, err := ApplyFork(t.Context(), f, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fork.Created || result.NoOp || f.applyCalls != 1 {
		t.Fatalf("result = %+v", result)
	}
	for key, want := range map[string]string{
		"remote.origin.url": forkTestTarget, "remote.upstream.url": forkTestSource,
		"branch.main.remote": "upstream", "branch.main.merge": "refs/heads/main",
		"branch.main.pushremote": "origin", "remote.pushdefault": "origin",
	} {
		if got := forkGit(t, path, "config", "--get", key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := forkGit(t, path, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Fatal("fork changed the checked-out commit")
	}
	plan, err = PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil || !plan.NoOp {
		t.Fatalf("second plan = %+v, %v", plan, err)
	}
	normalized := forkGit(t, path, "config", "--local", "--null", "--list")
	result, err = ApplyFork(t.Context(), f, plan)
	if err != nil || !result.NoOp || !reflect.DeepEqual(result.Completed, []string{"fork_reused"}) {
		t.Fatalf("second result = %+v, %v", result, err)
	}
	if forkGit(t, path, "config", "--local", "--null", "--list") != normalized {
		t.Fatal("normalized retry rewrote configuration")
	}
}

func TestForkExistingRemoteLayouts(t *testing.T) {
	for _, test := range []struct {
		name, remote, url, selected, wantPull, wantSource, wantTarget string
	}{
		{"source SSH", "origin", "git@github.com:upstream/widget.git", "", "upstream", "git@github.com:upstream/widget.git", "git@github.com:me/widget.git"},
		{"upstream only", "upstream", forkTestSource, "", "upstream", forkTestSource, forkTestTarget},
		{"personal only", "origin", forkTestTarget, "", "origin", forkTestSource, forkTestTarget},
		{"explicit source", "vendor", forkTestSource, "vendor", "upstream", forkTestSource, forkTestTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateForkGit(t)
			path := forkCheckoutFixture(t, test.remote, test.url)
			forkGit(t, path, "config", "branch.main.remote", test.remote)
			forkGit(t, path, "config", "branch.main.merge", "refs/heads/main")
			if test.remote == "upstream" {
				// A retry after rename/add was interrupted can already have its
				// final push target configured before origin is restored.
				forkGit(t, path, "config", "remote.pushDefault", "origin")
			}
			f := newForkTestForge(true)
			plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path, SourceRemote: test.selected})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyFork(t.Context(), f, plan); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"remote.origin.url": test.wantTarget, "remote.upstream.url": test.wantSource,
				"branch.main.remote": test.wantPull,
			} {
				if got := forkGit(t, path, "config", "--get", key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestForkRejectsLocalConflictsBeforeExternalWrite(t *testing.T) {
	for _, test := range []struct {
		name, want string
		setup      func(*testing.T, string)
	}{
		{"upstream conflict", "conflicts", func(t *testing.T, p string) {
			forkGit(t, p, "remote", "add", "upstream", "https://github.com/other/widget.git")
		}},
		{"duplicate source", "both identify", func(t *testing.T, p string) { forkGit(t, p, "remote", "add", "upstream", forkTestSource) }},
		{"origin conflict", "conflicts", func(t *testing.T, p string) {
			forkGit(t, p, "remote", "add", "upstream", forkTestSource)
			forkGit(t, p, "remote", "set-url", "origin", "https://github.com/other/widget.git")
		}},
		{"multiple URLs", "exactly one URL", func(t *testing.T, p string) { forkGit(t, p, "config", "--add", "remote.origin.url", forkTestTarget) }},
		{"multiple push URLs", "multiple push URLs", func(t *testing.T, p string) {
			forkGit(t, p, "config", "--add", "remote.origin.pushurl", forkTestSource)
			forkGit(t, p, "config", "--add", "remote.origin.pushurl", forkTestSource)
		}},
		{"foreign push URL", "different repository", func(t *testing.T, p string) { forkGit(t, p, "config", "remote.origin.pushurl", forkTestTarget) }},
		{"custom fetch", "fetch refspec", func(t *testing.T, p string) {
			forkGit(t, p, "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main")
		}},
		{"custom push", "push refspecs", func(t *testing.T, p string) { forkGit(t, p, "config", "remote.origin.push", "main:main") }},
		{"mirror", "mirror settings", func(t *testing.T, p string) { forkGit(t, p, "config", "remote.origin.mirror", "true") }},
		{"unrelated branch push", "unrelated remote", func(t *testing.T, p string) { forkGit(t, p, "config", "branch.main.pushRemote", "third") }},
		{"unrelated default push", "unrelated remote", func(t *testing.T, p string) { forkGit(t, p, "config", "remote.pushDefault", "third") }},
		{"multiple branch push", "multiple values", func(t *testing.T, p string) {
			forkGit(t, p, "config", "--add", "branch.main.pushRemote", "origin")
			forkGit(t, p, "config", "--add", "branch.main.pushRemote", "origin")
		}},
		{"remote without URL", "exactly one URL", func(t *testing.T, p string) {
			forkGit(t, p, "config", "remote.upstream.fetch", "+refs/heads/*:refs/remotes/upstream/*")
		}},
		{"rewritten source", "URL rewriting", func(t *testing.T, p string) {
			forkGit(t, p, "config", "url.https://github.com/other/.insteadOf", "https://github.com/upstream/")
		}},
		{"rewritten future fork", "URL rewriting", func(t *testing.T, p string) {
			forkGit(t, p, "config", "url.https://github.com/other/.pushInsteadOf", "https://github.com/me/")
		}},
		{"inherited source", "inherited configuration", func(t *testing.T, p string) {
			included := filepath.Join(t.TempDir(), "included")
			if err := os.WriteFile(included, []byte("[remote \"origin\"]\n\ttagOpt = --no-tags\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			forkGit(t, p, "config", "include.path", included)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateForkGit(t)
			path := forkCheckoutFixture(t, "origin", forkTestSource)
			test.setup(t, path)
			before := forkGit(t, path, "config", "--local", "--null", "--list")
			f := newForkTestForge(false)
			selected := ""
			if test.name == "upstream conflict" {
				selected = "origin"
			}
			_, err := PlanFork(t.Context(), f, ForkRequest{Path: path, SourceRemote: selected})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if f.applyCalls != 0 || forkGit(t, path, "config", "--local", "--null", "--list") != before {
				t.Fatal("conflict changed repository or fork")
			}
		})
	}
}

func TestForkRejectsChangedAuthorityBeforeMutation(t *testing.T) {
	for _, mutation := range []string{"plan", "config", "identity"} {
		t.Run(mutation, func(t *testing.T) {
			isolateForkGit(t)
			path := forkCheckoutFixture(t, "origin", forkTestSource)
			f := newForkTestForge(false)
			plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "plan":
				plan.ForkURL = "https://github.com/other/widget.git"
			case "config":
				forkGit(t, path, "config", "branch.main.pushRemote", "third")
			case "identity":
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if _, err := Acquire(t.Context(), AcquireRequest{Kind: AcquireNew, Name: "replacement", Destination: path}); err != nil {
					t.Fatal(err)
				}
				forkGit(t, path, "remote", "add", "origin", forkTestSource)
			}
			if _, err := ApplyFork(t.Context(), f, plan); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("error = %v", err)
			}
			if f.applyCalls != 0 {
				t.Fatal("stale plan reached forge write")
			}
		})
	}
}

func TestForkRetainsRemoteResultWhenLocalConfigChangesDuringRequest(t *testing.T) {
	isolateForkGit(t)
	path := forkCheckoutFixture(t, "origin", forkTestSource)
	f := newForkTestForge(false)
	plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	f.onApply = func() { forkGit(t, path, "config", "branch.main.pushRemote", "third") }
	result, err := ApplyFork(t.Context(), f, plan)
	if err == nil || !strings.Contains(err.Error(), "retained") || !result.Fork.Created || !reflect.DeepEqual(result.Completed, []string{"fork_created"}) {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if forkGit(t, path, "remote", "get-url", "origin") != forkTestSource {
		t.Fatal("local remotes changed after stale-config detection")
	}
}

func TestForkUnknownRemoteOutcomeStopsWithoutRetry(t *testing.T) {
	isolateForkGit(t)
	path := forkCheckoutFixture(t, "origin", forkTestSource)
	f := newForkTestForge(false)
	f.applyError, f.unknown = errors.New("connection lost after create"), true
	plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyFork(t.Context(), f, plan)
	if err == nil || !result.Fork.Unknown || f.applyCalls != 1 || len(result.Completed) != 0 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if forkGit(t, path, "remote", "get-url", "origin") != forkTestSource {
		t.Fatal("unknown remote result changed local remotes")
	}
}

func TestForkClonePreflightRunsBeforeForgeReads(t *testing.T) {
	isolateForkGit(t)
	checkout := forkCheckoutFixture(t, "origin", forkTestSource)
	for _, destination := range []string{checkout, filepath.Join(checkout, "nested", "clone")} {
		f := newForkTestForge(false)
		_, err := PlanFork(t.Context(), f, ForkRequest{CloneRef: "upstream/widget", Destination: destination})
		if err == nil || f.planCalls != 0 || f.applyCalls != 0 {
			t.Fatalf("destination = %s, calls = %d/%d, error = %v", destination, f.planCalls, f.applyCalls, err)
		}
	}
}

func TestForkCloneAcquiresCurrentSourceAndRetainsPartialEffects(t *testing.T) {
	for _, failClone := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "clone failure"}[failClone], func(t *testing.T) {
			isolateForkGit(t)
			source := forkCheckoutFixture(t, "", "")
			destination := filepath.Join(t.TempDir(), "clones", "widget")
			f := newForkTestForge(false)
			plan, err := PlanFork(t.Context(), f, ForkRequest{CloneRef: "upstream/widget", Destination: destination, Submodules: "none"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(destination)); !os.IsNotExist(err) {
				t.Fatalf("preview created clone parent: %v", err)
			}
			called := false
			result, err := applyFork(t.Context(), f, plan, func(ctx context.Context, request AcquireRequest) (AcquireResult, error) {
				called = true
				if f.applyCalls != 1 || request.CloneRef != forkTestSource || request.CloneRemote != "origin" || request.Submodules != "none" {
					t.Fatalf("acquisition must follow fork and use upstream: %+v", request)
				}
				if failClone {
					return AcquireResult{Path: request.Destination}, errors.New("fixture clone unavailable")
				}
				// Native Git performs the clone; only the network endpoint is
				// substituted with this fixture's current source repository.
				request.CloneRef = source
				acquired, err := Acquire(ctx, request)
				if err == nil {
					forkGit(t, acquired.Path, "remote", "set-url", "origin", forkTestSource)
				}
				return acquired, err
			})
			if !called || !result.Fork.Created || f.applyCalls != 1 {
				t.Fatalf("result = %+v, %v", result, err)
			}
			if failClone {
				if err == nil || !strings.Contains(err.Error(), "retained") || !reflect.DeepEqual(result.Completed, []string{"fork_created"}) {
					t.Fatalf("failure = %+v, %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Acquisition == nil || !result.Acquisition.Cloned || forkGit(t, destination, "rev-parse", "HEAD") != forkGit(t, source, "rev-parse", "HEAD") {
				t.Fatal("clone did not preserve current upstream commit")
			}
			if forkGit(t, destination, "config", "branch.main.remote") != "upstream" || forkGit(t, destination, "remote", "get-url", "origin") != forkTestTarget {
				t.Fatal("clone did not normalize pull and push remotes")
			}
		})
	}
}

func TestForkInterruptedRenameCanBeResumed(t *testing.T) {
	isolateForkGit(t)
	path := forkCheckoutFixture(t, "origin", forkTestSource)
	forkGit(t, path, "config", "branch.main.remote", "origin")
	partial := ForkResult{}
	err := applyForkActions(t.Context(), path, []forkAction{
		{"remote_renamed:origin:upstream", []string{"remote", "rename", "origin", "upstream"}},
		{"fixture failure", []string{"not-a-git-subcommand"}},
	}, &partial)
	if err == nil || !reflect.DeepEqual(partial.Completed, []string{"remote_renamed:origin:upstream"}) {
		t.Fatalf("partial = %+v, %v", partial, err)
	}
	f := newForkTestForge(true)
	plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFork(t.Context(), f, plan); err != nil {
		t.Fatal(err)
	}
	if forkGit(t, path, "remote", "get-url", "origin") != forkTestTarget || forkGit(t, path, "config", "branch.main.remote") != "upstream" {
		t.Fatal("resume did not retain renamed pull target and add fork")
	}
}

func TestForkCloneDestinationBecomesOccupiedBeforeApply(t *testing.T) {
	isolateForkGit(t)
	destination := filepath.Join(t.TempDir(), "widget")
	f := newForkTestForge(false)
	plan, err := PlanFork(t.Context(), f, ForkRequest{CloneRef: "upstream/widget", Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFork(t.Context(), f, plan); err == nil || f.applyCalls != 0 {
		t.Fatalf("occupied destination reached external write: %v", err)
	}
}

func TestForkCloneInheritedConflictsFailBeforeForgeReads(t *testing.T) {
	for _, contents := range []string{
		"[remote \"origin\"]\n\turl = https://github.com/other/widget.git\n",
		"[remote]\n\tpushDefault = third\n",
		"[branch \"main\"]\n\tpushRemote = third\n",
		"[branch \"main\"]\n\tremote = origin\n",
	} {
		t.Run(strings.ReplaceAll(contents, "\n", " "), func(t *testing.T) {
			isolateForkGit(t)
			global := filepath.Join(t.TempDir(), "gitconfig")
			if err := os.WriteFile(global, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			f := newForkTestForge(false)
			_, err := PlanFork(t.Context(), f, ForkRequest{CloneRef: "upstream/widget", Destination: filepath.Join(t.TempDir(), "widget")})
			if err == nil || f.planCalls != 0 || f.applyCalls != 0 {
				t.Fatalf("inherited conflict reached forge: %v, reads=%d, writes=%d", err, f.planCalls, f.applyCalls)
			}
		})
	}
}

func TestForkLocalPushDefaultOverridesInheritedDefaultIdempotently(t *testing.T) {
	isolateForkGit(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[remote]\n\tpushDefault = origin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	path := forkCheckoutFixture(t, "origin", forkTestSource)
	f := newForkTestForge(true)
	plan, err := PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFork(t.Context(), f, plan); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanFork(t.Context(), f, ForkRequest{Path: path})
	if err != nil || !plan.NoOp {
		t.Fatalf("normalized local override is not idempotent: %+v, %v", plan, err)
	}
}

func TestForkAcceptsInheritedPushToPlannedUpstream(t *testing.T) {
	for _, key := range []string{"remote.pushDefault", "branch.main.pushRemote"} {
		for _, clone := range []bool{false, true} {
			t.Run(key+map[bool]string{false: "/existing", true: "/clone"}[clone], func(t *testing.T) {
				isolateForkGit(t)
				global := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(global, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_CONFIG_GLOBAL", global)
				forkGit(t, t.TempDir(), "config", "--global", key, "upstream")
				source := forkCheckoutFixture(t, "origin", forkTestSource)
				path := source
				request := ForkRequest{Path: path}
				if clone {
					path = filepath.Join(t.TempDir(), "widget")
					request = ForkRequest{CloneRef: "upstream/widget", Destination: path, Submodules: "none"}
				}
				f := newForkTestForge(true)
				plan, err := PlanFork(t.Context(), f, request)
				if err != nil {
					t.Fatal(err)
				}
				_, err = applyFork(t.Context(), f, plan, func(ctx context.Context, acquisition AcquireRequest) (AcquireResult, error) {
					acquisition.CloneRef = source
					result, err := Acquire(ctx, acquisition)
					if err == nil {
						forkGit(t, result.Path, "remote", "set-url", "origin", forkTestSource)
					}
					return result, err
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := forkGit(t, path, "config", "--local", "--get", key); got != "origin" {
					t.Fatalf("%s = %q, want origin", key, got)
				}
				if forkGit(t, path, "remote", "get-url", "upstream") != forkTestSource || forkGit(t, path, "remote", "get-url", "origin") != forkTestTarget {
					t.Fatal("planned remotes were not normalized")
				}
				plan, err = PlanFork(t.Context(), f, ForkRequest{Path: path})
				if err != nil || !plan.NoOp {
					t.Fatalf("normalized inherited upstream is not idempotent: %+v, %v", plan, err)
				}
			})
		}
	}
}
