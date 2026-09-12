package machineregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

const (
	firstID  = "00000000-0000-4000-8000-000000000001"
	secondID = "00000000-0000-4000-8000-000000000002"
	thirdID  = "00000000-0000-4000-8000-000000000003"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "machines", "registry.db"))
}

func binding(id string) Binding {
	return Binding{Provider: "ssh", Scope: "/synthetic/.ssh/config", NativeID: id, Fingerprint: "reviewed-source-v1"}
}

func applyRequest(t *testing.T, store *Store, request Request) Result {
	t.Helper()
	plan, err := store.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReadAndPlanMissingRegistryDoNotCreateState(t *testing.T) {
	store := testStore(t)
	snapshot, err := store.Read(t.Context())
	if err != nil || !reflect.DeepEqual(snapshot, emptySnapshot()) {
		t.Fatalf("Read = %+v, %v", snapshot, err)
	}
	plan, err := store.Plan(t.Context(), Request{Action: "adopt", Label: "workstation", Bindings: []Binding{binding("work")}})
	if err != nil || !validID(plan.MachineID) || plan.After.Machines[0].ID != plan.MachineID {
		t.Fatalf("Plan = %+v, %v", plan, err)
	}
	if _, err := os.Lstat(filepath.Dir(store.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read/plan created state directory: %v", err)
	}
}

func TestAdoptRoundTripKeepsProviderIdentitiesSeparate(t *testing.T) {
	store := testStore(t)
	refs := []Binding{binding("developer"), binding("administrator"), {Provider: "tailscale", Scope: "tailnet-one", NativeID: "peer-abc"}, {Provider: "dev", Scope: "verified-remote", NativeID: secondID}}
	result := applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "workstation", Bindings: refs})
	if result.Status != "changed" || result.Snapshot.Revision != 1 || len(result.Snapshot.Machines) != 1 || len(result.Snapshot.Bindings) != 4 {
		t.Fatalf("result = %+v", result)
	}
	read, err := NewStore(store.Path).Read(t.Context())
	if err != nil || !reflect.DeepEqual(read, result.Snapshot) {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if read.Machines[0].ID == secondID || read.Machines[0].ID == "peer-abc" {
		t.Fatal("registry ID was replaced by a provider or remote pin")
	}
	for _, path := range []string{store.Path, filepath.Join(filepath.Dir(store.Path), ".registry.lock")} {
		if _, err := inspectPrivateFile(path, false); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected registry files: %+v %v", entries, err)
	}
}

func TestRegistryPathIsEncodedAsAFileURI(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "machine registry #1", "registry.db"))
	result := applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "retained"})
	read, err := store.Read(t.Context())
	if err != nil || !reflect.DeepEqual(result.Snapshot, read) {
		t.Fatalf("special-character database path = %+v, %v", read, err)
	}
}

