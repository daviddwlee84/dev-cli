package gitx_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestReadOnlyObservationContextDoesNotRefreshIndex(t *testing.T) {
	repository := gittest.New(t)
	index := filepath.Join(repository.Root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(repository.Root, "README.md")
	changed := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(readme, changed, changed); err != nil {
		t.Fatal(err)
	}
	ctx := gitx.WithReadOnlyObservations(t.Context())
	status, err := gitx.StatusOf(ctx, repository.Root)
	if err != nil || status.Dirty() {
		t.Fatalf("status = %+v, %v", status, err)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only observation rewrote index: %v", err)
	}
}
