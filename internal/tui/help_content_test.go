package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	helpdocs "github.com/daviddwlee84/dev-cli/internal/help"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestHelpContentEveryViewHasKeysGuideAndValidManualLinks(t *testing.T) {
	m := New(Actions{}, nil, nil)
	for _, view := range Views {
		t.Run(view.String(), func(t *testing.T) {
			keys, guides := m.helpKeyEntries(view), helpGuideEntries(view)
			if len(keys) < 10 || len(guides) < 5 || helpTLDR(view) == "" {
				t.Fatal("missing help layer")
			}
			seen := map[string]bool{}
			for _, entry := range append(keys, guides...) {
				if entry.ID == "" || seen[entry.ID] || entry.Title == "" || entry.Description == "" || entry.View != view {
					t.Fatalf("invalid or repeated entry: %+v", entry)
				}
				seen[entry.ID] = true
				if entry.Topic != "" {
					if topic, err := helpdocs.Get(entry.Topic); err != nil || topic.Name != entry.Topic {
						t.Fatalf("invalid manual link %q: %v", entry.Topic, err)
					}
				}
			}
			for _, topicName := range helpRelatedTopics(view) {
				if topic, err := helpdocs.Get(topicName); err != nil || topic.Name != topicName {
					t.Fatalf("invalid related topic %q: %v", topicName, err)
				}
			}
			for _, entry := range guides {
				if !entry.Guide {
					t.Fatal("guide entry was not marked as Guide")
				}
			}
		})
	}
}

func TestHelpContentToolsUseKnownStatusWithoutProbeOrCommandDisclosure(t *testing.T) {
	probe := func(context.Context) bool { t.Fatal("help probed an external tool"); return false }
	m := New(Actions{Tools: []Tool{
		{Key: "V", Name: "Editor", Command: []string{"private-program", "--secret=hidden-value"}, Probe: probe, Availability: ToolUnknown},
		{Key: "F", Name: "Files", Command: []string{"private-file-program"}, Probe: probe, Availability: ToolAvailable},
		{Key: "B", Name: "Monitor", Command: []string{"private-monitor"}, Probe: probe, Availability: ToolUnavailable},
		{Key: "A", Name: "Alternate", Command: []string{"private-alternate"}},
		{Key: "u", Name: "Shadowed update", Command: []string{"private-shadowed"}},
	}}, nil, nil)
	for _, view := range Views {
		tools := map[string]helpEntry{}
		for _, entry := range m.helpKeyEntries(view) {
			if entry.Group != "Custom tools" {
				continue
			}
			tools[entry.Key] = entry
			if text := entry.Title + entry.Description; strings.Contains(text, "private-") || strings.Contains(text, "hidden-value") {
				t.Fatalf("raw command disclosed: %s", text)
			}
		}
		if !strings.Contains(tools["V"].Description, "not yet known") || !strings.Contains(tools["F"].Description, "status: available") || !strings.Contains(tools["B"].Description, "status: unavailable") {
			t.Fatalf("known tool states lost for %s: %+v", view, tools)
		}
		if _, shadowed := tools["u"]; shadowed {
			t.Fatal("intercepted key advertised as a tool")
		}
		_, hasA := tools["A"]
		if wantA := view != ViewSkills && view != ViewMCP; hasA != wantA {
			t.Fatalf("A binding collision for %s", view)
		}
	}
}

func TestHostTreeHelpEntryIdentitiesAreUnique(t *testing.T) {
	m := treeModel(Actions{})
	seen := map[string]bool{}
	for _, entry := range append(m.helpKeyEntries(ViewFleet), helpGuideEntries(ViewFleet)...) {
		if seen[entry.ID] {
			t.Fatalf("duplicate host tree help entry %q", entry.ID)
		}
		seen[entry.ID] = true
	}
}

