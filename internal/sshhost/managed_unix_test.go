//go:build unix

package sshhost

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func secureFixtureService(t *testing.T, root string) (*Service, Paths) {
	t.Helper()
	paths := fixturePaths(t)
	if err := os.Chmod(paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if root != "" {
		writeFixture(t, paths.RootConfig, root)
		if err := os.Chmod(paths.RootConfig, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(paths, managedFixtureRunner{paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return service, paths
}

func managedRoot(content string) string {
	return rootIncludeLine() + "\n" + content
}

func applyDefinition(t *testing.T, service *Service, definition ManagedDefinition) ManagedResult {
	t.Helper()
	plan, err := service.PlanUpsert(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready() {
		t.Fatalf("plan blocked: %#v", plan.Diagnostics)
	}
	result, err := service.ApplyManaged(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManagedAddUpdateRemoveAndIdempotentMtime(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	initial := ManagedDefinition{Alias: "lab", HostName: "one.example", User: "alice", Port: 22}
	result := applyDefinition(t, service, initial)
	if !result.Changed || !result.Verified || result.Action != ActionCreate {
		t.Fatalf("create result = %#v", result)
	}
	managed, err := service.InspectManaged("lab")
	if err != nil {
		t.Fatal(err)
	}
	if managed.Definition.HostName != "one.example" || managed.Mode != 0o600 {
		t.Fatalf("managed file = %#v", managed)
	}
	before, err := os.Stat(filepath.Join(paths.ManagedDir, "lab.conf"))
	if err != nil {
		t.Fatal(err)
	}
	noop, err := service.PlanUpsert(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if noop.Action != ActionNoop {
		t.Fatalf("noop plan = %#v", noop)
	}
	noopResult, err := service.ApplyManaged(context.Background(), noop)
	if err != nil || noopResult.Changed || !noopResult.Verified {
		t.Fatalf("noop result = %#v, err %v", noopResult, err)
	}
	after, err := os.Stat(filepath.Join(paths.ManagedDir, "lab.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("mtime changed for identical bytes: %v -> %v", before.ModTime(), after.ModTime())
	}

	updated := initial
	updated.HostName = "two.example"
	updatePlan, err := service.PlanUpsert(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	if updatePlan.Action != ActionUpdate {
		t.Fatalf("update plan = %#v", updatePlan)
	}
	updateResult, err := service.ApplyManaged(context.Background(), updatePlan)
	if err != nil || !updateResult.Changed || !updateResult.Verified {
		t.Fatalf("update result = %#v, err %v", updateResult, err)
	}
	managed, err = service.InspectManaged("lab")
	if err != nil || managed.Definition.HostName != "two.example" {
		t.Fatalf("updated managed = %#v, err %v", managed, err)
	}

	removePlan, err := service.PlanRemove(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	if removePlan.Action != ActionRemove {
		t.Fatalf("remove plan = %#v", removePlan)
	}
	removeResult, err := service.ApplyManaged(context.Background(), removePlan)
	if err != nil || !removeResult.Changed {
		t.Fatalf("remove result = %#v, err %v", removeResult, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("managed file remains: %v", err)
	}
	secondRemove, err := service.PlanRemove(context.Background(), "lab")
	if err != nil || secondRemove.Action != ActionNoop {
		t.Fatalf("second remove = %#v, err %v", secondRemove, err)
	}
}

func TestManagedApplyCreatesMissingManagedDirectoryAfterInit(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(paths, managedFixtureRunner{paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	initPlan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyInit(context.Background(), initPlan); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.ManagedDir); err != nil {
		t.Fatal(err)
	}
	applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "host"})
	for path, mode := range map[string]fs.FileMode{
		paths.SSHDir: 0o700, paths.ManagedDir: 0o700, filepath.Join(paths.ManagedDir, "lab.conf"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %04o, want %04o", path, info.Mode().Perm(), mode)
		}
	}
}

func TestManagedPlansBlockForeignExactWildcardAndIncompleteClosure(t *testing.T) {
	for _, test := range []struct {
		name, root, code string
	}{
		{name: "exact casefold", root: "Host Lab\n", code: "foreign_exact_collision"},
		{name: "wildcard", root: "Host l*b\n", code: "foreign_wildcard_collision"},
		{name: "dynamic include", root: "Match exec \"never\"\n Include ${MISSING}/x\n", code: "dynamic_match_include"},
		{name: "resolved Match host", root: "Host logical\n HostName real.example\nMatch host real.example\n Include guarded.conf\n", code: "dynamic_match_include"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := secureFixtureService(t, managedRoot(test.root))
			plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Action != ActionBlocked || !hasDiagnostic(plan.Diagnostics, test.code) {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestManagedPlanRequiresDefinitelyActiveDedicatedInclude(t *testing.T) {
	for _, test := range []struct {
		name string
		root string
	}{
		{name: "missing root"},
		{name: "no include", root: "# root\n"},
		{name: "guarded include", root: "Host other\n  " + rootIncludeLine() + "\n"},
		{name: "broad include", root: "Include ~/.ssh/config.d/*\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := secureFixtureService(t, test.root)
			plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Action != ActionBlocked || !hasDiagnostic(plan.Diagnostics, "managed_include_inactive") {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestManagedApplyVerificationRollbackCreateAndUpdate(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		runner := &recordingRunner{result: RunResult{ExitCode: 255, Stderr: []byte("invalid configuration")}}
		service.runner = runner
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ApplyManaged(context.Background(), plan)
		if err == nil || !result.RolledBack || result.Changed || result.Verified {
			t.Fatalf("result = %#v, err %v", result, err)
		}
		if _, err := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("failed create remains after verification rollback: %v", err)
		}
		if len(runner.requests) != 1 || runner.requests[0].Name != "ssh" || len(runner.requests[0].Args) != 2 || runner.requests[0].Args[0] != "-G" || runner.requests[0].Args[1] != "lab" {
			t.Fatalf("verification request = %#v", runner.requests)
		}
	})

	t.Run("update", func(t *testing.T) {
		service, _ := secureFixtureService(t, managedRoot("# root\n"))
		original := ManagedDefinition{Alias: "lab", HostName: "one.example"}
		applyDefinition(t, service, original)
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "two.example"})
		if err != nil {
			t.Fatal(err)
		}
		service.runner = &recordingRunner{result: RunResult{Stdout: []byte("hostname wrong.example\n")}}
		result, err := service.ApplyManaged(context.Background(), plan)
		if err == nil || !result.RolledBack || result.Changed || result.Verified {
			t.Fatalf("result = %#v, err %v", result, err)
		}
		managed, inspectErr := service.InspectManaged("lab")
		if inspectErr != nil || managed.Definition.HostName != original.HostName {
			t.Fatalf("rollback state = %#v, err %v", managed, inspectErr)
		}
	})
}

func TestManagedVerificationDoesNotRollbackAChangedPublishedIdentity(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "one"})
	plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "two"})
	if err != nil {
		t.Fatal(err)
	}
	concurrent, _ := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "concurrent"})
	service.runner = runnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		if err := os.WriteFile(filepath.Join(paths.ManagedDir, "lab.conf"), concurrent, 0o600); err != nil {
			t.Fatal(err)
		}
		return RunResult{Stdout: []byte("hostname wrong\n")}, nil
	})
	result, err := service.ApplyManaged(context.Background(), plan)
	if err == nil || result.RolledBack || !result.Changed || result.Verified {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	got, readErr := os.ReadFile(filepath.Join(paths.ManagedDir, "lab.conf"))
	if readErr != nil || !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent publication = %q, err %v", got, readErr)
	}
}

func TestManagedNoopStillRequiresSemanticVerification(t *testing.T) {
	service, _ := secureFixtureService(t, managedRoot("# root\n"))
	definition := ManagedDefinition{Alias: "lab", HostName: "host"}
	applyDefinition(t, service, definition)
	plan, err := service.PlanUpsert(context.Background(), definition)
	if err != nil || plan.Action != ActionNoop {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	service.runner = &recordingRunner{err: errors.New("ssh unavailable")}
	result, err := service.ApplyManaged(context.Background(), plan)
	if err == nil || result.Changed || result.Verified || result.RolledBack {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func TestManagedNoopRevalidatesDedicatedInclude(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	definition := ManagedDefinition{Alias: "lab", HostName: "host"}
	applyDefinition(t, service, definition)
	plan, err := service.PlanUpsert(context.Background(), definition)
	if err != nil || plan.Action != ActionNoop {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	if err := os.WriteFile(paths.RootConfig, []byte("# Include removed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	service.runner = runner
	if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyManaged error = %v, want source changed", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("unsafe no-op invoked ssh: %#v", runner.requests)
	}
}

func TestManagedApplyRejectsModifiedPlanPath(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
	if err != nil {
		t.Fatal(err)
	}
	plan.Path = filepath.Join(paths.Home, "escape.conf")
	if _, err := service.ApplyManaged(context.Background(), plan); err == nil {
		t.Fatal("ApplyManaged accepted a modified plan path")
	}
	if _, err := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("modified plan wrote managed target: %v", err)
	}
}

func TestManagedApplyRevalidatesFinalAndParentIdentityAtCommit(t *testing.T) {
	t.Run("create final appeared", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
		if err != nil {
			t.Fatal(err)
		}
		other, _ := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "other"})
		service.beforeManagedCommit = func() { writeFixture(t, filepath.Join(paths.ManagedDir, "lab.conf"), string(other)) }
		if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("ApplyManaged error = %v, want source changed", err)
		}
		got, err := os.ReadFile(filepath.Join(paths.ManagedDir, "lab.conf"))
		if err != nil || !bytes.Equal(got, other) {
			t.Fatalf("concurrent final = %q, err %v", got, err)
		}
	})

	t.Run("update parent replaced", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "one"})
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
		if err != nil {
			t.Fatal(err)
		}
		other, _ := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "other"})
		moved := paths.ManagedDir + ".moved"
		service.beforeManagedCommit = func() {
			if err := os.Rename(paths.ManagedDir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(paths.ManagedDir, "lab.conf"), string(other))
		}
		if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("ApplyManaged error = %v, want source changed", err)
		}
		got, err := os.ReadFile(filepath.Join(paths.ManagedDir, "lab.conf"))
		if err != nil || !bytes.Equal(got, other) {
			t.Fatalf("replacement-parent final = %q, err %v", got, err)
		}
	})

	t.Run("remove final changed", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "one"})
		plan, err := service.PlanRemove(context.Background(), "lab")
		if err != nil {
			t.Fatal(err)
		}
		other, _ := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "other"})
		service.beforeManagedCommit = func() {
			if err := os.WriteFile(filepath.Join(paths.ManagedDir, "lab.conf"), other, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("ApplyManaged error = %v, want source changed", err)
		}
		got, err := os.ReadFile(filepath.Join(paths.ManagedDir, "lab.conf"))
		if err != nil || !bytes.Equal(got, other) {
			t.Fatalf("changed removal target = %q, err %v", got, err)
		}
	})
}

