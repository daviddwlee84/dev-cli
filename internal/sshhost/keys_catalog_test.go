package sshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type keyCatalogRunnerFunc func(context.Context, RunRequest) (RunResult, error)

func (f keyCatalogRunnerFunc) Run(ctx context.Context, r RunRequest) (RunResult, error) {
	return f(ctx, r)
}

func TestLocalKeyCatalogNoAgentIsStaticDeterministicAndRedacted(t *testing.T) {
	paths := fixturePaths(t)
	first, second := testPublicLine(0x84, "first"), testPublicLine(0x85, "second")
	writeFixture(t, filepath.Join(paths.SSHDir, "b.pub"), string(first)+"\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "a.pub"), string(second)+"\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "duplicate.pub"), string(first)+"\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "broken.pub"), "SECRET INVALID PUBLIC RECORD")
	writeFixture(t, paths.RootConfig, "Match exec \"MUST_NOT_RUN\"\n IdentityFile ~/.ssh/a\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || len(catalog.Candidates) != 2 || catalog.Complete || !hasDiagnostic(catalog.Diagnostics, "public_key_unreadable") {
		t.Fatalf("catalog=%+v error=%v", catalog, err)
	}
	if catalog.Candidates[0].Fingerprint > catalog.Candidates[1].Fingerprint {
		t.Fatal("candidate order is not deterministic")
	}
	for _, c := range catalog.Candidates {
		if c.Provenance.Effective || c.Provenance.Agent || c.NeedsPermissionRepair {
			t.Fatalf("invented source state: %+v", c)
		}
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET INVALID", "MUST_NOT_RUN", strings.Fields(string(first))[1], strings.Fields(string(second))[1]} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("catalog leaked material: %s", encoded)
		}
	}
	again, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, _ := json.Marshal(again)
	if !bytes.Equal(encoded, encodedAgain) {
		t.Fatal("repeated static catalog changed output")
	}
}

func TestLocalKeyCatalogRejectsConflictingInputsAndAllowsMissingSSHDir(t *testing.T) {
	paths := fixturePaths(t)
	s := newFixtureService(t, paths, DiscoverOptions{})
	for _, request := range []KeyCatalogRequest{{LocalOnly: true, Alias: "box"}, {LocalOnly: true, Effective: &EffectiveConfig{Alias: "box"}}} {
		if _, err := s.Catalog(t.Context(), request); err == nil {
			t.Fatal("conflicting catalog source accepted")
		}
	}
	if err := os.Remove(paths.SSHDir); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || !catalog.Complete || len(catalog.Candidates) != 0 {
		t.Fatalf("missing directory catalog=%+v error=%v", catalog, err)
	}
}

func TestCatalogPreservesPrivateCompanionNeedingRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-mode fixture")
	}
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "protected")
	writeFixture(t, identity, "PRIVATE CONTENT MUST NOT BE READ")
	writeFixture(t, identity+".pub", string(testPublicLine(0x86, "repair"))+"\n")
	if err := os.Chmod(identity, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatal(catalog, err)
	}
	selected := catalog.Candidates[0]
	if !selected.NeedsPermissionRepair || selected.IdentityFile != identity || selected.Provenance.Private || !hasDiagnostic(catalog.Diagnostics, "private_key_permissions") {
		t.Fatalf("repair source was lost: %+v", catalog)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: selected})
	if err != nil || plan.Ready() || plan.Action != ActionBlocked {
		t.Fatalf("unrepaired candidate plan=%+v error=%v", plan, err)
	}
	if err := os.Chmod(identity, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := s.PlanKey(t.Context(), KeyRequest{Candidate: selected})
	if err != nil || old.Ready() {
		t.Fatal("repair silently refreshed an old candidate")
	}
	fresh, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || len(fresh.Candidates) != 1 {
		t.Fatal(fresh, err)
	}
	if fresh.Candidates[0].Fingerprint != selected.Fingerprint || fresh.Candidates[0].NeedsPermissionRepair || !fresh.Candidates[0].Provenance.Private {
		t.Fatalf("fresh candidate=%+v", fresh.Candidates[0])
	}
	if plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: fresh.Candidates[0]}); err != nil || !plan.Ready() {
		t.Fatal(plan, err)
	}
}

