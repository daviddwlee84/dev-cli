package machineregistry

import (
	"errors"
	"reflect"
	"testing"
)

func testImport(alias, origin string) SSHImport {
	return SSHImport{LocalAlias: alias, OriginID: origin, ProfileID: "remote-profile", FleetHost: "gateway", RemoteAlias: "db", SourceFingerprint: "source-v1", RouteFingerprint: "route-v1", DefinitionFingerprint: "definition-v1"}
}

func TestSSHImportProvenanceIsIdempotentAndSourceBound(t *testing.T) {
	s := testStore(t)
	value := testImport("gateway-db", "origin-a")
	first := applyRequest(t, s, Request{Action: "record-imports", Imports: []SSHImport{value}})
	if len(first.Snapshot.Machines) != 0 || len(first.Snapshot.Imports) != 1 {
		t.Fatal(first)
	}
	second := applyRequest(t, s, Request{Action: "record-imports", Imports: []SSHImport{value}})
	if second.Status != "noop" || !reflect.DeepEqual(first.Snapshot, second.Snapshot) {
		t.Fatal(second)
	}
	value.SourceFingerprint = "source-v2"
	updated := applyRequest(t, s, Request{Action: "record-imports", Imports: []SSHImport{value}})
	if updated.Snapshot.Revision != first.Snapshot.Revision+1 {
		t.Fatal(updated)
	}
	value.OriginID = "different-machine"
	if _, err := s.Plan(t.Context(), Request{Action: "record-imports", Imports: []SSHImport{value}}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	applyRequest(t, s, Request{Action: "adopt", MachineID: firstID, Label: "unrelated"})
	after, err := s.Read(t.Context())
	if err != nil || len(after.Imports) != 1 || after.Imports[0].SourceFingerprint != "source-v2" {
		t.Fatal(after, err)
	}
}

func TestSSHImportMigratesV1OnlyOnApply(t *testing.T) {
	s := testStore(t)
	old := applyRequest(t, s, Request{Action: "adopt", MachineID: firstID, Label: "existing"}).Snapshot
	db, err := openDatabase(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE ssh_imports; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	read, err := s.Read(t.Context())
	if err != nil || !reflect.DeepEqual(old, read) {
		t.Fatal(read, err)
	}
	plan, err := s.Plan(t.Context(), Request{Action: "record-imports", Imports: []SSHImport{testImport("gateway-db", "origin")}})
	if err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(s.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal(version, err)
	}
	db.Close()
	result, err := s.Apply(t.Context(), plan)
	if err != nil || len(result.Snapshot.Machines) != 1 || len(result.Snapshot.Imports) != 1 {
		t.Fatal(result, err)
	}
	db, err = openDatabase(s.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
}

func TestSSHImportRejectsStaleRegistryPlan(t *testing.T) {
	s := testStore(t)
	plan, err := s.Plan(t.Context(), Request{Action: "record-imports", Imports: []SSHImport{testImport("db", "origin")}})
	if err != nil {
		t.Fatal(err)
	}
	applyRequest(t, s, Request{Action: "adopt", MachineID: firstID, Label: "other"})
	if _, err := s.Apply(t.Context(), plan); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
