//go:build linux || darwin

package sshhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedV2UpgradeOwnershipUpdateAndRemove(t *testing.T) {
	ctx := context.Background()
	_, paths := secureFixtureService(t, managedRoot("# keep root\n"))
	service, err := NewService(paths, runnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
		result, err := (managedFixtureRunner{paths: paths}).Run(ctx, request)
		if err != nil {
			return result, err
		}
		path, err := paths.ManagedPath(request.Args[1])
		if err != nil {
			return RunResult{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return RunResult{}, err
		}
		definition, err := ParseManaged(data)
		if err != nil {
			return RunResult{}, err
		}
		if definition.IdentityAgent != "" {
			result.Stdout = fmt.Appendf(result.Stdout, "identityagent %s\n", definition.IdentityAgent)
		}
		if definition.SecurityKeyProvider != "" {
			result.Stdout = fmt.Appendf(result.Stdout, "securitykeyprovider %s\n", definition.SecurityKeyProvider)
		}
		return result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	definition := ManagedDefinition{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22}
	applyDefinition(t, service, definition)
	path := filepath.Join(paths.ManagedDir, "lab.conf")
	v1, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(v1, []byte(ManagedHeader+"\n")) {
		t.Fatalf("initial v1 bytes = %q, err %v", v1, err)
	}

	definition.IdentityAgent = filepath.Join(paths.Home, "agent with spaces", "socket")
	definition.IdentityFile = filepath.Join(paths.SSHDir, "dev_agent_custom_lab.pub")
	definition.IdentitiesOnly = boolPointer(true)
	definition.SecurityKeyProvider = "internal"
	upgrade, err := service.PlanUpsert(ctx, definition)
	if err != nil || upgrade.Action != ActionUpdate || !upgrade.Ready() {
		t.Fatalf("v2 upgrade plan = %#v, err %v", upgrade, err)
	}
	result, err := service.ApplyManaged(ctx, upgrade)
	if err != nil || !result.Changed || !result.Verified {
		t.Fatalf("v2 upgrade result = %#v, err %v", result, err)
	}
	v2, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(v2, []byte(ManagedHeaderV2+"\n")) {
		t.Fatalf("upgraded v2 bytes = %q, err %v", v2, err)
	}
	managed, err := service.InspectManaged("lab")
	if err != nil || managed.Definition.IdentityAgent != definition.IdentityAgent || managed.Definition.SecurityKeyProvider != definition.SecurityKeyProvider {
		t.Fatalf("v2 inspection = %#v, err %v", managed, err)
	}
	inventory, err := service.Discover(ctx)
	if err != nil || !inventory.Complete {
		t.Fatalf("v2 discovery = %#v, err %v", inventory, err)
	}
	alias, ok := inventory.Find("lab")
	if !ok || len(alias.Definitions) != 1 || alias.Definitions[0].Ownership != OwnershipManaged {
		t.Fatalf("v2 ownership = %#v", alias)
	}
	noop, err := service.PlanUpsert(ctx, definition)
	if err != nil || noop.Action != ActionNoop {
		t.Fatalf("v2 noop = %#v, err %v", noop, err)
	}
	result, err = service.ApplyManaged(ctx, noop)
	if err != nil || result.Changed || !result.Verified {
		t.Fatalf("v2 noop apply = %#v, err %v", result, err)
	}
	definition.HostName = "updated.example"
	applyDefinition(t, service, definition)
	managed, err = service.InspectManaged("lab")
	if err != nil || managed.Definition.HostName != definition.HostName || managed.Definition.IdentityAgent != definition.IdentityAgent {
		t.Fatalf("v2 updated definition = %#v, err %v", managed, err)
	}
	remove, err := service.PlanRemove(ctx, "lab")
	if err != nil || remove.Action != ActionRemove {
		t.Fatalf("v2 removal plan = %#v, err %v", remove, err)
	}
	result, err = service.ApplyManaged(ctx, remove)
	if err != nil || !result.Changed {
		t.Fatalf("v2 removal = %#v, err %v", result, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("v2 managed file remains: %v", err)
	}
	root, err := os.ReadFile(paths.RootConfig)
	if err != nil || string(root) != managedRoot("# keep root\n") {
		t.Fatalf("managed lifecycle altered root: %q, err %v", root, err)
	}
}

func TestManagedV2EffectiveMismatchRollsBackUpgrade(t *testing.T) {
	for _, field := range []string{"IdentityAgent", "SecurityKeyProvider"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			service, paths := secureFixtureService(t, managedRoot(""))
			definition := ManagedDefinition{Alias: "lab", HostName: "host.example"}
			applyDefinition(t, service, definition)
			path := filepath.Join(paths.ManagedDir, "lab.conf")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			definition.IdentityAgent = filepath.Join(paths.Home, "agent.sock")
			definition.SecurityKeyProvider = "internal"
			plan, err := service.PlanUpsert(ctx, definition)
			if err != nil || !plan.Ready() {
				t.Fatalf("upgrade plan = %#v, err %v", plan, err)
			}
			agent, provider := definition.IdentityAgent, definition.SecurityKeyProvider
			if field == "IdentityAgent" {
				agent = filepath.Join(paths.Home, "wrong.sock")
			} else {
				provider = filepath.Join(paths.Home, "wrong-provider.so")
			}
			service.runner = &recordingRunner{result: RunResult{Stdout: fmt.Appendf(nil,
				"hostname host.example\nidentityagent %s\nsecuritykeyprovider %s\n", agent, provider)}}
			result, err := service.ApplyManaged(ctx, plan)
			if err == nil || !result.RolledBack || result.Changed || result.Verified {
				t.Fatalf("mismatched %s result = %#v, err %v", field, result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("mismatched %s did not restore v1: %v", field, err)
			}
		})
	}
}

func TestManagedV2HeaderBlocksForeignFormattingAndOrganization(t *testing.T) {
	ctx := context.Background()
	paths := fixturePaths(t)
	data, err := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "lab.example", SecurityKeyProvider: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.SSHDir, "config.d", "work", "lab.conf")
	writeFixture(t, path, string(data))
	writeFixture(t, paths.RootConfig, "Include config.d/work/lab.conf\n")
	service := newFixtureService(t, paths, DiscoverOptions{})
	if _, err := service.PlanFormat(ctx, []string{path}, "  "); err == nil {
		t.Fatal("format accepted a provider-managed v2 file in a user directory")
	}
	if _, err := service.Organization(ctx); err == nil {
		t.Fatal("organization accepted a provider-managed v2 fragment")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatalf("rejected edit changed provider bytes: %v", err)
	}
}
