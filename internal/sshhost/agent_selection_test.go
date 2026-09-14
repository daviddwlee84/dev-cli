package sshhost

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type agentKeyFixture struct {
	service     *Service
	ref         AgentSocketRef
	line        []byte
	fingerprint string
	available   bool
	requests    []RunRequest
}

func newAgentKeyFixture(t *testing.T) *agentKeyFixture {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	paths := fixturePaths(t)
	f := &agentKeyFixture{
		ref:  AgentSocketRef{Provider: AgentProviderBitwarden, Socket: filepath.Join(paths.Home, "agent.sock")},
		line: testPublicLine(0xb1, "vault key"), available: true,
	}
	metadata, err := ParsePublicKey(f.line)
	if err != nil {
		t.Fatal(err)
	}
	f.fingerprint = metadata.Fingerprint
	f.service, err = NewService(paths, keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		f.requests = append(f.requests, request)
		if request.Name != "ssh-add" {
			t.Fatalf("agent-only operation invoked %s", request.Name)
		}
		if f.available && reflect.DeepEqual(request.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + f.ref.Socket}) {
			return RunResult{Stdout: append(append([]byte(nil), f.line...), '\n')}, nil
		}
		return RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	acceptFileAgentSockets(f.service)
	writeFixture(t, f.ref.Socket, "")
	return f
}

