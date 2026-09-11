package fleetnav

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
)

const checkedIdentity = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type navigationFixture struct {
	target   Target
	profiles []herdrremote.Profile
	calls    []string
	service  Service
}

func profile(id, session string, enabled bool) herdrremote.Profile {
	return herdrremote.Profile{ID: strings.Repeat(id, 32), Label: "Lab " + session, Target: "lab", Session: session, Enabled: enabled}
}

func newNavigationFixture(profiles ...herdrremote.Profile) *navigationFixture {
	f := &navigationFixture{
		target:   Target{Host: fleet.Host{Name: "Lab", SSHAlias: "lab"}, EndpointID: "endpoint"},
		profiles: append([]herdrremote.Profile(nil), profiles...),
	}
	f.service = Service{
		Resolve: func(_ context.Context, r Request) (Target, error) {
			f.calls = append(f.calls, "resolve")
			target := f.target
			target.Repository = r.Repository
			return target, nil
		},
		Capabilities: func(context.Context) (Capabilities, error) {
			f.calls = append(f.calls, "capabilities")
			return Capabilities{Installed: true, RemoteAvailable: true, Catalog: herdrremote.Inventory{Status: "ready", Profiles: append([]herdrremote.Profile(nil), f.profiles...)}}, nil
		},
		ChooseSSH: func(context.Context, string) (bool, error) {
			f.calls = append(f.calls, "choose-ssh")
			return true, nil
		},
		PickProfile: func(_ context.Context, profiles []herdrremote.Profile) (herdrremote.Profile, error) {
			f.calls = append(f.calls, "pick-profile")
			return profiles[len(profiles)-1], nil
		},
		CheckTarget: func(_ context.Context, target Target) error {
			f.calls = append(f.calls, "check-target")
			if target.EndpointID != f.target.EndpointID {
				return errors.New("endpoint changed")
			}
			return nil
		},
		CheckProfile: func(_ context.Context, selected herdrremote.Profile) error {
			f.calls = append(f.calls, "check-profile")
			for _, p := range f.profiles {
				if sameProfile(p, selected) {
					return nil
				}
			}
			return errors.New("profile changed")
		},
		EnsureProfile: func(_ context.Context, target Target, selection Selection) (EnsureResult, error) {
			f.calls = append(f.calls, "ensure-profile")
			p := selection.Profile
			if !selection.Exists {
				p = profile("f", selection.Session, true)
				p.Target = target.Host.SSHAlias
				f.profiles = append(f.profiles, p)
			} else {
				p.Enabled = true
				for i := range f.profiles {
					if f.profiles[i].ID == p.ID {
						f.profiles[i] = p
					}
				}
			}
			return EnsureResult{Status: "applied", Profile: p}, nil
		},
		CheckRepository: func(_ context.Context, target Target, session, expected string) (fleet.HerdrRepoResult, error) {
			f.calls = append(f.calls, "check-repository")
			if expected != "" && expected != checkedIdentity {
				return fleet.HerdrRepoResult{}, errors.New("repository changed")
			}
			return repoResult(target, session, "check"), nil
		},
		PrepareRepository: func(_ context.Context, target Target, session, expected string) (fleet.HerdrRepoResult, error) {
			f.calls = append(f.calls, "prepare-repository")
			if expected != checkedIdentity {
				return fleet.HerdrRepoResult{}, errors.New("unchecked repository")
			}
			return repoResult(target, session, "prepare"), nil
		},
		Attach: func(_ context.Context, _ Target, session string) error {
			f.calls = append(f.calls, "attach:"+session)
			return nil
		},
		SSH: func(context.Context, Target) error {
			f.calls = append(f.calls, "ssh")
			return nil
		},
	}
	return f
}

func repoResult(target Target, session, phase string) fleet.HerdrRepoResult {
	r := fleet.HerdrRepoResult{SchemaVersion: fleet.HerdrRepoSchemaVersion, Phase: phase, Session: session, Path: target.Repository.Path, RemoteIdentity: target.Repository.RemoteIdentity, RepositoryIdentity: checkedIdentity, RuntimeState: "ready"}
	if phase == "prepare" {
		r.Workspace, r.Surface = "herdr:w7", "workspace"
	}
	return r
}

