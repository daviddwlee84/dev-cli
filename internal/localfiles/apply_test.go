package localfiles

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/lease"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const (
	testSourceMachine = "11111111-1111-4111-8111-111111111111"
	testTargetMachine = "22222222-2222-4222-8222-222222222222"
	testRemoteURL     = "https://example.test/acme/portable.git"
)

type repoPair struct {
	source  string
	target  string
	head    string
	binding Binding
	service *Service
	limits  safefile.Limits
}

func newRepoPair(t *testing.T) *repoPair {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", source)
	runGit(t, source, "config", "user.email", "test@example.test")
	runGit(t, source, "config", "user.name", "Test")
	writeTestFile(t, filepath.Join(source, ".gitignore"), ".env\n.env.*\n.mcp/\nsecret-*/\n")
	writeTestFile(t, filepath.Join(source, "README.md"), "tracked\n")
	runGit(t, source, "add", ".gitignore", "README.md")
	runGit(t, source, "commit", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, root, "clone", remote, target)
	runGit(t, source, "remote", "set-url", "origin", testRemoteURL)
	runGit(t, target, "remote", "set-url", "origin", testRemoteURL)
	head := runGit(t, source, "rev-parse", "HEAD")
	cfg := config.Default()
	cfg.Paths.ScanRoots = nil
	cfg.Paths.RepoPaths = []string{target}
	cfg.Paths.StateDir = filepath.Join(root, "state")
	service := NewService(cfg)
	service.Platform = "linux"
	service.StoreRoot = filepath.Join(root, "target-state", "local-files")
	service.Authority = lease.New(filepath.Join(root, "target-state", "leases"))
	service.LoadMachineID = func(context.Context) (string, error) { return testTargetMachine, nil }
	return &repoPair{
		source: source, target: target, head: head, service: service,
		limits: cfg.LocalFiles.Limits(),
		binding: Binding{
			RemoteIdentity: catalog.NormalizeRemoteIdentity(testRemoteURL), Branch: "main", HeadOID: head,
			SourceMachine: testSourceMachine, TargetMachine: testTargetMachine,
		},
	}
}

func (p *repoPair) prepare(t *testing.T, patterns []string, replace bool) (*Source, PlanResponse) {
	t.Helper()
	inputs := make([]Pattern, len(patterns))
	for index, pattern := range patterns {
		inputs[index] = Pattern{Value: pattern, Source: "test"}
	}
	source, report, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: p.source, Binding: p.binding, Patterns: inputs, Limits: p.limits, Replace: replace,
	})
	if err != nil {
		t.Fatalf("prepare source: %v; report=%+v", err, report)
	}
	plan, err := p.service.Plan(t.Context(), source.Request())
	if err != nil {
		t.Fatalf("plan target: %v", err)
	}
	return source, plan
}

