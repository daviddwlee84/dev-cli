package sshhost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestGeneratedKeyPathRulesAndDirectChildCreation(t *testing.T) {
	ctx := context.Background()
	paths := fixturePaths(t)
	runner := &keygenRunner{t: t, line: testPublicLine(0x44, "custom")}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}

	existing := filepath.Join(paths.SSHDir, "existing")
	makeFixturePrivateDirectory(t, existing)
	plan, err := service.PlanKey(ctx, KeyRequest{Operation: KeyGenerate, DestinationIdentity: filepath.Join(existing, "id_lab"), Comment: "lab key", NoPassphrase: true})
	if err != nil || !plan.Ready() || plan.CreateParent != "" {
		t.Fatalf("existing subdirectory plan = %#v, err %v", plan, err)
	}
	if _, err := service.ApplyKey(ctx, plan); err != nil {
		t.Fatal(err)
	}
	args := runner.requests[len(runner.requests)-2].Args
	if index := slices.Index(args, "-C"); index < 0 || index+1 >= len(args) || args[index+1] != "lab key" {
		t.Fatalf("comment missing from argv: %#v", args)
	}

	work := filepath.Join(paths.SSHDir, "work")
	plan, err = service.PlanKey(ctx, KeyRequest{Operation: KeyGenerate, DestinationIdentity: filepath.Join(work, "id_work"), NoPassphrase: true})
	if err != nil || !plan.Ready() || plan.CreateParent != work {
		t.Fatalf("direct child plan = %#v, err %v", plan, err)
	}
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("planning created the key directory: %v", err)
	}
	if err := service.RevalidateKeySelection(ctx, plan); err != nil {
		t.Fatalf("missing planned directory failed revalidation: %v", err)
	}
	if _, err := service.ApplyKey(ctx, plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(work)
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("created key directory = %v, err %v", info, err)
	}
	assertFixturePrivateFile(t, filepath.Join(work, "id_work"))

	for _, test := range []struct {
		destination, code string
	}{
		{filepath.Join(paths.SSHDir, "a", "b", "id_nested"), "key_parent_missing"},
		{filepath.Join(paths.Home, "id_outside"), "key_path_outside_ssh"},
		{"$KEYDIR/id_expanded", "key_path_unsupported_expansion"},
		{filepath.Join(paths.SSHDir, "id_named.pub"), "key_name_pub_suffix"},
	} {
		plan, err := service.PlanKey(ctx, KeyRequest{Operation: KeyGenerate, DestinationIdentity: test.destination, NoPassphrase: true})
		if err != nil || plan.Action != ActionBlocked || !hasDiagnostic(plan.Diagnostics, test.code) || plan.Diagnostics[0].Message == "" {
			t.Fatalf("%s plan = %#v, err %v", test.destination, plan, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.SSHDir, "a")); !os.IsNotExist(err) {
		t.Fatalf("blocked nested plan created a directory: %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	late := filepath.Join(paths.SSHDir, "late")
	plan, err = service.PlanKey(ctx, KeyRequest{Operation: KeyGenerate, DestinationIdentity: filepath.Join(late, "id_late"), NoPassphrase: true})
	if err != nil || plan.CreateParent != late {
		t.Fatalf("late plan = %#v, err %v", plan, err)
	}
	if err := os.Mkdir(late, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := service.RevalidateKeySelection(ctx, plan); err == nil {
		t.Fatal("unsafe directory created after planning passed revalidation")
	}
	if _, err := service.ApplyKey(ctx, plan); err == nil {
		t.Fatal("unsafe directory created after planning was used for generation")
	}
}