func TestCatalogEffectivePublicIdentityRemainsPublicOnly(t *testing.T) {
	paths := fixturePaths(t)
	publicPath := filepath.Join(paths.SSHDir, "only.pub")
	writeFixture(t, publicPath, string(testPublicLine(0x97, "public-only"))+"\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{
		Effective: &EffectiveConfig{Alias: "target", IdentityFiles: []string{publicPath}}, NoAgent: true,
	})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatal(catalog, err)
	}
	candidate := catalog.Candidates[0]
	if candidate.IdentityFile != publicPath || candidate.Provenance.Private || candidate.NeedsPermissionRepair || !candidate.Provenance.Effective {
		t.Fatalf("invented private companion for effective public source: %+v", candidate)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: candidate})
	if err != nil || !plan.Ready() {
		t.Fatal(plan, err)
	}
	if _, err := s.ApplyKey(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogDuplicatePublicKeyPrefersUsablePrivateCompanion(t *testing.T) {
	paths := fixturePaths(t)
	line := testPublicLine(0x87, "orphan comment")
	writeFixture(t, filepath.Join(paths.SSHDir, "a.pub"), string(line)+"\n")
	identity := filepath.Join(paths.SSHDir, "z")
	writeFixture(t, identity, "opaque private identity")
	writeFixture(t, identity+".pub", string(testPublicLine(0x87, "private comment"))+"\n")
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Name != "ssh-keygen" || !hasArgPair(r.Args, "-f", identity) {
			t.Fatalf("wrong signer request=%+v", r)
		}
		return RunResult{Stdout: line}, nil
	})
	s, _ := NewService(paths, runner)
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatal(catalog, err)
	}
	c := catalog.Candidates[0]
	if c.IdentityFile != identity || c.PublicPath != identity+".pub" || !c.Provenance.Private || c.Comment != "private comment" {
		t.Fatalf("selected mismatched source paths: %+v", c)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: c})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyKey(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedPublicSourceRevalidatedBeforeEffects(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "different-key", true: "same-key-new-file"}[replacement], func(t *testing.T) {
			paths := fixturePaths(t)
			path := filepath.Join(paths.SSHDir, "key.pub")
			line := string(testPublicLine(0x88, "selected")) + "\n"
			writeFixture(t, path, line)
			s := newFixtureService(t, paths, DiscoverOptions{})
			catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]})
			if err != nil {
				t.Fatal(err)
			}
			if replacement {
				other := path + ".new"
				writeFixture(t, other, line)
				if err := os.Rename(other, path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFixture(t, path, string(testPublicLine(0x89, "replacement"))+"\n")
			}
			if err := s.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("source revalidation error=%v", err)
			}
			if _, err := s.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("stale source applied: %v", err)
			}
			if _, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]}); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("stale picker handle replanned: %v", err)
			}
		})
	}
}

func TestAgentSelectionPinsInheritedContextAndBootstrapsWithoutAPath(t *testing.T) {
	paths := fixturePaths(t)
	socket := filepath.Join(paths.Home, "selected-agent.sock")
	t.Setenv("SSH_AUTH_SOCK", socket)
	line := testPublicLine(0x90, "agent only")
	var calls []RunRequest
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		calls = append(calls, r)
		if r.Name == "ssh-add" {
			if !reflect.DeepEqual(r.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + socket}) {
				t.Fatalf("agent context substituted: %v", r.Env)
			}
			return RunResult{Stdout: append(append([]byte{}, line...), '\n')}, nil
		}
		if r.Name != "ssh" {
			t.Fatalf("agent-only path invoked %s", r.Name)
		}
		if r.Display == "ssh selected-key-only authentication proof" {
			data, err := os.ReadFile(sshArgForCatalogTest(r.Args, "-F"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "IdentityAgent "+socket) || !reflect.DeepEqual(r.Env, []string{"LC_ALL=C"}) {
				t.Fatalf("proof changed agent: %s %v", data, r.Env)
			}
			if err := os.WriteFile(sshArgForCatalogTest(r.Args, "-E"), []byte("Authenticated to target using \"publickey\".\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return RunResult{}, nil
	})
	s, _ := NewService(paths, runner)
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatal(catalog, err)
	}
	candidate := catalog.Candidates[0]
	if candidate.IdentityFile != "" || candidate.PublicPath != "" || !candidate.Provenance.Agent {
		t.Fatal(candidate)
	}
	before := len(calls)
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: candidate})
	if err != nil || len(calls) != before {
		t.Fatalf("planning executed a process: %v", err)
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(paths.Home, "replacement-agent.sock"))
	if err := s.RevalidateKeySelection(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	key, err := s.ApplyKey(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if key.Candidate.IdentityFile != "" || key.Candidate.state.agent == nil {
		t.Fatal("agent handle lost or invented identity path")
	}
	route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, Key: key})
	if err != nil || !result.Ready {
		t.Fatal(result, err)
	}
	files, err := os.ReadDir(paths.SSHDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("agent-only bootstrap retained selector files: %v %v", files, err)
	}
	data, _ := json.Marshal(key)
	if strings.Contains(string(data), socket) || strings.Contains(string(data), strings.Fields(string(line))[1]) {
		t.Fatal("agent context or key material leaked into JSON")
	}
}