func repoRequest(inside bool) Request {
	return Request{Host: "Lab", ExpectedEndpoint: "endpoint", InsideHerdr: inside, Repository: &fleet.OpenRequest{Path: "/srv/repo", RemoteIdentity: "github.com/team/repo"}}
}

func assertNoCalls(t *testing.T, calls []string, prefixes ...string) {
	t.Helper()
	for _, call := range calls {
		for _, prefix := range prefixes {
			if strings.HasPrefix(call, prefix) {
				t.Fatalf("unexpected %s among calls %v", call, calls)
			}
		}
	}
}

func TestHostNavigationInsideAndOutside(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inside   bool
		profiles []herdrremote.Profile
		session  string
		ensure   bool
	}{
		{"inside-enabled", true, []herdrremote.Profile{profile("1", "work", true)}, "work", false},
		{"inside-disabled", true, []herdrremote.Profile{profile("1", "work", false)}, "work", true},
		{"inside-removed", true, nil, "default", true},
		{"outside-enabled", false, []herdrremote.Profile{profile("1", "work", true)}, "work", false},
		{"outside-disabled", false, []herdrremote.Profile{profile("1", "work", false)}, "work", false},
		{"outside-no-profile", false, nil, "default", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNavigationFixture(tc.profiles...)
			r, err := f.service.Navigate(context.Background(), Request{InsideHerdr: tc.inside})
			if err != nil || r.Session != tc.session {
				t.Fatalf("result=%+v err=%v calls=%v", r, err, f.calls)
			}
			assertNoCalls(t, f.calls, "ssh", "check-repository", "prepare-repository")
			if tc.inside {
				assertNoCalls(t, f.calls, "attach:")
				if r.State != "sidebar-ready" || !strings.Contains(r.Summary, "native sidebar") {
					t.Fatalf("inside navigation claimed an attachment: %+v", r)
				}
			} else if r.State != "returned" || f.calls[len(f.calls)-1] != "attach:"+tc.session {
				t.Fatalf("outside navigation did not attach explicit session: %+v %v", r, f.calls)
			}
			if tc.ensure != (r.CatalogState == "applied") {
				t.Fatalf("catalog mutation mismatch: %+v", r)
			}
		})
	}
}

func TestRepositoryNavigationCheckEnsurePrepareOrdering(t *testing.T) {
	f := newNavigationFixture()
	r, err := f.service.Navigate(context.Background(), repoRequest(true))
	if err != nil || r.State != "sidebar-ready" || r.RepositoryState != "prepared" || r.Workspace != "herdr:w7" || r.CatalogState != "applied" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	var operations []string
	for _, call := range f.calls {
		if call == "check-repository" || call == "ensure-profile" || call == "prepare-repository" {
			operations = append(operations, call)
		}
	}
	if want := []string{"check-repository", "ensure-profile", "check-repository", "prepare-repository"}; !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations=%v want=%v", operations, want)
	}
	assertNoCalls(t, f.calls, "attach:", "ssh")
}

func TestMultipleProfilesRequireExactChoiceAndIgnoreSelected(t *testing.T) {
	for _, inside := range []bool{false, true} {
		f := newNavigationFixture(profile("1", "default", true), profile("2", "work", false))
		f.profiles[0].Selected = true
		basePick := f.service.PickProfile
		f.service.PickProfile = func(ctx context.Context, profiles []herdrremote.Profile) (herdrremote.Profile, error) {
			p, err := basePick(ctx, profiles)
			p.Selected = !p.Selected
			return p, err
		}
		r, err := f.service.Navigate(context.Background(), repoRequest(inside))
		if err != nil || r.Session != "work" || r.ProfileID != strings.Repeat("2", 32) || !f.profiles[0].Enabled || f.profiles[1].Enabled != inside {
			t.Fatalf("inside=%t result=%+v err=%v profiles=%+v", inside, r, err, f.profiles)
		}
	}

	f := newNavigationFixture(profile("1", "default", true), profile("2", "work", false))
	f.service.PickProfile = func(context.Context, []herdrremote.Profile) (herdrremote.Profile, error) {
		return profile("3", "unexpected", true), nil
	}
	if _, err := f.service.Navigate(context.Background(), repoRequest(true)); err == nil {
		t.Fatal("accepted a profile absent from the reviewed catalog")
	}
	assertNoCalls(t, f.calls, "ensure-profile", "check-repository", "prepare-repository", "attach:", "ssh")
}

