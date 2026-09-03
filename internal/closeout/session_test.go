package closeout_test

import (
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/closeout"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func TestBuildSessionClassifiesEveryAgentStatus(t *testing.T) {
	tests := []struct {
		status string
		want   closeout.RuntimeCloseStatus
	}{
		{status: "idle", want: closeout.RuntimeCloseEligible},
		{status: "done", want: closeout.RuntimeCloseEligible},
		{status: "working", want: closeout.RuntimeCloseBlocked},
		{status: "running", want: closeout.RuntimeCloseBlocked},
		{status: "busy", want: closeout.RuntimeCloseBlocked},
		{status: "blocked", want: closeout.RuntimeCloseBlocked},
		{status: "waiting", want: closeout.RuntimeCloseBlocked},
		{status: "", want: closeout.RuntimeCloseUnknown},
		{status: "unknown", want: closeout.RuntimeCloseUnknown},
		{status: "paused", want: closeout.RuntimeCloseUnknown},
	}
	for _, tc := range tests {
		name := tc.status
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			session := runtime.Session{
				Handle: "w1", AgentStatus: tc.status,
				Panes: []runtime.Pane{{ID: "w1:p1", CWD: "/repo/wt", Agent: "claude", AgentStatus: tc.status}},
			}
			got := closeout.BuildSession(session, nil, coveredTarget())
			if got.RuntimeCloseStatus != tc.want {
				t.Fatalf("BuildSession(%q) = %q, details=%v", tc.status, got.RuntimeCloseStatus, got.Details)
			}
			if got.RuntimeCloseStatus == closeout.RuntimeCloseEligible &&
				(got.RuntimeCloseScope != closeout.RuntimeCloseScope || !strings.Contains(got.RuntimeCloseMeaning, "not task or work completion")) {
				t.Fatalf("eligible report lacks runtime-only qualification: %+v", got)
			}
		})
	}
}

func TestBuildSessionCallerAndMixedPurposeBlock(t *testing.T) {
	session := runtime.Session{
		Handle: "w1", AgentStatus: "idle",
		Panes: []runtime.Pane{
			{ID: "w1:p1", CWD: "/repo/wt", Agent: "claude", AgentStatus: "idle"},
			{ID: "w1:p2", CWD: "/other"},
		},
	}
	for name, mapping := range map[string]closeout.TargetMapping{
		"caller": {
			CheckoutPath: "/repo/wt", CoverageKnown: true, CoversTarget: true,
			CoveringPaneIDs: []string{"w1:p1"}, CallerContained: true,
		},
		"mixed flag": {
			CheckoutPath: "/repo/wt", CoverageKnown: true, CoversTarget: true,
			CoveringPaneIDs: []string{"w1:p1"}, MixedPurpose: true,
		},
		"mixed pane evidence": {
			CheckoutPath: "/repo/wt", CoverageKnown: true, CoversTarget: true,
			CoveringPaneIDs: []string{"w1:p1"}, MixedPaneIDs: []string{"w1:p2"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := closeout.BuildSession(session, nil, mapping)
			if got.RuntimeCloseStatus != closeout.RuntimeCloseBlocked {
				t.Fatalf("BuildSession = %q, details=%v", got.RuntimeCloseStatus, got.Details)
			}
		})
	}
}

func TestBuildSessionUnknownEvidenceFailsClosed(t *testing.T) {
	idle := runtime.Session{Handle: "w1", AgentStatus: "idle"}
	if got := closeout.BuildSession(idle, nil, closeout.TargetMapping{}); got.RuntimeCloseStatus != closeout.RuntimeCloseUnknown {
		t.Fatalf("missing coverage = %q, details=%v", got.RuntimeCloseStatus, got.Details)
	}
	if got := closeout.BuildSession(idle, nil, closeout.TargetMapping{CoverageKnown: true}); got.RuntimeCloseStatus != closeout.RuntimeCloseUnknown {
		t.Fatalf("unmatched session = %q, details=%v", got.RuntimeCloseStatus, got.Details)
	}
	if got := closeout.BuildSession(runtime.Session{Handle: "w1"}, nil, coveredTarget()); got.RuntimeCloseStatus != closeout.RuntimeCloseUnknown {
		t.Fatalf("missing activity = %q, details=%v", got.RuntimeCloseStatus, got.Details)
	}
}

func TestBuildSessionUsesOptionalActivitiesAndPreservesEvidence(t *testing.T) {
	session := runtime.Session{
		Handle: "w1", Label: "feature", Dirs: []string{"/repo/wt"}, Focused: true,
		AgentSessions: []string{"claude:session"},
		Panes: []runtime.Pane{{
			ID: "w1:p1", CWD: "/repo/wt/src", ShellCWD: "/repo/wt",
			Agent: "claude", AgentSession: "claude:session",
		}},
	}
	activities := []runtime.AgentActivity{
		{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", Name: "worker", Status: "DONE", CWD: "/repo/wt/src"},
		{PaneID: "w2:p1", WorkspaceID: "w2", Agent: "claude", Status: "working", CWD: "/other"},
	}
	mapping := coveredTarget()
	got := closeout.BuildSession(session, activities, mapping)
	if got.RuntimeCloseStatus != closeout.RuntimeCloseEligible {
		t.Fatalf("BuildSession = %q, details=%v", got.RuntimeCloseStatus, got.Details)
	}
	if !got.ActivitiesAvailable || len(got.Activities) != 1 || got.Activities[0].PaneID != "w1:p1" {
		t.Fatalf("activities = %+v, available=%t", got.Activities, got.ActivitiesAvailable)
	}
	if got.Session.WorkspaceID != "w1" || got.Session.Panes[0].CWD != "/repo/wt/src" ||
		got.Session.Panes[0].ShellCWD != "/repo/wt" || got.Activities[0].CWD != "/repo/wt/src" {
		t.Fatalf("runtime evidence not preserved: %+v", got)
	}

	session.Dirs[0] = "/changed"
	session.Panes[0].CWD = "/changed"
	activities[0].CWD = "/changed"
	mapping.CoveringPaneIDs[0] = "changed"
	if got.Session.Directories[0] != "/repo/wt" || got.Session.Panes[0].CWD != "/repo/wt/src" ||
		got.Activities[0].CWD != "/repo/wt/src" || got.Target.CoveringPaneIDs[0] != "w1:p1" {
		t.Fatalf("report shares mutable input: %+v", got)
	}
}

func TestBuildSessionUsesMappedPaneForActivityWithoutWorkspaceID(t *testing.T) {
	got := closeout.BuildSession(
		runtime.Session{Handle: "w1", Dirs: []string{"/repo/wt"}},
		[]runtime.AgentActivity{{PaneID: "w1:p1", Agent: "claude", Status: "done", CWD: "/repo/wt"}},
		coveredTarget(),
	)
	if got.RuntimeCloseStatus != closeout.RuntimeCloseEligible || len(got.Activities) != 1 {
		t.Fatalf("BuildSession = %q, activities=%+v details=%v", got.RuntimeCloseStatus, got.Activities, got.Details)
	}
}

func coveredTarget() closeout.TargetMapping {
	return closeout.TargetMapping{
		CheckoutPath: "/repo/wt", CoverageKnown: true, CoversTarget: true,
		CoveringPaneIDs: []string{"w1:p1"},
	}
}
