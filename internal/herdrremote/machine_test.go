package herdrremote

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"reflect"
	"testing"
)

const id = "0123456789abcdef0123456789abcdef"

type fakeRunner struct {
	rows        []Profile
	calls       []sshhost.RunRequest
	fail        bool
	afterAction func()
}

func (f *fakeRunner) Run(_ context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
	f.calls = append(f.calls, r)
	if reflect.DeepEqual(r.Args, []string{"machine", "list", "--json"}) {
		rows := f.rows
		if rows == nil {
			rows = []Profile{}
		}
		b, _ := json.Marshal(rows)
		return sshhost.RunResult{Stdout: b}, nil
	}
	if f.fail {
		return sshhost.RunResult{ExitCode: 1}, nil
	}
	switch r.Args[1] {
	case "add":
		f.rows = append(f.rows, Profile{ID: id, Label: r.Args[4], Target: r.Args[2], Session: r.Args[6], Enabled: true})
	case "rename":
		f.rows[0].Label = r.Args[4]
	case "disable":
		f.rows[0].Enabled = false
	case "enable":
		f.rows[0].Enabled = true
	case "remove":
		f.rows = nil
	default:
		return sshhost.RunResult{}, errors.New("unexpected native action")
	}
	if f.afterAction != nil {
		f.afterAction()
	}
	return sshhost.RunResult{}, nil
}
func TestNativeAddUsesExplicitAliasSessionAndVerification(t *testing.T) {
	f := &fakeRunner{}
	s := Service{Runner: f}
	ctx := context.Background()
	inv, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Plan(inv, Request{Action: "register", Alias: "box", Label: "My machine", Session: "agents"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatal("planning executed an action")
	}
	p.Request.Alias = "another" // display fields cannot retarget the private plan
	result, err := s.Apply(ctx, p, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || len(f.rows) != 1 || f.rows[0].Target != "box" || f.rows[0].Session != "agents" {
		t.Fatalf("bad result: %+v %+v", result, f.rows)
	}
	for _, r := range f.calls {
		if len(r.Args) > 1 && r.Args[1] == "add" && !r.Interactive {
			t.Fatal("native terminal was not handed through")
		}
	}
}
func TestProfilesAreMatchedByTargetSessionAndMutatedByID(t *testing.T) {
	f := &fakeRunner{rows: []Profile{{ID: id, Label: "Custom", Target: "box", Session: "default", Enabled: false}}}
	s := Service{Runner: f}
	ctx := context.Background()
	inv, _ := s.List(ctx)
	p, err := s.Plan(inv, Request{Action: "register", Alias: "box", Label: "different"})
	if err != nil {
		t.Fatal(err)
	}
	p.Status = "planned" // cannot override no-op authority
	r, err := s.Apply(ctx, p, false)
	if err != nil || r.Status != "noop" || f.rows[0].Enabled {
		t.Fatal("registration changed an existing disabled profile", r, err)
	}
	p, err = s.Plan(inv, Request{Action: "rename", ProfileID: id, Label: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	f.rows[0].Target = "external-change"
	if _, err = s.Apply(ctx, p, false); err == nil {
		t.Fatal("stale profile was mutated")
	}
}
func TestNativeFailureStaysUnknownAndNeverInventsProfiles(t *testing.T) {
	f := &fakeRunner{fail: true}
	s := Service{Runner: f}
	ctx := context.Background()
	inv, _ := s.List(ctx)
	p, err := s.Plan(inv, Request{Action: "register", Alias: "box"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Apply(ctx, p, false)
	if err == nil || r.Status != "unknown" || len(f.rows) != 0 {
		t.Fatal("native failure claimed success", r, err)
	}
}
func TestInvalidInventoryDoesNotExposeCredentialTargets(t *testing.T) {
	f := &fakeRunner{rows: []Profile{{ID: id, Label: "Bad", Target: "ssh://user:secret@host", Session: "default"}}}
	inv, err := (Service{Runner: f}).List(context.Background())
	if err == nil || len(inv.Profiles) != 0 {
		t.Fatal("invalid credential target leaked")
	}
}

func TestNativeProfileMutationVerifiesUnchangedIdentityAfterApply(t *testing.T) {
	for _, action := range []string{"enable", "disable", "rename"} {
		for _, field := range []string{"target", "session", "id", "other-field"} {
			t.Run(action+"/"+field, func(t *testing.T) {
				before := Profile{ID: id, Label: "Reviewed", Target: "reviewed-host", Session: "agents", Enabled: action != "enable"}
				f := &fakeRunner{rows: []Profile{before}}
				s := Service{Runner: f}
				inv, err := s.List(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				request := Request{Action: action, ProfileID: id}
				if action == "rename" {
					request.Label = "New label"
				}
				plan, err := s.Plan(inv, request)
				if err != nil {
					t.Fatal(err)
				}
				var concurrent Profile
				f.afterAction = func() {
					switch field {
					case "target":
						f.rows[0].Target = "concurrent-host"
					case "session":
						f.rows[0].Session = "concurrent-session"
					case "id":
						f.rows[0].ID = "1123456789abcdef0123456789abcdef"
					case "other-field":
						if action == "rename" {
							f.rows[0].Enabled = !before.Enabled
						} else {
							f.rows[0].Label = "Concurrent label"
						}
					}
					concurrent = f.rows[0]
				}
				result, err := s.Apply(t.Context(), plan, false)
				if err == nil || result.Status != "unknown" {
					t.Fatalf("concurrent change verified: %+v / %v", result, err)
				}
				mutations := 0
				for _, call := range f.calls {
					if len(call.Args) > 1 && call.Args[1] != "list" {
						mutations++
					}
				}
				if mutations != 1 || !reflect.DeepEqual(f.rows[0], concurrent) {
					t.Fatal("verification attempted to roll back external state")
				}
			})
		}
	}
}

func TestNativeProfileMutationIgnoresOnlySelectedAfterApply(t *testing.T) {
	for _, action := range []string{"enable", "disable", "rename"} {
		t.Run(action, func(t *testing.T) {
			f := &fakeRunner{rows: []Profile{{ID: id, Label: "Reviewed", Target: "host", Session: "agents", Enabled: action != "enable"}}}
			s := Service{Runner: f}
			inv, err := s.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Action: action, ProfileID: id}
			if action == "rename" {
				request.Label = "New label"
			}
			plan, err := s.Plan(inv, request)
			if err != nil {
				t.Fatal(err)
			}
			f.afterAction = func() { f.rows[0].Selected = true }
			result, err := s.Apply(t.Context(), plan, false)
			if err != nil || result.Status != "applied" || !f.rows[0].Selected {
				t.Fatalf("client selection invalidated mutation: %+v / %v", result, err)
			}
		})
	}
}