func TestLinkUnlinkSuppressionAndScopes(t *testing.T) {
	store := testStore(t)
	ref := binding("work")
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first", Bindings: []Binding{ref}})
	otherScope := ref
	otherScope.Scope = "/other/.ssh/config"
	applyRequest(t, store, Request{Action: "adopt", MachineID: secondID, Label: "second", Bindings: []Binding{otherScope}})
	if _, err := store.Plan(t.Context(), Request{Action: "link", MachineID: secondID, Bindings: []Binding{ref}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-machine collision = %v", err)
	}
	unlinked := applyRequest(t, store, Request{Action: "unlink", MachineID: firstID, Bindings: []Binding{ref}})
	got, ok := unlinked.Snapshot.LookupBinding(ref.Provider, ref.Scope, ref.NativeID)
	if !ok || !got.Suppressed || got.MachineID != "" || len(unlinked.Snapshot.Machines) != 2 {
		t.Fatalf("unlink lost suppression or machine: %+v", unlinked)
	}
	noop := applyRequest(t, store, Request{Action: "unlink", MachineID: firstID, Bindings: []Binding{ref}})
	if noop.Status != "noop" || noop.Snapshot.Revision != unlinked.Snapshot.Revision {
		t.Fatalf("repeat unlink = %+v", noop)
	}
	ref.Fingerprint = "explicitly-reviewed-v2"
	relinked := applyRequest(t, store, Request{Action: "link", MachineID: secondID, Bindings: []Binding{ref}})
	got, ok = relinked.Snapshot.LookupBinding(ref.Provider, ref.Scope, ref.NativeID)
	if !ok || got.Suppressed || got.MachineID != secondID || got.Fingerprint != ref.Fingerprint {
		t.Fatalf("explicit relink = %+v", got)
	}
	noop = applyRequest(t, store, Request{Action: "link", MachineID: secondID, Bindings: []Binding{ref}})
	if noop.Status != "noop" || noop.Snapshot.Revision != relinked.Snapshot.Revision {
		t.Fatalf("repeat link = %+v", noop)
	}
}

func TestMergeIsAtomicRetainsIDsAndSurvivorLabel(t *testing.T) {
	store := testStore(t)
	for _, item := range []struct{ id, label string }{{firstID, "first"}, {secondID, "survivor"}, {thirdID, "last"}} {
		applyRequest(t, store, Request{Action: "adopt", MachineID: item.id, Label: item.label, Bindings: []Binding{binding(item.label)}})
	}
	merged := applyRequest(t, store, Request{Action: "merge", MachineID: firstID, Into: secondID})
	resolved, ok := merged.Snapshot.Find(firstID)
	if !ok || resolved.ID != secondID || resolved.Label != "survivor" || len(merged.Snapshot.Machines) != 3 {
		t.Fatalf("merge result = %+v", merged)
	}
	ref, _ := merged.Snapshot.LookupBinding("ssh", "/synthetic/.ssh/config", "first")
	if ref.MachineID != secondID {
		t.Fatalf("source binding not transferred: %+v", ref)
	}
	if _, err := store.Plan(t.Context(), Request{Action: "adopt", MachineID: firstID, Label: "reuse"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("merged ID was reused: %v", err)
	}
	again := applyRequest(t, store, Request{Action: "merge", MachineID: secondID, Into: thirdID})
	resolved, ok = again.Snapshot.Find(firstID)
	if !ok || resolved.ID != thirdID {
		t.Fatal("chained redirect did not resolve")
	}
	if _, err := store.Plan(t.Context(), Request{Action: "merge", MachineID: thirdID, Into: firstID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge into redirect = %v", err)
	}
}

func TestStalePlanDoesNotOverwriteUnrelatedChanges(t *testing.T) {
	store := testStore(t)
	first, err := store.Plan(t.Context(), Request{Action: "adopt", MachineID: firstID, Label: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Plan(t.Context(), Request{Action: "adopt", MachineID: secondID, Label: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	result, err := store.Apply(t.Context(), second)
	if !errors.Is(err, ErrStale) || result.Status != "stale" {
		t.Fatalf("stale apply = %+v, %v", result, err)
	}
	snapshot, err := store.Read(t.Context())
	if err != nil || len(snapshot.Machines) != 1 || snapshot.Machines[0].ID != firstID {
		t.Fatalf("stale apply changed data: %+v, %v", snapshot, err)
	}
}

func TestPublicPlanMutationCannotChangeAuthority(t *testing.T) {
	store := testStore(t)
	refs := []Binding{binding("work")}
	plan, err := store.Plan(t.Context(), Request{Action: "adopt", MachineID: firstID, Label: "reviewed", Bindings: refs})
	if err != nil {
		t.Fatal(err)
	}
	refs[0].NativeID = "changed-input"
	plan.MachineID = secondID
	plan.After.Machines[0].Label = "unreviewed"
	plan.After.Bindings[0].NativeID = "unreviewed"
	result, err := store.Apply(t.Context(), plan)
	if err != nil || result.MachineID != firstID || result.Snapshot.Machines[0].Label != "reviewed" || result.Snapshot.Bindings[0].NativeID != "work" {
		t.Fatalf("mutated public plan affected apply: %+v, %v", result, err)
	}
	serialized, _ := json.Marshal(plan)
	var reconstructed Plan
	if err := json.Unmarshal(serialized, &reconstructed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(t.Context(), reconstructed); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("serialized plan applied: %v", err)
	}
}

func TestFutureSchemaFailsWithoutRecreatingDatabase(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "retained"})
	db, err := openDatabase(store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Path)
	if _, err := store.Read(t.Context()); !errors.Is(err, ErrSchema) {
		t.Fatalf("future schema read = %v", err)
	}
	if _, err := store.Plan(t.Context(), Request{Action: "adopt", Label: "new"}); !errors.Is(err, ErrSchema) {
		t.Fatalf("future schema plan = %v", err)
	}
	after, _ := os.ReadFile(store.Path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unsupported database was changed")
	}
}

func TestConcurrentPlansOnlyOneCanApply(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	var plans []Plan
	for _, name := range []string{"one", "two"} {
		plan, err := store.Plan(t.Context(), Request{Action: "link", MachineID: firstID, Bindings: []Binding{binding(name)}})
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, plan := range plans {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := NewStore(store.Path).Apply(context.Background(), plan)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	success, stale := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestDatabaseReplacementInvalidatesPlanEvenWithIdenticalContents(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	plan, err := store.Plan(t.Context(), Request{Action: "link", MachineID: firstID, Bindings: []Binding{binding("work")}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(store.Path, store.Path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setPrivateMode(store.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(t.Context(), plan); !errors.Is(err, ErrStale) {
		t.Fatalf("replaced database plan = %v", err)
	}
}

func TestCanceledApplyDoesNotCreateRegistry(t *testing.T) {
	store := testStore(t)
	plan, err := store.Plan(t.Context(), Request{Action: "adopt", MachineID: firstID, Label: "first"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Apply(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply = %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(store.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled apply created directory: %v", err)
	}
}

func TestFailedWriteRollsBackEveryAssociation(t *testing.T) {
	store := testStore(t)
	initial := applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first", Bindings: []Binding{binding("first")}})
	plan, err := store.Plan(t.Context(), Request{Action: "link", MachineID: firstID, Bindings: []Binding{binding("second")}})
	if err != nil {
		t.Fatal(err)
	}
	// An externally introduced constraint forces failure after the transaction
	// has deleted/reinserted records, proving failure cannot leave a half merge.
	db, err := openDatabase(store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE UNIQUE INDEX test_single_provider ON bindings(provider)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := store.Apply(t.Context(), plan)
	if err == nil || result.Status != "failed" {
		t.Fatalf("forced write failure = %+v, %v", result, err)
	}
	after, err := store.Read(t.Context())
	if err != nil || !reflect.DeepEqual(after, initial.Snapshot) {
		t.Fatalf("failed transaction changed registry: %+v, %v", after, err)
	}
}
