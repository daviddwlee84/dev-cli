package catalog_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

func graduationNameCatalogEntry() catalog.Entry {
	now := fixedClock()
	return catalog.Entry{
		SchemaVersion: catalog.CurrentSchemaVersion, ID: firstID,
		Kind: catalog.KindTry, Name: "scratch", Created: now, Discovered: now, Updated: now,
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: "scratch", Started: now,
			OriginalPath: "/tries/scratch", GraduatedAt: now, GraduatedPath: "/projects/chosen-project",
		},
	}
}

func TestGraduatedNameValidationIsOptionalAndRequiresSuccessfulHistory(t *testing.T) {
	for _, tc := range []struct {
		name, remembered string
		missingTime      bool
		missingPath      bool
		wantError        bool
	}{
		{name: "legacy absent"},
		{name: "never graduated", missingTime: true, missingPath: true},
		{name: "safe remembered name", remembered: "chosen-project"},
		{name: "missing time", remembered: "chosen-project", missingTime: true, wantError: true},
		{name: "missing path", remembered: "chosen-project", missingPath: true, wantError: true},
		{name: "traversal", remembered: "../elsewhere", wantError: true},
		{name: "POSIX separator", remembered: "nested/project", wantError: true},
		{name: "Windows separator", remembered: `nested\project`, wantError: true},
		{name: "not normalized", remembered: " chosen-project ", wantError: true},
		{name: "NUL", remembered: "project\x00name", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := graduationNameCatalogEntry()
			entry.Experiment.GraduatedName = tc.remembered
			if tc.missingTime {
				entry.Experiment.GraduatedAt = time.Time{}
			}
			if tc.missingPath {
				entry.Experiment.GraduatedPath = ""
			}
			err := entry.Validate()
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "graduated name") {
					t.Fatalf("invalid remembered name/history accepted: %v", err)
				}
			} else if err != nil {
				t.Fatalf("compatible optional name/history rejected: %v", err)
			}
		})
	}
}

func TestGraduatedNameRoundTripKeepsLegacyFieldAbsentUntilExplicitUpdate(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	entry := graduationNameCatalogEntry()
	entry.ID = "" // Create assigns the stable ID.
	if err := store.Create(&entry); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, entry.ID+".toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte("graduated_name")) {
		t.Fatal("legacy empty field was not omitted from TOML")
	}
	legacy, err := store.Get(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("graduated_name")) {
		t.Fatal("legacy empty field was not omitted from JSON")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reading legacy metadata rewrote the record: %v", err)
	}
	clone := legacy.Clone()
	clone.Experiment.GraduatedName = "chosen-project"
	if legacy.Experiment.GraduatedName != "" {
		t.Fatal("modifying a cloned graduation name changed the selected record")
	}
	if _, err := store.Update(entry.ID, func(next *catalog.Entry) error {
		next.Experiment.GraduatedName = clone.Experiment.GraduatedName
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(entry.ID)
	if err != nil || updated.Experiment.GraduatedName != "chosen-project" || updated.SchemaVersion != entry.SchemaVersion {
		t.Fatalf("additive remembered name did not round-trip: %+v, %v", updated, err)
	}
	encoded, err = json.Marshal(updated)
	if err != nil || !bytes.Contains(encoded, []byte(`"graduated_name":"chosen-project"`)) {
		t.Fatalf("remembered name missing from JSON: %s, %v", encoded, err)
	}
}