func TestHelpContentSnapshotKeepsCachedAndFailedObservationsExplicit(t *testing.T) {
	m := New(Actions{ReloadRepos: func(context.Context) ([]RepoRow, error) {
		t.Fatal("snapshot reloaded repositories")
		return nil, nil
	}}, nil, []RepoRow{{
		Repo:    repo.Repo{Name: "sample", Path: "/repos/sample", HasGit: true},
		Status:  gitx.Status{Branch: "main", Upstream: "origin/main"},
		Pending: "cached", GitKnown: true,
	}})
	m.view = ViewRepos
	m.loads[int(ViewRepos)].source = perftrace.SourceCache
	m.loads[int(ViewRepos)].freshness = perftrace.FreshnessStale
	m.loads[int(ViewRepos)].loading = true
	snapshot := m.helpSelectionSnapshot()
	for _, text := range []string{"Captured when Help opened", "Source: cache", "freshness: stale", "loading", "Selected: sample", "Cyan/blue", "Gray", "Gray alone does not prove clean Git", "GIT: ~clean", "not a fresh clean/closed proof"} {
		if !strings.Contains(snapshot, text) {
			t.Errorf("snapshot lacks %q:\n%s", text, snapshot)
		}
	}
	if snapshot != ansi.Strip(snapshot) {
		t.Fatal("snapshot contains ANSI styling")
	}
	// The captured text must remain an ordinary immutable value while model
	// observations change underneath an open Help window.
	m.repos[0].Pending = ""
	m.repos[0].Context.Checkouts = []inventory.RepoCheckout{{StatusErr: errors.New("permission denied")}}
	m.viewErrors[int(ViewRepos)] = errors.New("incomplete repository read")
	failed := m.helpSelectionSnapshot()
	if !strings.Contains(failed, "Observation error: incomplete repository read") || !strings.Contains(failed, "GIT: ?") {
		t.Fatalf("failed observation became a clean fact:\n%s", failed)
	}
	if !strings.Contains(snapshot, "GIT: ~clean") {
		t.Fatal("captured text changed with the model")
	}
}

func TestHelpContentColorMeaningPreservesPriorityAndSeparateFacts(t *testing.T) {
	m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "repo", Path: "/repo"}, Status: gitx.Status{Changed: 1}}})
	m.view = ViewRepos
	if meaning := m.helpSelectionColor(); !strings.Contains(meaning, "Orange") {
		t.Fatalf("quiet styling hid dirty observation: %s", meaning)
	}
	item := repoItem{CheckoutIndex: 1, Repo: RepoRow{Context: inventory.RepoContext{Checkouts: []inventory.RepoCheckout{
		{}, {Ownership: inventory.CheckoutExternal, Status: gitx.Status{Changed: 1}},
	}}}}
	if repoItemColor(item) != helpColorDirty {
		t.Fatal("external worktree styling hid dirty observation")
	}
	item.Repo.Context.Checkouts[1].Status.Changed = 0
	if repoItemColor(item) != helpColorExternal {
		t.Fatal("external worktree ownership lost")
	}
	m.view, m.skills = ViewSkills, []agentskill.Skill{{Name: "skill", UpdateStatus: agentskill.UpdateCurrent, Presence: agentskill.PresenceMissing}}
	if meaning := m.helpSelectionColor(); !strings.Contains(meaning, "Green") || !strings.Contains(meaning, "INSTALL/integrity is separate") {
		t.Fatalf("skill source freshness confused with local presence: %s", meaning)
	}
	enabled := true
	m.view, m.mcp = ViewMCP, []agentmcp.Declaration{{Name: "server", Enabled: &enabled}}
	if meaning := m.helpSelectionColor(); !strings.Contains(meaning, "Green") || !strings.Contains(meaning, "health are not checked") {
		t.Fatalf("MCP enabled confused with server health: %s", meaning)
	}
	if got := (New(Actions{}, nil, nil)).helpSelectionSnapshot(); !strings.Contains(got, "No selected row") || !strings.Contains(got, "no complete view snapshot") {
		t.Fatalf("missing observations presented as facts: %s", got)
	}
}