func (p *repoPair) apply(t *testing.T, source *Source, plan PlanResponse, retain bool) (ApplyEnvelope, ApplyResponse) {
	t.Helper()
	envelope, err := source.BuildEnvelope(t.Context(), plan, retain)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p.service.Apply(t.Context(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	return envelope, response
}

func TestPlanCreateApplyCurrentAndIdempotentRetry(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "TOKEN=top-secret\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	if len(plan.Files) != 1 || plan.Files[0].State != StateReady || plan.Files[0].Action != actionCreate {
		t.Fatalf("create plan = %+v", plan.Files)
	}
	envelope, response := p.apply(t, source, plan, false)
	if response.Files[0].State != StateCreated {
		t.Fatalf("apply response = %+v", response.Files)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "TOKEN=top-secret\n", 0o600)

	retried, err := p.service.Apply(t.Context(), envelope)
	if err != nil || retried.Files[0].State != StateCreated {
		t.Fatalf("idempotent retry = %+v, %v", retried, err)
	}

	currentSource, currentPlan := p.prepare(t, []string{".env"}, false)
	if currentPlan.Files[0].State != StateCurrent || currentPlan.Files[0].Action != actionCurrent {
		t.Fatalf("current plan = %+v", currentPlan.Files)
	}
	currentEnvelope, err := currentSource.BuildEnvelope(t.Context(), currentPlan, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(currentEnvelope.Payloads) != 0 {
		t.Fatal("current file content should not cross the wire")
	}
	_, currentResult := p.apply(t, currentSource, currentPlan, false)
	if currentResult.Files[0].State != StateCurrent {
		t.Fatalf("current apply = %+v", currentResult.Files)
	}
}

func TestConflictRequiresReplaceAndRestoresOriginalModeOnRollback(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "source-secret\n")
	writeTestFile(t, filepath.Join(p.target, ".env"), "target-secret\n")
	if err := os.Chmod(filepath.Join(p.target, ".env"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, blocked := p.prepare(t, []string{".env"}, false)
	if blocked.Files[0].State != StateBlockedConflict || blocked.Files[0].Action != "" {
		t.Fatalf("non-replace plan = %+v", blocked.Files)
	}

	source, plan := p.prepare(t, []string{".env"}, true)
	if plan.Files[0].Action != actionReplace || plan.Files[0].State != StateReady {
		t.Fatalf("replace plan = %+v", plan.Files)
	}
	p.service.Fault = func(point, path string) error {
		if point == "after-publish" {
			return errors.New("injected publish failure")
		}
		return nil
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("faulted apply error = %v", err)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "target-secret\n", 0o640)

	p.service.Fault = nil
	retry, err := p.service.Apply(t.Context(), envelope)
	if err != nil || retry.Files[0].State != StateReplaced {
		t.Fatalf("retry after rollback = %+v, %v", retry, err)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "source-secret\n", 0o600)
}

func TestMultiFileFailureRollsBackCreatesAndReplacements(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "new-env\n")
	writeTestFile(t, filepath.Join(p.source, ".env.local"), "new-local\n")
	writeTestFile(t, filepath.Join(p.target, ".env"), "old-env\n")
	source, plan := p.prepare(t, []string{".env*"}, true)
	p.service.Fault = func(point, path string) error {
		if point == "after-publish" && path == ".env.local" {
			return errors.New("second publication failed")
		}
		return nil
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("faulted multi-file apply succeeded")
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "old-env\n", 0o644)
	if _, err := os.Lstat(filepath.Join(p.target, ".env.local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file survived rollback: %v", err)
	}
}

func TestRetryRecoversCrashBeforeAndAfterFirstJournal(t *testing.T) {
	for _, faultPoint := range []string{"after-store-create", "after-journal"} {
		t.Run(faultPoint, func(t *testing.T) {
			p := newRepoPair(t)
			writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
			source, plan := p.prepare(t, []string{".env"}, false)
			envelope, err := source.BuildEnvelope(t.Context(), plan, false)
			if err != nil {
				t.Fatal(err)
			}
			p.service.Fault = func(point, _ string) error {
				if point == faultPoint {
					return errors.New("injected initialization crash")
				}
				return nil
			}
			if _, err := p.service.Apply(t.Context(), envelope); err == nil {
				t.Fatal("faulted initialization succeeded")
			}
			p.service.Fault = nil
			response, err := p.service.Apply(t.Context(), envelope)
			if err != nil || response.Files[0].State != StateCreated {
				t.Fatalf("retry after %s = %+v, %v", faultPoint, response, err)
			}
			assertTestFile(t, filepath.Join(p.target, ".env"), "secret\n", 0o600)
		})
	}
}

func TestRetryDiscardsLegacyUnjournaledPayloadStore(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newOperationStore(p.service.StoreRoot, envelope.Request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureOperation(); err != nil {
		t.Fatal(err)
	}
	if err := store.ensurePayloadLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.writeManifest(envelope); err != nil {
		t.Fatal(err)
	}
	if err := store.writeBlob(store.blobPath(0), []byte("secret\n"), false); err != nil {
		t.Fatal(err)
	}
	response, err := p.service.Apply(t.Context(), envelope)
	if err != nil || response.Files[0].State != StateCreated {
		t.Fatalf("legacy unjournaled retry = %+v, %v", response, err)
	}
}

func TestRollbackPreservesConcurrentlyOwnedParentDirectory(t *testing.T) {
	p := newRepoPair(t)
	path := ".mcp/nested/config.json"
	writeTestFile(t, filepath.Join(p.source, path), "source\n")
	source, plan := p.prepare(t, []string{path}, false)
	p.service.Fault = func(point, _ string) error {
		if point == "after-publish" {
			writeTestFile(t, filepath.Join(p.target, ".mcp", "other-process.txt"), "other\n")
			return errors.New("injected publication crash")
		}
		return nil
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("faulted nested apply succeeded")
	}
	if _, err := os.Lstat(filepath.Join(p.target, path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published file survived rollback: %v", err)
	}
	assertTestFile(t, filepath.Join(p.target, ".mcp", "other-process.txt"), "other\n", 0o644)
}

func TestRetryPreservesIdenticalFileWithoutPublishProvenance(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "same-bytes\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	p.service.Fault = func(point, _ string) error {
		if point == "after-applying-journal" {
			return errors.New("simulated process crash")
		}
		return nil
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("simulated crash succeeded")
	}
	writeTestFile(t, filepath.Join(p.target, ".env"), "same-bytes\n")
	if err := os.Chmod(filepath.Join(p.target, ".env"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.service.Fault = nil
	if _, err := p.service.Apply(t.Context(), envelope); err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("retry without publication provenance = %v", err)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "same-bytes\n", 0o600)
}

func TestRollbackRestoresModeWhenReplacementBytesAreIdentical(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission modes are not available")
	}
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "same-bytes\n")
	writeTestFile(t, filepath.Join(p.target, ".env"), "same-bytes\n")
	if err := os.Chmod(filepath.Join(p.target, ".env"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, plan := p.prepare(t, []string{".env"}, true)
	if plan.Files[0].Action != actionReplace || plan.Files[0].TargetMode != "0644" {
		t.Fatalf("mode-only replacement plan = %+v", plan.Files[0])
	}
	p.service.Fault = func(point, _ string) error {
		if point == "after-publish" {
			return errors.New("injected failure")
		}
		return nil
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("mode-only rollback error = %v", err)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "same-bytes\n", 0o644)
}

func TestRemoteAliasesShareCanonicalCheckoutLease(t *testing.T) {
	p := newRepoPair(t)
	const alternateURL = "https://mirror.example.test/acme/portable.git"
	runGit(t, p.target, "remote", "add", "mirror", alternateURL)
	secondCheckout := filepath.Join(filepath.Dir(p.source), "source-alias")
	runGit(t, filepath.Dir(p.source), "clone", p.source, secondCheckout)
	runGit(t, secondCheckout, "remote", "set-url", "origin", alternateURL)
	writeTestFile(t, filepath.Join(p.source, ".env"), "one\n")
	writeTestFile(t, filepath.Join(secondCheckout, ".env.local"), "two\n")
	firstSource, firstPlan := p.prepare(t, []string{".env"}, false)
	secondBinding := p.binding
	secondBinding.RemoteIdentity = catalog.NormalizeRemoteIdentity(alternateURL)
	secondSource, report, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: secondCheckout, Binding: secondBinding,
		Patterns: []Pattern{{Value: ".env.local", Source: "test"}}, Limits: p.limits,
	})
	if err != nil {
		t.Fatalf("prepare alias source: %v; report=%+v", err, report)
	}
	secondPlan, err := p.service.Plan(t.Context(), secondSource.Request())
	if err != nil {
		t.Fatal(err)
	}
	firstEnvelope, err := firstSource.BuildEnvelope(t.Context(), firstPlan, false)
	if err != nil {
		t.Fatal(err)
	}
	secondEnvelope, err := secondSource.BuildEnvelope(t.Context(), secondPlan, false)
	if err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	p.service.Fault = func(point, path string) error {
		if point != "before-publish" {
			return nil
		}
		switch path {
		case ".env":
			close(firstEntered)
			<-releaseFirst
		case ".env.local":
			close(secondEntered)
		}
		return nil
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := p.service.Apply(context.Background(), firstEnvelope)
		firstDone <- err
	}()
	<-firstEntered
	go func() {
		_, err := p.service.Apply(context.Background(), secondEnvelope)
		secondDone <- err
	}()
	select {
	case <-secondEntered:
		t.Fatal("remote aliases bypassed the canonical checkout lease")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second alias apply did not proceed after lease release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestBuildEnvelopeRejectsSourceBindingDrift(t *testing.T) {
	for _, drift := range []string{"head", "branch", "remote"} {
		t.Run(drift, func(t *testing.T) {
			p := newRepoPair(t)
			writeTestFile(t, filepath.Join(p.source, ".env"), "unchanged\n")
			source, plan := p.prepare(t, []string{".env"}, false)
			switch drift {
			case "head":
				writeTestFile(t, filepath.Join(p.source, "tracked.txt"), "new commit\n")
				runGit(t, p.source, "add", "tracked.txt")
				runGit(t, p.source, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "drift")
			case "branch":
				runGit(t, p.source, "switch", "-c", "other")
			case "remote":
				runGit(t, p.source, "remote", "set-url", "origin", "https://example.test/other/repository.git")
			}
			if _, err := source.BuildEnvelope(t.Context(), plan, false); !errors.Is(err, ErrDrift) {
				t.Fatalf("%s drift error = %v, want ErrDrift", drift, err)
			}
		})
	}
}

func TestRetainForEvictControlsRecoveryBlobLifetime(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "retained\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	envelope, _ := p.apply(t, source, plan, true)
	store, exists, err := openOperationStore(p.service.StoreRoot, envelope.Request.RequestID)
	if err != nil || !exists {
		t.Fatalf("operation store = %v, %v", exists, err)
	}
	blob, err := os.Stat(store.blobPath(0))
	if err != nil || blob.Mode().Perm() != 0o600 {
		t.Fatalf("retained blob = %+v, %v", blob, err)
	}
	body, err := os.ReadFile(store.blobPath(0))
	if err != nil || string(body) != "retained\n" {
		t.Fatalf("retained blob content unavailable: %q, %v", body, err)
	}

	p2 := newRepoPair(t)
	writeTestFile(t, filepath.Join(p2.source, ".env"), "ephemeral\n")
	source2, plan2 := p2.prepare(t, []string{".env"}, false)
	envelope2, _ := p2.apply(t, source2, plan2, false)
	store2, _, _ := openOperationStore(p2.service.StoreRoot, envelope2.Request.RequestID)
	if _, err := os.Stat(store2.blobDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-retained blob directory remains: %v", err)
	}
}

func TestIgnoredDisagreementBlocksTarget(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	writeTestFile(t, filepath.Join(p.target, ".gitignore"), ".other\n")
	_, plan := p.prepare(t, []string{".env"}, false)
	if plan.Files[0].State != StateBlockedIneligible {
		t.Fatalf("ignore disagreement plan = %+v", plan.Files)
	}
	if _, err := os.Stat(filepath.Join(p.target, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("report-only plan mutated target")
	}
}

func TestTargetRequiresUniqueCloneCheckedOutBranchAndExactOID(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	inputs := []Pattern{{Value: ".env", Source: "test"}}
	source, _, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: p.source, Binding: p.binding, Patterns: inputs, Limits: p.limits,
	})
	if err != nil {
		t.Fatal(err)
	}

	absentConfig := p.service.Config
	absentConfig.Paths.RepoPaths = []string{filepath.Join(t.TempDir(), "missing")}
	absent := NewService(absentConfig)
	absent.Platform, absent.LoadMachineID = "linux", p.service.LoadMachineID
	if _, err := absent.Plan(t.Context(), source.Request()); !targetErrorIs(err, TargetAbsent) {
		t.Fatalf("absent target error = %v", err)
	}

	runGit(t, p.target, "switch", "--detach", p.head)
	if _, err := p.service.Plan(t.Context(), source.Request()); !targetErrorIs(err, TargetFetchedOnly) {
		t.Fatalf("fetched-only target error = %v", err)
	}
	runGit(t, p.target, "switch", "main")
	writeTestFile(t, filepath.Join(p.target, "drift.txt"), "drift\n")
	runGit(t, p.target, "add", "drift.txt")
	runGit(t, p.target, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "drift")
	if _, err := p.service.Plan(t.Context(), source.Request()); !targetErrorIs(err, TargetStale) {
		t.Fatalf("stale target error = %v", err)
	}
}

func TestTargetIdentityRequiresFetchURLMatch(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	source, _, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: p.source, Binding: p.binding,
		Patterns: []Pattern{{Value: ".env", Source: "test"}}, Limits: p.limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, p.target, "remote", "set-url", "origin", "https://example.test/other/repository.git")
	runGit(t, p.target, "remote", "set-url", "--push", "origin", testRemoteURL)
	if _, err := p.service.Plan(t.Context(), source.Request()); !targetErrorIs(err, TargetAbsent) {
		t.Fatalf("push-only identity match authorized target: %v", err)
	}
}

func TestTargetDiscoveryFailureIsNotSilentlySkipped(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	source, _, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: p.source, Binding: p.binding,
		Patterns: []Pattern{{Value: ".env", Source: "test"}}, Limits: p.limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(filepath.Dir(p.target), "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(broken, ".git"), "gitdir: ../missing-git-dir\n")
	p.service.Config.Paths.RepoPaths = append(p.service.Config.Paths.RepoPaths, broken)
	if _, err := p.service.Plan(t.Context(), source.Request()); err == nil || !strings.Contains(err.Error(), "inspect discovered target repository") {
		t.Fatalf("incomplete target discovery error = %v", err)
	}
}

func TestApplyEnvelopeRejectsNonCanonicalBase64(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "secret\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded := envelope.Payloads[0].Content
	if _, err := base64.StdEncoding.DecodeString(encoded[:4] + "\n" + encoded[4:]); err != nil {
		t.Fatalf("test input must remain decodable: %v", err)
	}
	envelope.Payloads[0].Content = encoded[:4] + "\n" + encoded[4:]
	if err := envelope.Validate(); err == nil || !strings.Contains(err.Error(), "canonical base64") {
		t.Fatalf("non-canonical payload validation = %v", err)
	}
}

func TestPlanAndApplyBindExactTargetMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission modes are not available")
	}
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "new\n")
	writeTestFile(t, filepath.Join(p.target, ".env"), "old\n")
	if err := os.Chmod(filepath.Join(p.target, ".env"), 0o640); err != nil {
		t.Fatal(err)
	}
	source, plan := p.prepare(t, []string{".env"}, true)
	if plan.Files[0].TargetMode != "0640" {
		t.Fatalf("planned target mode = %q", plan.Files[0].TargetMode)
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(p.target, ".env"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); !errors.Is(err, ErrDrift) {
		t.Fatalf("mode drift apply error = %v", err)
	}
	assertTestFile(t, filepath.Join(p.target, ".env"), "old\n", 0o600)
}

func TestRollbackRemovesRetainedRecoveryPayloads(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "new\n")
	writeTestFile(t, filepath.Join(p.target, ".env"), "old\n")
	source, plan := p.prepare(t, []string{".env"}, true)
	p.service.Fault = func(point, _ string) error {
		if point == "after-publish" {
			return errors.New("injected failure")
		}
		return nil
	}
	envelope, err := source.BuildEnvelope(t.Context(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("faulted retained apply succeeded")
	}
	store, exists, err := openOperationStore(p.service.StoreRoot, envelope.Request.RequestID)
	if err != nil || !exists {
		t.Fatalf("operation store = %v, %v", exists, err)
	}
	for _, path := range []string{store.blobDir(), store.rollbackDir(), store.manifestPath()} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rolled-back payload remains at %s: %v", path, err)
		}
	}
}

func TestCompletedRetryRejectsSymlinkedJournalAndBlob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges")
	}
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "retained\n")
	source, plan := p.prepare(t, []string{".env"}, false)
	envelope, _ := p.apply(t, source, plan, true)
	store, exists, err := openOperationStore(p.service.StoreRoot, envelope.Request.RequestID)
	if err != nil || !exists {
		t.Fatalf("operation store = %v, %v", exists, err)
	}
	if err := os.Remove(store.blobPath(0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.target, ".env"), store.blobPath(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("retry followed a symlinked retained blob")
	}

	if err := os.Remove(store.blobPath(0)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.blobPath(0), []byte("retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store.manifestPath(), store.journalPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Apply(t.Context(), envelope); err == nil {
		t.Fatal("retry followed a symlinked journal")
	}
}

func targetErrorIs(err error, code TargetCode) bool {
	var target *TargetError
	return errors.As(err, &target) && target.Code == code
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), directory, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, body)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, %v; want %o", path, info.Mode().Perm(), err, mode)
		}
	}
}