func (f *agentKeyFixture) selectKey(t *testing.T) KeyCandidate {
	t.Helper()
	candidate, err := f.service.SelectAgentKey(t.Context(), f.ref, f.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func (f *agentKeyFixture) publication(t *testing.T) KeyPlan {
	t.Helper()
	plan, err := f.service.PlanKey(t.Context(), KeyRequest{
		Operation: KeyPublishAgent, Candidate: f.selectKey(t),
		PublicDestination: f.service.DefaultAgentPublicPath(f.ref.Provider, "target"),
	})
	if err != nil || !plan.Ready() {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	return plan
}

func TestExplicitAgentSnapshotsRejectPointerMutation(t *testing.T) {
	for _, field := range []string{"socket", "provider", "sources"} {
		t.Run("candidate/"+field, func(t *testing.T) {
			f := newAgentKeyFixture(t)
			candidate := f.selectKey(t)
			switch field {
			case "socket":
				candidate.Agent.Socket = filepath.Join(f.service.paths.Home, "other.sock")
			case "provider":
				candidate.Agent.Provider = AgentProvider1Password
			case "sources":
				candidate.Sources[0] = KeySourceGenerated
			}
			if _, err := f.service.PlanKey(t.Context(), KeyRequest{Candidate: candidate}); err == nil {
				t.Fatal("mutated candidate was accepted")
			}
		})
	}
	for _, operation := range []KeyOperation{KeyUse, KeyPublishAgent} {
		for _, field := range []string{"socket", "provider"} {
			t.Run(string(operation)+"/"+field, func(t *testing.T) {
				f := newAgentKeyFixture(t)
				plan := f.publication(t)
				if operation == KeyUse {
					var err error
					plan, err = f.service.PlanKey(t.Context(), KeyRequest{Candidate: f.selectKey(t)})
					if err != nil {
						t.Fatal(err)
					}
				}
				if field == "socket" {
					plan.Agent.Socket = filepath.Join(f.service.paths.Home, "other.sock")
				} else {
					plan.Agent.Provider = AgentProvider1Password
				}
				before := len(f.requests)
				if err := f.service.RevalidateKeySelection(t.Context(), plan); err == nil {
					t.Fatal("mutated plan was revalidated")
				}
				if _, err := f.service.ApplyKey(t.Context(), plan); err == nil {
					t.Fatal("mutated plan was applied")
				}
				if len(f.requests) != before {
					t.Fatal("mutated plan invoked an agent")
				}
			})
		}
	}
}

func TestNoAgentSkipsAllExplicitSockets(t *testing.T) {
	paths := fixturePaths(t)
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{
		LocalOnly: true, NoAgent: true,
		Agents: []AgentSocketRef{{Provider: AgentProviderBitwarden, Socket: filepath.Join(paths.Home, "does-not-exist.sock")}},
	})
	if err != nil || !catalog.Complete {
		t.Fatalf("no-agent should remain static: %+v %v", catalog, err)
	}
}

func TestExplicitAgentRemainsSignerWithLocalPrivateDuplicate(t *testing.T) {
	f := newAgentKeyFixture(t)
	private := filepath.Join(f.service.paths.SSHDir, "local-copy")
	writeFixture(t, private, "opaque local private identity")
	writeFixture(t, private+".pub", string(f.line)+"\n")
	candidate := f.selectKey(t)
	if candidate.IdentityFile != "" || candidate.PublicPath != "" || candidate.Agent == nil || candidate.Provenance.Private {
		t.Fatalf("exact selection inherited a local identity: %+v", candidate)
	}
	if len(f.requests) != 1 {
		t.Fatalf("exact selection queried additional sources: %+v", f.requests)
	}
	catalog, err := f.service.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{f.ref}})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	candidate = catalog.Candidates[0]
	if candidate.Agent == nil || candidate.Provenance.Private || candidate.Provenance.SecurityKeyStub || candidate.NeedsPermissionRepair || candidate.IdentityFile != private+".pub" {
		t.Fatalf("named catalog selection changed signer: %+v", candidate)
	}
	plan, err := f.service.PlanKey(t.Context(), KeyRequest{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyKey(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestAliasAndRequestedSameSocketKeepNamedProvider(t *testing.T) {
	f := newAgentKeyFixture(t)
	catalog, err := f.service.Catalog(t.Context(), KeyCatalogRequest{
		Effective: &EffectiveConfig{Alias: "target", Values: map[string][]string{"identityagent": {f.ref.Socket}}},
		Agents:    []AgentSocketRef{f.ref},
	})
	if err != nil || len(catalog.Candidates) != 1 || catalog.Candidates[0].Agent == nil || *catalog.Candidates[0].Agent != f.ref {
		t.Fatalf("same-socket attribution lost: %+v %v", catalog, err)
	}
	if catalog.Candidates[0].state.agent.identity == nil {
		t.Fatal("alias precedence discarded the explicit socket snapshot")
	}
}

func TestExplicitAgentSocketReplacementIsRejected(t *testing.T) {
	for _, phase := range []string{"inventory", "plan", "apply", "publication"} {
		t.Run(phase, func(t *testing.T) {
			f := newAgentKeyFixture(t)
			replace := func() {
				if err := os.Rename(f.ref.Socket, f.ref.Socket+".old"); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, f.ref.Socket, "replacement")
			}
			if phase == "inventory" {
				runner := f.service.runner
				f.service.runner = keyCatalogRunnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
					result, err := runner.Run(ctx, request)
					replace()
					return result, err
				})
				if _, err := f.service.SelectAgentKey(t.Context(), f.ref, f.fingerprint); !errors.Is(err, ErrSourceChanged) {
					t.Fatalf("socket changed during inventory: %v", err)
				}
				return
			}
			candidate := f.selectKey(t)
			if phase == "plan" {
				replace()
				if _, err := f.service.PlanKey(t.Context(), KeyRequest{Candidate: candidate}); !errors.Is(err, ErrSourceChanged) {
					t.Fatalf("socket changed before planning: %v", err)
				}
				return
			}
			plan := f.publication(t)
			if phase == "apply" {
				replace()
			} else {
				f.service.beforeKeyCommit = replace
			}
			if _, err := f.service.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("replaced socket accepted: %v", err)
			}
			if _, err := os.Stat(plan.PublicPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("public file was written after socket replacement: %v", err)
			}
		})
	}
}

func TestAgentPublicationChecksFinalAvailabilityAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "key-disappears", true: "canceled"}[cancel], func(t *testing.T) {
			f := newAgentKeyFixture(t)
			plan := f.publication(t)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			f.service.beforeKeyCommit = func() {
				if cancel {
					stop()
				} else {
					f.available = false
				}
			}
			if _, err := f.service.ApplyKey(ctx, plan); err == nil {
				t.Fatal("stale selection was published")
			}
			if _, err := os.Stat(plan.PublicPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unexpected publication: %v", err)
			}
		})
	}
}