func TestManagedApplyRechecksInventoryAtCommitAndAfterPublication(t *testing.T) {
	t.Run("collision before commit", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
		if err != nil {
			t.Fatal(err)
		}
		service.beforeManagedCommit = func() {
			writeFixture(t, paths.RootConfig, managedRoot("Host lab\n"))
		}
		result, err := service.ApplyManaged(context.Background(), plan)
		if !errors.Is(err, ErrSourceChanged) || result.Changed || result.RolledBack {
			t.Fatalf("pre-commit collision result = %#v, err %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("pre-commit collision published managed file: %v", statErr)
		}
	})

	t.Run("collision after publication rolls back", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
		if err != nil {
			t.Fatal(err)
		}
		service.afterManagedCommit = func() {
			writeFixture(t, paths.RootConfig, managedRoot("Host lab\n"))
		}
		result, err := service.ApplyManaged(context.Background(), plan)
		if !errors.Is(err, ErrSourceChanged) || result.Changed || !result.RolledBack || result.Verified {
			t.Fatalf("post-publication collision result = %#v, err %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("post-publication collision survived rollback: %v", statErr)
		}
	})

	t.Run("incomplete closure after publication rolls back", func(t *testing.T) {
		service, paths := secureFixtureService(t, managedRoot("# root\n"))
		plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
		if err != nil {
			t.Fatal(err)
		}
		service.afterManagedCommit = func() {
			writeFixture(t, paths.RootConfig, managedRoot("Match exec never-run\n    Include missing.conf\n"))
		}
		result, err := service.ApplyManaged(context.Background(), plan)
		if !errors.Is(err, ErrSourceChanged) || result.Changed || !result.RolledBack {
			t.Fatalf("post-publication incomplete result = %#v, err %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("post-publication incomplete closure survived rollback: %v", statErr)
		}
	})
}

func TestManagedFinalStaticRevalidationRollsBackCollisionInjectedBySSHVerification(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted.example"})
	if err != nil {
		t.Fatal(err)
	}
	service.runner = runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Name != "ssh" || len(request.Args) != 2 || request.Args[0] != "-G" {
			t.Fatalf("unexpected verification request: %#v", request)
		}
		writeFixture(t, paths.RootConfig, managedRoot("Host lab\n    HostName foreign.example\n"))
		return RunResult{Stdout: []byte("hostname wanted.example\n")}, nil
	})
	result, err := service.ApplyManaged(context.Background(), plan)
	if !errors.Is(err, ErrSourceChanged) || !result.RolledBack || result.Changed || result.Verified {
		t.Fatalf("late verification collision result = %#v, err %v", result, err)
	}
	if _, statErr := os.Lstat(filepath.Join(paths.ManagedDir, "lab.conf")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("late-collision publication survived rollback: %v", statErr)
	}
}

