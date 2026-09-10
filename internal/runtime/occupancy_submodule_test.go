package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestStrictOccupancyIncludesVerifiedSubmoduleAncestorsAndChildren(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	child, parent, other := gittest.New(t), gittest.New(t), gittest.New(t)
	parent.Git("-c", "protocol.file.allow=always", "submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	inner := filepath.Join(parent.Root, "child")
	for _, paths := range [][2]string{{parent.Root, inner}, {inner, parent.Root}} {
		target, err := inspectCheckoutIdentity(t.Context(), paths[0])
		if err != nil {
			t.Fatal(err)
		}
		covered, err := activityCoversCheckout(t.Context(), target, paths[1], OccupancyStrict)
		if err != nil || !covered {
			t.Fatalf("coverage %v = %v, %v", paths, covered, err)
		}
		covered, err = activityCoversCheckout(t.Context(), target, other.Root, OccupancyStrict)
		if err != nil || covered {
			t.Fatalf("unrelated repo covered = %v %v", covered, err)
		}
	}
}