func TestAgentProofPinsTargetWithoutReplacingProxyJumpAgent(t *testing.T) {
	paths := fixturePaths(t)
	targetSocket := filepath.Join(paths.Home, "target-agent.sock")
	jumpSocket := filepath.Join(paths.Home, "jump-agent.sock")
	t.Setenv("SSH_AUTH_SOCK", targetSocket)
	line := testPublicLine(0x95, "target agent")
	proofs := 0
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Name == "ssh-add" {
			if !reflect.DeepEqual(r.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + targetSocket}) {
				t.Fatal("target source revalidation changed agent")
			}
			return RunResult{Stdout: line}, nil
		}
		if r.Display == "ssh selected-key-only authentication proof" {
			proofs++
			if !reflect.DeepEqual(r.Env, []string{"LC_ALL=C"}) || os.Getenv("SSH_AUTH_SOCK") != jumpSocket {
				t.Fatalf("target proof replaced ambient jump agent: %v", r.Env)
			}
			data, err := os.ReadFile(sshArgForCatalogTest(r.Args, "-F"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "IdentityAgent "+targetSocket) || !strings.Contains(string(data), "IdentityAgent SSH_AUTH_SOCK") || !strings.Contains(string(data), "ProxyJump jump") {
				t.Fatalf("target/proxy agent separation missing:\n%s", data)
			}
			if err := os.WriteFile(sshArgForCatalogTest(r.Args, "-E"), []byte("Authenticated to target using \"publickey\".\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return RunResult{}, nil
	})
	s, _ := NewService(paths, runner)
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.ApplyKey(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", jumpSocket)
	route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "jump", RemoteOS: RemoteOSPOSIX}, {Alias: "target", RemoteOS: RemoteOSPOSIX}})
	material, err := s.validateKeyCandidate(key.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	selector, cleanup, err := s.prepareKeySelector(material)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	verified, err := s.runSSHProof(t.Context(), route.state.hops[1], selector, true)
	if err != nil || !verified || proofs != 1 {
		t.Fatalf("verified=%v proofs=%d err=%v", verified, proofs, err)
	}
}

func sshArgForCatalogTest(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func TestAgentSelectionDisappearanceStopsBeforeEffects(t *testing.T) {
	paths := fixturePaths(t)
	line := testPublicLine(0x91, "original")
	available := true
	sshCalls := 0
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Name != "ssh-add" {
			sshCalls++
			t.Fatalf("effect before selected source validation: %+v", r)
		}
		if !available {
			return RunResult{Stdout: testPublicLine(0x92, "other key")}, nil
		}
		return RunResult{Stdout: line}, nil
	})
	s, _ := NewService(paths, runner)
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]})
	if err != nil {
		t.Fatal(err)
	}
	available = false
	if err := s.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("missing key revalidation=%v", err)
	}
	if _, err := s.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("missing key applied=%v", err)
	}
	if sshCalls != 0 {
		t.Fatal("missing selected agent key started SSH")
	}
}

func TestNativeDefaultAgentContextDoesNotRewriteProxyEnvironment(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	paths := fixturePaths(t)
	line := testPublicLine(0x98, "native default agent")
	nativeCalls := 0
	s, _ := NewService(paths, keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		nativeCalls++
		if request.Name == "ssh-add" {
			if !reflect.DeepEqual(request.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK="}) {
				t.Fatalf("default agent catalog was not captured: %+v", request)
			}
			return RunResult{Stdout: line}, nil
		}
		if request.Name != "ssh" || !reflect.DeepEqual(request.Env, []string{"LC_ALL=C"}) {
			t.Fatalf("default agent proof changed the process context: %+v", request)
		}
		config, err := os.ReadFile(sshArgForCatalogTest(request.Args, "-F"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(config, []byte("IdentityAgent")) {
			t.Fatalf("default agent was replaced with an explicit target policy: %s", config)
		}
		if err := os.WriteFile(sshArgForCatalogTest(request.Args, "-E"), []byte("Authenticated to target using \"publickey\".\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return RunResult{}, nil
	}))
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.ApplyKey(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	material, err := s.validateKeyCandidate(key.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	selector, cleanup, err := s.prepareKeySelector(material)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	verified, err := s.runSSHProof(t.Context(), route.state.hops[0], selector, true)
	if err != nil || !verified {
		t.Fatalf("native default proof verified=%v err=%v", verified, err)
	}
	priorCalls := nativeCalls
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(paths.Home, "new-ambient.sock"))
	if err := s.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed native default context accepted before configuration: %v", err)
	}
	if nativeCalls != priorCalls {
		t.Fatal("changed native default context ran a command")
	}
}

func TestAgentSelectionRejectsExplicitAliasPolicyBeforeInstaller(t *testing.T) {
	for _, policy := range []string{"none", "/different-agent.sock"} {
		t.Run(policy, func(t *testing.T) {
			paths := fixturePaths(t)
			socket := filepath.Join(paths.Home, "custom-agent.sock")
			line := testPublicLine(0x93, "custom")
			installs := 0
			runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if r.Name == "ssh-add" {
					if !reflect.DeepEqual(r.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + socket}) {
						t.Fatal(r.Env)
					}
					return RunResult{Stdout: line}, nil
				}
				if r.Display == "ssh public-key installer" {
					installs++
				}
				return RunResult{}, nil
			})
			s, _ := NewService(paths, runner)
			catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{Effective: &EffectiveConfig{Alias: "target", Values: map[string][]string{"identityagent": {socket}}}})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanKey(t.Context(), KeyRequest{Candidate: catalog.Candidates[0]})
			if err != nil {
				t.Fatal(err)
			}
			key, err := s.ApplyKey(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
			route.state.hops[0].effective.Values["identityagent"] = []string{policy}
			result, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, Key: key})
			if err != nil || result.Ready || installs != 0 || result.Hops[0].Code != "selected_agent_policy_incompatible" {
				t.Fatalf("policy=%s result=%+v error=%v installs=%d", policy, result, err, installs)
			}
		})
	}
}