func TestUnknownCatalogNeverAssumesDefaultSession(t *testing.T) {
	for _, inside := range []bool{false, true} {
		for _, status := range []string{"invalid", "unavailable", "ready"} {
			f := newNavigationFixture()
			f.service.Capabilities = func(context.Context) (Capabilities, error) {
				return Capabilities{Installed: true, RemoteAvailable: true, Catalog: herdrremote.Inventory{Status: status}}, errors.New("catalog read failed")
			}
			r, err := f.service.Navigate(context.Background(), repoRequest(inside))
			if err == nil || r.State != "failed" || r.Session != "" {
				t.Fatalf("inside=%t status=%s result=%+v err=%v", inside, status, r, err)
			}
			assertNoCalls(t, f.calls, "choose-ssh", "ssh", "ensure-profile", "check-repository", "prepare-repository", "attach:")
		}
	}
}

func TestNoRuntimeBypassesAllHerdrOperations(t *testing.T) {
	f := newNavigationFixture()
	rq := repoRequest(true)
	rq.NoRuntime = true
	r, err := f.service.Navigate(context.Background(), rq)
	if err != nil || r.Transport != "ssh" || r.RefreshHerdr() {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if want := []string{"resolve", "check-target", "ssh"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%v want=%v", f.calls, want)
	}
}

func TestUnavailableHerdrRequiresExplicitSSHChoice(t *testing.T) {
	for _, choose := range []bool{true, false} {
		f := newNavigationFixture()
		f.target.Host.RemoteOS = fleet.RemoteOSWindows
		f.service.ChooseSSH = func(context.Context, string) (bool, error) { return choose, nil }
		r, err := f.service.Navigate(context.Background(), Request{InsideHerdr: true})
		if choose {
			if err != nil || r.Transport != "ssh" {
				t.Fatalf("result=%+v err=%v", r, err)
			}
		} else if !errors.Is(err, ErrCanceled) || r.State != "canceled" {
			t.Fatalf("result=%+v err=%v", r, err)
		}
		assertNoCalls(t, f.calls, "ensure-profile", "check-repository", "prepare-repository", "attach:")
		if !choose {
			assertNoCalls(t, f.calls, "ssh")
		}
	}
}

func TestRepositoryCapabilityFailurePrecedesProfileChanges(t *testing.T) {
	f := newNavigationFixture()
	f.service.CheckRepository = func(context.Context, Target, string, string) (fleet.HerdrRepoResult, error) {
		return fleet.HerdrRepoResult{}, errors.New("unknown command _herdr-repo; update remote dev")
	}
	r, err := f.service.Navigate(context.Background(), repoRequest(true))
	if err == nil || r.State != "failed" || r.Stage != "check-repository" || r.CatalogState != "unchanged" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	assertNoCalls(t, f.calls, "ensure-profile", "prepare-repository", "attach:", "ssh")
}

func TestMissingServerOutsideRequiresHostNavigationFirst(t *testing.T) {
	f := newNavigationFixture()
	baseCheck := f.service.CheckRepository
	f.service.CheckRepository = func(ctx context.Context, target Target, session, identity string) (fleet.HerdrRepoResult, error) {
		r, err := baseCheck(ctx, target, session, identity)
		r.RuntimeState = "needs-server"
		return r, err
	}
	r, err := f.service.Navigate(context.Background(), repoRequest(false))
	if err == nil || !strings.Contains(err.Error(), "host Enter") || r.RepositoryState != "checked" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	assertNoCalls(t, f.calls, "ensure-profile", "prepare-repository", "attach:", "ssh")
}

func TestCancellationDoesNotFallThroughToSSH(t *testing.T) {
	for _, where := range []string{"pick", "ensure"} {
		f := newNavigationFixture()
		if where == "pick" {
			f.profiles = []herdrremote.Profile{profile("1", "default", true), profile("2", "work", false)}
			f.service.PickProfile = func(context.Context, []herdrremote.Profile) (herdrremote.Profile, error) {
				return herdrremote.Profile{}, ErrCanceled
			}
		} else {
			f.service.EnsureProfile = func(context.Context, Target, Selection) (EnsureResult, error) {
				return EnsureResult{Status: "unchanged"}, ErrCanceled
			}
		}
		r, err := f.service.Navigate(context.Background(), repoRequest(true))
		if !errors.Is(err, ErrCanceled) || r.State != "canceled" {
			t.Fatalf("where=%s result=%+v err=%v", where, r, err)
		}
		assertNoCalls(t, f.calls, "prepare-repository", "attach:", "ssh")
	}
}

func TestPartialResultsRetainCompletedEffects(t *testing.T) {
	for _, phase := range []string{"ensure-unknown", "identity-recheck", "prepare", "attach"} {
		t.Run(phase, func(t *testing.T) {
			f := newNavigationFixture()
			rq := repoRequest(true)
			switch phase {
			case "ensure-unknown":
				f.service.EnsureProfile = func(context.Context, Target, Selection) (EnsureResult, error) {
					return EnsureResult{Status: "unknown"}, errors.New("native verification interrupted")
				}
			case "identity-recheck":
				base := f.service.CheckRepository
				f.service.CheckRepository = func(ctx context.Context, target Target, session, identity string) (fleet.HerdrRepoResult, error) {
					if identity != "" {
						return fleet.HerdrRepoResult{}, errors.New("repository replaced")
					}
					return base(ctx, target, session, identity)
				}
			case "prepare":
				f.service.PrepareRepository = func(context.Context, Target, string, string) (fleet.HerdrRepoResult, error) {
					return fleet.HerdrRepoResult{}, errors.New("connection lost after create")
				}
			case "attach":
				rq.InsideHerdr = false
				f.service.Attach = func(context.Context, Target, string) error { return errors.New("client exited 1") }
			}
			r, err := f.service.Navigate(context.Background(), rq)
			if err == nil || r.State != "partial" || !strings.Contains(r.Summary, "partial") {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if phase == "attach" && r.RepositoryState != "prepared" || phase == "prepare" && r.RepositoryState != "unknown" || phase == "identity-recheck" && r.CatalogState != "applied" || phase == "ensure-unknown" && r.CatalogState != "unknown" {
				t.Fatalf("lost effect evidence: %+v", r)
			}
			assertNoCalls(t, f.calls, "ssh")
		})
	}
}

func TestFreshEndpointAndProfileChecksPreventAttachment(t *testing.T) {
	for _, authority := range []string{"endpoint", "profile"} {
		f := newNavigationFixture(profile("1", "work", true))
		basePrepare := f.service.PrepareRepository
		f.service.PrepareRepository = func(ctx context.Context, target Target, session, identity string) (fleet.HerdrRepoResult, error) {
			r, err := basePrepare(ctx, target, session, identity)
			if authority == "endpoint" {
				f.target.EndpointID = "changed"
			} else {
				f.profiles[0].Session = "changed"
			}
			return r, err
		}
		r, err := f.service.Navigate(context.Background(), repoRequest(false))
		if err == nil || r.State != "partial" || r.RepositoryState != "prepared" {
			t.Fatalf("authority=%s result=%+v err=%v", authority, r, err)
		}
		assertNoCalls(t, f.calls, "attach:", "ssh")
	}
}

func TestEnsureCannotSubstituteAnotherProfile(t *testing.T) {
	f := newNavigationFixture(profile("1", "work", false))
	f.service.EnsureProfile = func(context.Context, Target, Selection) (EnsureResult, error) {
		substitute := profile("2", "work", true)
		f.profiles = []herdrremote.Profile{substitute}
		return EnsureResult{Status: "applied", Profile: substitute}, nil
	}
	r, err := f.service.Navigate(context.Background(), repoRequest(true))
	if err == nil || r.State != "partial" || r.CatalogState != "applied" {
		t.Fatalf("substituted a different profile: result=%+v err=%v", r, err)
	}
	assertNoCalls(t, f.calls, "prepare-repository", "attach:", "ssh")
}
