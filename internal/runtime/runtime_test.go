package runtime_test

import (
	"context"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func TestSelect(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"none", "none"},
		{"tmux", "tmux"},
		{"herdr", "herdr"},
		{"nonsense", "none"}, // validated earlier; never panic here
	} {
		if got := runtime.Select(tc.in).Name(); got != tc.want {
			t.Errorf("Select(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// auto must always resolve to something usable.
	if got := runtime.Select("auto"); !got.Available() {
		t.Errorf("Select(auto) returned unavailable backend %q", got.Name())
	}
}

func TestNoneIsAlwaysAvailable(t *testing.T) {
	n := runtime.None{}
	if !n.Available() {
		t.Fatal("None must always be available — it is the floor dev degrades to")
	}
	h, err := n.Open(context.Background(), "/some/dir", "label")
	if err != nil || h != "/some/dir" {
		t.Errorf("None.Open = %q, %v; want the directory back as the handle", h, err)
	}
	if err := n.Close(context.Background(), h); err != nil {
		t.Errorf("None.Close: %v", err)
	}
	if err := n.Annotate(context.Background(), h, map[string]string{"stage": "HOT"}); err != nil {
		t.Errorf("None.Annotate should be a silent no-op: %v", err)
	}
}

func TestSessionName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"atp security recovery", "atp-security-recovery"},
		{"repo.name:1", "repo-name-1"},
		{"", "dev"},
		{"...", "dev"},
	} {
		if got := runtime.SessionName(tc.in); got != tc.want {
			t.Errorf("SessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionCovers(t *testing.T) {
	s := runtime.Session{Dirs: []string{"/wt/demo/feat-auth"}}
	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{"/wt/demo/feat-auth", true},
		{"/wt/demo/feat-auth/src", true}, // a pane deeper in the checkout
		{"/wt/demo", true},               // the repo containing the checkout
		{"/wt/demo/feat-auth-other", false},
		{"/elsewhere", false},
	} {
		if got := s.Covers(tc.dir); got != tc.want {
			t.Errorf("Covers(%q) = %v, want %v", tc.dir, got, tc.want)
		}
	}
}

// The contract every backend must satisfy. Backends that are not installed on
// this machine are skipped rather than failed, so the suite is meaningful in
// CI (where only None exists) and locally (where herdr and tmux do).
func TestBackendContract(t *testing.T) {
	for _, rt := range runtime.All() {
		t.Run(rt.Name(), func(t *testing.T) {
			if !rt.Available() {
				t.Skipf("%s not available on this machine", rt.Name())
			}
			ctx := context.Background()
			sessions, err := rt.List(ctx)
			if err != nil {
				t.Fatalf("List on an available backend must not error: %v", err)
			}
			for _, s := range sessions {
				if s.Handle == "" {
					t.Errorf("session with empty handle: %+v", s)
				}
			}
			// Closing an unknown handle must be tolerated, not fatal: dev's
			// stored handle is advisory and often stale.
			if err := rt.Close(ctx, ""); err != nil {
				t.Errorf("Close(\"\") should be a no-op, got %v", err)
			}
		})
	}
}