func TestKeySelectionPreflightChecksMetadataWithoutDecryptingOrWriting(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "private")
	writeFixture(t, identity, "opaque identity")
	writeFixture(t, identity+".pub", string(testPublicLine(0x94, "pair"))+"\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	plan, err := s.PlanKey(t.Context(), KeyRequest{Path: identity})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevalidateKeySelection(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, identity, "changed identity metadata")
	if err := s.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("private source change accepted: %v", err)
	}
	generated := filepath.Join(paths.SSHDir, "new-key")
	generation, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyGenerate, DestinationIdentity: generated, NoPassphrase: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevalidateKeySelection(t.Context(), generation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight generated a key")
	}
	writeFixture(t, generated, "do not overwrite")
	if err := s.RevalidateKeySelection(t.Context(), generation); !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("new destination collision accepted: %v", err)
	}
}

func TestKeySelectionRejectsInPlacePrivateRewriteWithRestoredMtime(t *testing.T) {
	for _, derive := range []bool{false, true} {
		t.Run(map[bool]string{false: "reviewed-key", true: "reviewed-derivation"}[derive], func(t *testing.T) {
			paths := fixturePaths(t)
			identity := filepath.Join(paths.SSHDir, "private")
			writeFixture(t, identity, "opaque private A")
			if !derive {
				writeFixture(t, identity+".pub", string(testPublicLine(0x95, "reviewed"))+"\n")
			}
			s := newFixtureService(t, paths, DiscoverOptions{})
			plan, err := s.PlanKey(t.Context(), KeyRequest{Path: identity, AllowDerive: derive})
			if err != nil || !plan.Ready() {
				t.Fatal(plan, err)
			}
			if err := s.RevalidateKeySelection(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			rewritePrivatePreservingMtime(t, identity)
			if err := s.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("reviewed private rewrite accepted before effects: %v", err)
			}
			if _, err := s.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("reviewed private rewrite applied: %v", err)
			}
		})
	}
}

func TestVerifiedPrivateRewriteCannotReuseCachedPairProof(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "private")
	line := testPublicLine(0x96, "verified")
	writeFixture(t, identity, "opaque private A")
	writeFixture(t, identity+".pub", string(line)+"\n")
	nativeCalls := 0
	s, _ := NewService(paths, keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		nativeCalls++
		if nativeCalls != 1 || request.Name != "ssh-keygen" {
			t.Fatalf("changed identity reused native authority: %+v", request)
		}
		return RunResult{Stdout: line}, nil
	}))
	plan, err := s.PlanKey(t.Context(), KeyRequest{Path: identity})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.ApplyKey(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	material, err := s.validateKeyCandidate(key.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	rewritePrivatePreservingMtime(t, identity)
	if _, _, err := s.prepareKeySelector(material); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("cached pair selector accepted private rewrite: %v", err)
	}
	if _, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Key: key}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("cached pair bootstrap accepted private rewrite: %v", err)
	}
	if nativeCalls != 1 {
		t.Fatal("changed identity triggered an additional native command")
	}
}

func rewritePrivatePreservingMtime(t *testing.T, path string) {
	t.Helper()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the existing inode and size, and explicitly restore its mtime. The
	// change timestamp remains the evidence that reviewed private material moved.
	time.Sleep(time.Millisecond)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("B"), before.Size()-1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil || !stableFileInfo(before, after) {
		t.Fatalf("fixture failed to preserve ordinary metadata: %v", err)
	}
}
