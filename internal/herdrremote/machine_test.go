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
	rows  []Profile
	calls []sshhost.RunRequest
	fail  bool
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