func TestManagedNamespaceRejectsCasefoldDriftLinksAndSpecialFiles(t *testing.T) {
	canonical, err := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "host"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, paths Paths)
	}{
		{name: "casefold filename", setup: func(t *testing.T, paths Paths) {
			writeFixture(t, filepath.Join(paths.ManagedDir, "Lab.conf"), string(canonical))
		}},
		{name: "unknown content", setup: func(t *testing.T, paths Paths) {
			writeFixture(t, filepath.Join(paths.ManagedDir, "lab.conf"), "Host lab\n")
		}},
		{name: "symlink", setup: func(t *testing.T, paths Paths) {
			target := filepath.Join(paths.Home, "foreign")
			writeFixture(t, target, string(canonical))
			if err := os.Symlink(target, filepath.Join(paths.ManagedDir, "lab.conf")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, paths Paths) {
			target := filepath.Join(paths.ManagedDir, "lab.conf")
			writeFixture(t, target, string(canonical))
			if err := os.Link(target, filepath.Join(paths.Home, "other-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", setup: func(t *testing.T, paths Paths) {
			if err := unix.Mkfifo(filepath.Join(paths.ManagedDir, "lab.conf"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, paths := secureFixtureService(t, managedRoot("# root\n"))
			if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, paths)
			plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "new"})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Action != ActionBlocked {
				t.Fatalf("unsafe namespace plan = %#v", plan)
			}
		})
	}
}

func TestManagedApplyRejectsChangedSourceButAcceptsConvergedBytes(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "one"})
	wanted := ManagedDefinition{Alias: "lab", HostName: "two"}
	plan, err := service.PlanUpsert(context.Background(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.ManagedDir, "lab.conf"), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyManaged error = %v, want source changed", err)
	}

	fresh, err := service.PlanUpsert(context.Background(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	desired, _ := RenderManaged(wanted)
	if err := os.WriteFile(filepath.Join(paths.ManagedDir, "lab.conf"), desired, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyManaged(context.Background(), fresh)
	if err != nil || result.Action != ActionNoop || result.Changed {
		t.Fatalf("converged result = %#v, err %v", result, err)
	}
}

func TestManagedCreatePlanDoesNotOverwriteConcurrentFile(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "wanted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	other, _ := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "other"})
	writeFixture(t, filepath.Join(paths.ManagedDir, "lab.conf"), string(other))
	if _, err := service.ApplyManaged(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyManaged error = %v, want source changed", err)
	}
	content, err := os.ReadFile(filepath.Join(paths.ManagedDir, "lab.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(other) {
		t.Fatal("concurrent managed file was overwritten")
	}
}

func TestDiscoveryClassifiesConcreteManagedOwnership(t *testing.T) {
	service, paths := secureFixtureService(t, managedRoot("# root\n"))
	applyDefinition(t, service, ManagedDefinition{Alias: "lab", HostName: "host"})
	writeFixture(t, paths.RootConfig, rootIncludeLine()+"\n")
	if err := os.Chmod(paths.RootConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	alias, ok := inventory.Find("lab")
	if !ok || alias.Definitions[0].Ownership != OwnershipManaged {
		t.Fatalf("managed ownership = %#v", alias)
	}
}
