package artifact_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
)

func TestInspectWorktreesAggregatesWithoutMutatingIntents(t *testing.T) {
	root := t.TempDir()
	store := artifact.NewStore(filepath.Join(root, "intents"))
	intent := &artifact.Intent{
		RunID: "run-1", Provider: "test", SessionID: "1234567890abcdef",
		RepoPath: root, GitCommonDir: filepath.Join(root, ".git"), WorktreePath: root,
		Branch: "feat/x", Head: "0123456789abcdef",
	}
	if err := store.Create(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	inspections, err := artifact.InspectWorktrees(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspections) != 1 {
		t.Fatalf("inspections = %+v", inspections)
	}
	var inspection artifact.WorktreeInspection
	for _, value := range inspections {
		inspection = value
	}
	if inspection.IntentCount != 1 || inspection.Ready || inspection.Status != artifact.Armed {
		t.Fatalf("armed inspection = %+v", inspection)
	}
	if got, _ := store.Get(intent.ID); got.Status != artifact.Armed {
		t.Errorf("inspection mutated intent to %s", got.Status)
	}

	if err := store.Update(t.Context(), intent.ID, func(current *artifact.Intent) error {
		current.Status = artifact.Discarded
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inspections, err = artifact.InspectWorktrees(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range inspections {
		if !got.Ready || got.Status != artifact.Discarded {
			t.Fatalf("discarded inspection = %+v", got)
		}
	}
}