func TestAgentPublicationCreatesMissingSSHDirectory(t *testing.T) {
	f := newAgentKeyFixture(t)
	if err := os.Remove(f.service.paths.SSHDir); err != nil {
		t.Fatal(err)
	}
	plan := f.publication(t)
	if _, err := os.Stat(f.service.paths.SSHDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("planning created the SSH directory")
	}
	if err := f.service.RevalidateKeySelection(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ApplyKey(t.Context(), plan)
	if err != nil || !result.Created || !result.Retained {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertFixturePrivateFile(t, plan.PublicPath)
}

func TestAgentPublicationRetainsUnknownPostCommitEffects(t *testing.T) {
	f := newAgentKeyFixture(t)
	plan := f.publication(t)
	f.service.agentPublicCommit = func(staged *stagedFile, path string, expected fileSnapshot) (bool, error) {
		published, err := commitNoReplaceObserved(staged, path, expected)
		if err != nil {
			return published, err
		}
		return published, syscall.EIO
	}
	result, err := f.service.ApplyKey(t.Context(), plan)
	if !errors.Is(err, syscall.EIO) || errors.Is(err, ErrKeyCollision) || !result.Created || !result.Retained || !result.PublicationUnknown || result.Candidate.PublicPath != plan.PublicPath {
		t.Fatalf("post-commit error lost its receipt: %+v %v", result, err)
	}
	if result.Candidate.state != nil {
		t.Fatal("unknown publication returned an actionable candidate")
	}
	if _, err := os.Stat(plan.PublicPath); err != nil {
		t.Fatalf("published public file was not retained: %v", err)
	}
}

func TestAgentPublicationCollisionDoesNotOverwrite(t *testing.T) {
	f := newAgentKeyFixture(t)
	plan := f.publication(t)
	other := string(testPublicLine(0xb2, "other")) + "\n"
	f.service.beforeKeyCommit = func() { writeFixture(t, plan.PublicPath, other) }
	result, err := f.service.ApplyKey(t.Context(), plan)
	if !errors.Is(err, ErrKeyCollision) || result.Created || result.PublicationUnknown {
		t.Fatalf("collision result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(plan.PublicPath)
	if err != nil || string(data) != other {
		t.Fatal("collision overwrote the other public key")
	}
}

func TestAgentPublicationNoopRejectsDeletedReviewedFile(t *testing.T) {
	f := newAgentKeyFixture(t)
	plan := f.publication(t)
	writeFixture(t, plan.PublicPath, string(f.line)+"\n")
	plan = f.publication(t)
	if plan.Action != ActionNoop {
		t.Fatal("identical public file was not reused")
	}
	if err := os.Remove(plan.PublicPath); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RevalidateKeySelection(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("deleted reviewed file accepted: %v", err)
	}
	if _, err := f.service.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("noop unexpectedly created a new file: %v", err)
	}
}

func TestVerifyAgentKeyPolicyPreservesNativeSelectors(t *testing.T) {
	for _, mode := range []string{"matching", "ambient-matching", "ambient-other", "none", "other", "identities-only-matching", "identities-only-other"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentKeyFixture(t)
			candidate := f.selectKey(t)
			agent, identities := f.ref.Socket, "no"
			selector := filepath.Join(f.service.paths.SSHDir, "public-selector.pub")
			if strings.HasPrefix(mode, "ambient-") {
				agent = "SSH_AUTH_SOCK"
				t.Setenv("SSH_AUTH_SOCK", f.ref.Socket)
				if mode == "ambient-other" {
					t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.service.paths.Home, "other.sock"))
				}
			} else if mode == "none" {
				agent = "none"
			} else if mode == "other" {
				agent = filepath.Join(f.service.paths.Home, "other.sock")
			} else if strings.HasPrefix(mode, "identities-only-") {
				identities = "yes"
				line := f.line
				if mode == "identities-only-other" {
					line = testPublicLine(0xb3, "different")
				}
				writeFixture(t, selector, string(line)+"\n")
			}
			runner := f.service.runner
			f.service.runner = keyCatalogRunnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
				if request.Name == "ssh" {
					if !reflect.DeepEqual(request.Args, []string{"-G", "target"}) {
						t.Fatalf("policy check logged in: %+v", request)
					}
					return RunResult{Stdout: []byte("hostname target\nidentityagent " + agent + "\nidentitiesonly " + identities + "\nidentityfile " + selector + "\n")}, nil
				}
				return runner.Run(ctx, request)
			})
			err := f.service.VerifyAgentKeyPolicy(t.Context(), "target", candidate)
			wantOK := mode == "matching" || mode == "ambient-matching" || mode == "identities-only-matching"
			if wantOK && err != nil || !wantOK && !errors.Is(err, ErrAgentPolicyMismatch) {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}
