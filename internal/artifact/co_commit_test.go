package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

const coCommitTestRun = "72b5c55e-d964-45cd-b040-cb29d0d7af05"
const coCommitTestSession = "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"

// This installed helper fixture exercises only temporary repositories. It is a
// protocol peer, not a substitute for integration with the canonical helper.
const coCommitTestHelper = `import hashlib, json, os, pathlib, subprocess, sys
state_path = pathlib.Path(os.environ["DEV_TEST_CO_COMMIT_STATE"])
state = json.loads(state_path.read_text())
args = sys.argv[1:]
op = args.pop(0)
def save(): state_path.write_text(json.dumps(state))
def digest(data): return hashlib.sha256(data).hexdigest()
def git(*args, data=None):
    return subprocess.check_output(["git", *args], input=data, cwd=state["root"])
def arg(name, default=""):
    return args[args.index(name)+1] if name in args else default
state.setdefault("calls", []).append(op)
save()
mode_key = "inspect_context" if op == "inspect" and "--context" in args else "inspect_request" if op == "inspect" else op
mode = state.get("modes", {}).get(mode_key, "")
if mode == "invalid_json":
    print("not json: PRIVATE_TEST_SECRET")
    raise SystemExit(0)
if mode == "multiple_json":
    print('{} {}')
    raise SystemExit(0)
if mode == "overflow":
    print("X" * ((1 << 20) + 1))
    raise SystemExit(0)
if mode == "sensitive_failure":
    print("PRIVATE_TEST_SECRET stdout")
    print("PRIVATE_TEST_SECRET stderr", file=sys.stderr)
    raise SystemExit(1)
if mode in ["sensitive_json_failure", "known_status_sensitive_action"]:
    print(json.dumps(dict(status="review_required" if mode == "known_status_sensitive_action" else "private_test_secret", next_action="retain_private_test_secret")))
    raise SystemExit(1)
if op == "capabilities":
    caps = ["inspect_v2", "authentic_context_v2", "queue_idempotent_v2", "sanitation_receipt_v2", "review_v2", "exact_export_v2", "no_cloud_v2", "prepare_only_v2", "revision_guard_v2", "reconcile_only_v2"]
    if mode == "old_protocol": caps.remove("no_cloud_v2")
    print(json.dumps(dict(schema_version=2, protocol_version=2, helper_revision="c"*64, capabilities=caps)))
elif op == "inspect":
    observation = dict(state["observation"])
    if "--context" in args:
        observation["authentic_context"] = state.get("authentic", True)
        state["received_context"] = {k: os.environ.get(k) for k in ["AGENT_HISTORY_RUN_ID", "AGENT_HISTORY_REQUEST_PATH"]}
        save()
    observation.update(state.get("observation_override", {}))
    # Unknown raw helper fields must never be echoed by typed native projections.
    observation["raw_finding"] = "PRIVATE_TEST_SECRET"
    print(json.dumps(observation))
elif op == "queue":
    state["queue_count"] = state.get("queue_count", 0) + 1
    observation = state["observation"]
    message = pathlib.Path(arg("--message-file")).read_bytes()
    state["message"] = message.decode()
    observation.update(request_id=observation["run_id"], request_revision="a"*64, provider="claude", session_id=arg("--session-id"), specstory_path=arg("--specstory-path"), plan_policy="none" if "--no-plan" in args else "path", plan_path=arg("--plan"), message_sha256=digest(message), normalized_message_sha256=digest(message.decode().strip().encode()), input_index_tree=git("write-tree").decode().strip(), state="pending")
    request = pathlib.Path(observation["request_path"])
    request.parent.mkdir(parents=True, exist_ok=True)
    request.write_text(json.dumps(observation))
    if state.get("obstruct_store"):
        pathlib.Path(state["store_dir"]).write_text("block native binding")
    save()
    ack = dict(kind="agent_history_queue_ack", schema_version=2, request_id=observation["request_id"], request_path=observation["request_path"], request_revision=observation["request_revision"], helper_revision=observation["helper_revision"])
    fault = state.get("ack_fault", "")
    if fault == "kind": ack["kind"] = "other_ack"
    if fault == "schema_version": ack["schema_version"] = 1
    if fault == "request_id": ack["request_id"] = "11111111-2222-3333-4444-555555555555"
    if fault == "request_path": ack["request_path"] = str(request.parent.parent / "11111111-2222-3333-4444-555555555555" / "request.json")
    if fault == "relative_path": ack["request_path"] = ".git/request.json"
    if fault == "request_revision": ack["request_revision"] = "e"*64
    if fault == "helper_revision": ack["helper_revision"] = "e"*64
    if fault == "empty": ack = {}
    reply = dict(status="queued")
    if fault != "missing": reply["queue_ack"] = ack
    print(json.dumps(reply))
elif op == "finalize":
    if "--preview-review" in args:
        observation = state["observation"]
        run_dir = pathlib.Path(observation["request_path"]).parent
        finding = dict(finding_id="e"*64, path=observation["specstory_path"], kind="scanner", masked_value="[REDACTED]", occurrence_count=1, complete=True, raw_value="PRIVATE_TEST_SECRET")
        if state.get("review_fault") == "raw_mask": finding["masked_value"] = "PRIVATE_TEST_SECRET"
        preview = dict(schema_version=2, protocol_version=2, status="review_preview", request_revision=observation["request_revision"], receipt_revision=observation.get("receipt_revision", ""), prepared_tree=observation.get("prepared_tree", ""), review_status="unresolved", reviewable=True, findings=[finding], private_receipt_path=str(run_dir / "sanitation-receipt.json"), private_review_path=str(run_dir / "review.json"), commit_attempted=False, raw_finding="PRIVATE_TEST_SECRET")
        print(json.dumps(preview))
        raise SystemExit(0)
    observation = state["observation"]
    state.setdefault("expected_revisions", []).append(arg("--expected-revision"))
    if arg("--expected-revision") != observation["journal_revision"]:
        save()
        print(json.dumps(dict(status="stale_revision")))
        raise SystemExit(1)
    if "--reconcile-only" in args:
        state["reconcile_count"] = state.get("reconcile_count", 0) + 1
        if "committed_observation" in state:
            state["observation"] = state["committed_observation"]
            save()
            print(json.dumps(dict(status="done")))
            raise SystemExit(0)
        save()
        print(json.dumps(dict(status="reconciliation_required")))
        raise SystemExit(1)
    count_key = "prepare_count" if "--prepare-only" in args else "finalize_count"
    state[count_key] = state.get(count_key, 0) + 1
    paths = [observation["specstory_path"]]
    if observation["plan_policy"] == "path": paths.append(observation["plan_path"])
    git("add", "--", *paths)
    tree = git("write-tree").decode().strip()
    expected = state["message"].strip() + "\n\nAgent-History-Request: " + observation["run_id"] + "\n"
    observation.update(prepared_tree=tree, normalized_message_sha256=digest(git("stripspace", data=expected.encode())), composed_message_sha256=digest(expected.encode()))
    if "--prepare-only" in args:
        observation["state"] = "prepared"
        observation["journal_revision"] = digest(b"prepared")
        save()
        print(json.dumps(dict(status="prepared")))
        raise SystemExit(0)
    actual = expected
    fault = state.get("commit_fault", "")
    if fault == "message": actual = "UNREVIEWED BODY\n\n" + expected
    if fault == "nonce": actual = actual.replace(observation["run_id"], "11111111-2222-3333-4444-555555555555")
    if fault == "duplicate_nonce": actual += "Agent-History-Request: " + observation["run_id"] + "\n"
    parent_args = [] if fault == "parent" else ["-p", observation["head_oid"]]
    commit = git("-c", "commit.gpgsign=false", "commit-tree", tree, *parent_args, data=actual.encode()).decode().strip()
    git("update-ref", "HEAD", commit)
    observation.update(state="done", status="done", prepared_tree=observation["input_index_tree"] if fault == "tree" else tree, commit_oid=commit, commit_proven=True, receipt_revision="b"*64, composed_message_sha256=digest(expected.encode()))
    observation["journal_revision"] = digest(b"done")
    if state.get("uncertain_commit"):
        state["committed_observation"] = dict(observation)
        observation.update(state="committing", status="committing", commit_proven=False, journal_revision=digest(b"committing"))
        save()
        print(json.dumps(dict(status="reconciliation_required")))
        raise SystemExit(1)
    if state.get("native_write_drift"):
        native = pathlib.Path(state["native_intent_path"])
        record = json.loads(native.read_text())
        record["failure_code"] = "concurrent_observation"
        native.write_text(json.dumps(record))
    save()
    print(json.dumps(dict(status="done")))
else:
    raise SystemExit(2)
`

type coCommitFixture struct {
	t       *testing.T
	service *Service
	repo    *gittest.Repo
	request CoCommitPrepareRequest
	state   string
}

func newCoCommitFixture(t *testing.T) *coCommitFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native co-commit POSIX helper contract")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python fixture unavailable")
	}
	isolateGitConfig(t)
	r := gittest.New(t)
	root, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	r.Root = root
	r.Git("config", "core.hooksPath", t.TempDir())
	r.Write("feature.go", "package feature\n")
	r.Git("add", "feature.go")
	r.Write(".specstory/history/exact.md", "placeholder")
	transcript := filepath.Join(root, ".specstory/history/exact.md")
	writeTranscript(t, transcript, "Claude Code", coCommitTestSession, "selected evidence")
	private, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// User-owned executable wrappers keep protocol tests independent of system
	// install metadata. Production tool trust is reviewed separately; these never
	// change any real interpreter or shell binary.
	tools := filepath.Join(private, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python3", "bash"} {
		actual, err := exec.LookPath(name)
		if err != nil {
			t.Skip(err)
		}
		actual, err = filepath.EvalSymlinks(actual)
		if err != nil {
			t.Fatal(err)
		}
		quoted := "'" + strings.ReplaceAll(actual, "'", "'\\''") + "'"
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexec "+quoted+" \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	helper := filepath.Join(private, "installed", "scripts")
	for _, dir := range []string{helper, filepath.Join(private, "installed", "assets")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"_post_session.py": coCommitTestHelper, "queue-agent-commit.sh": "#!/bin/sh\nexit 99\n", "finalize-agent-commit.sh": "#!/bin/sh\nexit 99\n"} {
		if err := os.WriteFile(filepath.Join(helper, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	message := filepath.Join(private, "message.txt")
	if err := os.WriteFile(message, []byte("feat: reviewed snapshot\n\nProduct change only.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &coCommitFixture{t: t, repo: r, state: filepath.Join(private, "fixture.json"), service: &Service{Store: NewStore(filepath.Join(private, "intents"))}}
	f.service.ScanStaged = func(context.Context, string, []string) error {
		t.Error("co-commit called legacy staged scanner")
		return errors.New("legacy scanner forbidden")
	}
	f.request = CoCommitPrepareRequest{Worktree: root, Session: "claude:" + coCommitTestSession, SpecStoryPath: transcript, NoPlan: true, MessageFile: message, HelperDir: helper}
	gitDir := r.Git("rev-parse", "--path-format=absolute", "--git-dir")
	requestPath := coCommitRequestPath(gitDir, coCommitTestRun)
	f.writeState(map[string]any{
		"root": root, "store_dir": f.service.Store.Dir, "authentic": true,
		"observation": map[string]any{
			"schema_version": 2, "protocol_version": 2, "helper_revision": strings.Repeat("c", 64), "journal_revision": strings.Repeat("d", 64),
			"status": "running", "state": "running", "run_id": coCommitTestRun, "request_id": "", "request_path": requestPath, "automatic_commit": false,
			"worktree_root": root, "git_dir": gitDir, "head_ref": r.Git("symbolic-ref", "HEAD"), "head_oid": r.Git("rev-parse", "HEAD"),
			"child_exit_code": 0, "child_signal": nil, "group_quiescent": true, "sync_status": "proven", "freshness_status": "proven", "review_status": "not_required", "commit_proven": false,
		},
	})
	t.Setenv("DEV_TEST_CO_COMMIT_STATE", f.state)
	t.Setenv("AGENT_HISTORY_RUN_ID", coCommitTestRun)
	t.Setenv("AGENT_HISTORY_REQUEST_PATH", requestPath)
	return f
}

func (f *coCommitFixture) readState() map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		f.t.Fatal(err)
	}
	return state
}

func (f *coCommitFixture) writeState(state map[string]any) {
	f.t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.state, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coCommitFixture) mutateState(mutate func(map[string]any)) {
	state := f.readState()
	mutate(state)
	f.writeState(state)
}

func (f *coCommitFixture) prepare() *CoCommitResult {
	f.t.Helper()
	result, err := f.service.PrepareCoCommit(f.t.Context(), f.request)
	if err != nil {
		f.t.Fatalf("prepare: result=%+v err=%v", result, err)
	}
	if result.Status != "queued" || result.Intent == nil || result.Revision == "" {
		f.t.Fatalf("incomplete native handoff: %+v", result)
	}
	return result
}

func (f *coCommitFixture) finalize(prepared *CoCommitResult) (*CoCommitResult, error) {
	return f.service.FinalizeCoCommit(f.t.Context(), CoCommitFinalizeRequest{IntentID: prepared.Intent.ID, AllowCommit: true, Guard: func(context.Context, string, []string) error { return nil }})
}

func requireCoCommitCode(t *testing.T, err error, code string) {
	t.Helper()
	var safe *CoCommitError
	if !errors.As(err, &safe) || safe.Code != code {
		t.Fatalf("error=%v; want CoCommitError %s", err, code)
	}
}

func TestCoCommitRequiresAuthenticWrapper(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fake context", true: "absent context"}[missing], func(t *testing.T) {
			f := newCoCommitFixture(t)
			if missing {
				t.Setenv("AGENT_HISTORY_REQUEST_PATH", "")
				t.Setenv("AGENT_HISTORY_RUN_ID", "")
			} else {
				f.mutateState(func(s map[string]any) { s["authentic"] = false })
			}
			index := f.repo.Git("write-tree")
			_, err := f.service.PrepareCoCommit(t.Context(), f.request)
			requireCoCommitCode(t, err, "requires_wrapper")
			if f.readState()["queue_count"] != nil || f.repo.Git("write-tree") != index {
				t.Fatal("missing real wrapper queued or modified index")
			}
		})
	}
}

func TestCoCommitRequiresExplicitFeatureOnlySnapshot(t *testing.T) {
	for _, kind := range []string{"empty index", "artifact staged", "missing selector", "implicit plan", "both plan choices", "managed trailer", "foreign index"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoCommitFixture(t)
			switch kind {
			case "empty index":
				f.repo.Git("reset", "--", "feature.go")
			case "artifact staged":
				f.repo.Git("add", "--", f.request.SpecStoryPath)
			case "missing selector":
				f.request.SpecStoryPath = ""
			case "implicit plan":
				f.request.NoPlan = false
			case "both plan choices":
				f.request.Plan = ".claude/plans/selected.md"
			case "managed trailer":
				if err := os.WriteFile(f.request.MessageFile, []byte("feat: test\n\nAgent-History-Request: foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "foreign index":
				t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "foreign-index"))
			}
			if _, err := f.service.PrepareCoCommit(t.Context(), f.request); err == nil {
				t.Fatal("unreviewed snapshot accepted")
			}
			if f.readState()["queue_count"] != nil {
				t.Fatal("invalid snapshot queued")
			}
		})
	}
}

func TestCoCommitQueuesOnceAndRepairsSameRunBinding(t *testing.T) {
	f := newCoCommitFixture(t)
	index, head := f.repo.Git("write-tree"), f.repo.Git("rev-parse", "HEAD")
	prepared := f.prepare()
	requireCoCommitQueueAck(t, f, prepared)
	f.request.RunID = coCommitTestRun
	bound, err := f.service.PrepareCoCommit(t.Context(), f.request)
	if err != nil || bound.Status != "bound" || bound.Intent.ID != prepared.Intent.ID || bound.Revision != prepared.Revision {
		t.Fatalf("binding repair=%+v %v", bound, err)
	}
	if coCommitResultAck(t, bound) != nil {
		t.Fatal("existing-run binding repair invented a queue ACK")
	}
	state := f.readState()
	if state["queue_count"] != float64(1) || f.repo.Git("write-tree") != index || f.repo.Git("rev-parse", "HEAD") != head {
		t.Fatal("binding repair requeued or changed Git")
	}
	data, _ := json.Marshal(prepared)
	if strings.Contains(string(data), "PRIVATE_TEST_SECRET") {
		t.Fatal("raw helper finding escaped typed observation")
	}
}

func TestCoCommitQueueRetainsRecoverableIDWhenNativeBindingFails(t *testing.T) {
	for _, inspectFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "store unavailable", true: "post queue inspect unavailable"}[inspectFailure], func(t *testing.T) {
			f := newCoCommitFixture(t)
			f.mutateState(func(s map[string]any) {
				if inspectFailure {
					s["modes"] = map[string]any{"inspect_request": "invalid_json"}
				} else {
					s["obstruct_store"] = true
				}
			})
			partial, err := f.service.PrepareCoCommit(t.Context(), f.request)
			if err == nil || partial == nil || partial.RequestID != coCommitTestRun {
				t.Fatalf("queue recovery lost exact ID: %+v %v", partial, err)
			}
			requireCoCommitQueueAck(t, f, partial)
			if !inspectFailure {
				if err := os.Remove(f.service.Store.Dir); err != nil {
					t.Fatal(err)
				}
			}
			f.mutateState(func(s map[string]any) { delete(s, "modes"); delete(s, "obstruct_store") })
			f.request.RunID = coCommitTestRun
			bound, err := f.service.PrepareCoCommit(t.Context(), f.request)
			if err != nil || bound.Status != "bound" || f.readState()["queue_count"] != float64(1) {
				t.Fatalf("exact repair requeued: %+v %v", bound, err)
			}
			if coCommitResultAck(t, bound) != nil {
				t.Fatal("repair without queue invocation invented an ACK")
			}
		})
	}
}

func TestCoCommitRejectsBoundAuthorityDrift(t *testing.T) {
	for _, kind := range []string{"request revision", "selector", "plan policy", "provider", "head", "helper", "python", "bash"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			switch kind {
			case "helper":
				if err := os.WriteFile(filepath.Join(f.request.HelperDir, "added.py"), []byte("# tool changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "python", "bash":
				if err := f.service.Store.Update(t.Context(), prepared.Intent.ID, func(i *Intent) error {
					if kind == "python" {
						i.CoCommit.PythonToken = strings.Repeat("0", 64)
					} else {
						i.CoCommit.BashToken = strings.Repeat("0", 64)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			default:
				f.mutateState(func(s map[string]any) {
					o := s["observation"].(map[string]any)
					switch kind {
					case "request revision":
						o["request_revision"] = strings.Repeat("e", 64)
					case "selector":
						o["specstory_path"] = ".specstory/history/alias.md"
					case "plan policy":
						o["plan_policy"] = "path"
					case "provider":
						o["provider"] = "codex"
					case "head":
						o["head_oid"] = strings.Repeat("0", 40)
					}
				})
			}
			if _, err := f.finalize(prepared); err == nil {
				t.Fatal("changed authority finalized")
			}
			if f.readState()["finalize_count"] != nil {
				t.Fatal("stale authority reached helper finalization")
			}
		})
	}
}

func TestCoCommitGuardAndRevisionRefuseBeforeHelperMutation(t *testing.T) {
	for _, kind := range []string{"guard refusal", "guard CAS drift", "reviewed stale revision", "missing guard", "missing approval", "writer unknown"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			request := CoCommitFinalizeRequest{IntentID: prepared.Intent.ID, Revision: prepared.Revision, AllowCommit: true, Guard: func(context.Context, string, []string) error { return nil }}
			update := func() error {
				return f.service.Store.Update(t.Context(), prepared.Intent.ID, func(i *Intent) error { i.FailureCode = "concurrent_observation"; return nil })
			}
			switch kind {
			case "guard refusal":
				request.Guard = func(context.Context, string, []string) error { return errors.New("observed writer") }
			case "guard CAS drift":
				request.Guard = func(context.Context, string, []string) error { return update() }
			case "reviewed stale revision":
				if err := update(); err != nil {
					t.Fatal(err)
				}
			case "missing guard":
				request.Guard = nil
			case "missing approval":
				request.AllowCommit = false
			case "writer unknown":
				f.mutateState(func(s map[string]any) { s["observation"].(map[string]any)["group_quiescent"] = nil })
			}
			_, err := f.service.FinalizeCoCommit(t.Context(), request)
			if err == nil {
				t.Fatal("unproven mutation accepted")
			}
			if strings.Contains(kind, "revision") || strings.Contains(kind, "CAS") {
				if !errors.Is(err, ErrStaleRevision) {
					t.Fatalf("stale failure=%v", err)
				}
			}
			if state := f.readState(); state["finalize_count"] != nil || state["prepare_count"] != nil {
				t.Fatal("guard/revision refusal still invoked mutating helper")
			}
		})
	}
}

func TestCoCommitHelperOutputIsBoundedAndSanitized(t *testing.T) {
	for _, mode := range []string{"invalid_json", "multiple_json", "overflow", "sensitive_failure", "sensitive_json_failure", "known_status_sensitive_action", "old_protocol"} {
		t.Run(mode, func(t *testing.T) {
			f := newCoCommitFixture(t)
			f.mutateState(func(s map[string]any) { s["modes"] = map[string]any{"capabilities": mode} })
			result, err := f.service.PrepareCoCommit(t.Context(), f.request)
			if err == nil {
				t.Fatal("invalid helper response accepted")
			}
			data, _ := json.Marshal(result)
			if strings.Contains(strings.ToLower(err.Error()+string(data)), "private_test_secret") {
				t.Fatal("sensitive helper diagnostics escaped")
			}
			if f.readState()["queue_count"] != nil {
				t.Fatal("invalid capability proof queued a request")
			}
		})
	}
}

func TestCoCommitVerifiesFullCommittedObject(t *testing.T) {
	for _, fault := range []string{"", "parent", "tree", "message", "nonce", "duplicate_nonce"} {
		name := fault
		if name == "" {
			name = "valid complete proof"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			f.mutateState(func(s map[string]any) { s["commit_fault"] = fault })
			result, err := f.finalize(prepared)
			if fault == "" {
				if err != nil || result.Status != "committed" || result.Intent.Status != Finalized {
					t.Fatalf("valid proof=%+v %v", result, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s mismatch became native finalized proof", fault)
			}
			current, getErr := f.service.Store.Get(prepared.Intent.ID)
			if getErr != nil || current.Status == Finalized {
				t.Fatalf("invalid proof persisted: %+v %v", current, getErr)
			}
		})
	}
}

func TestCoCommitReconcilesCommitAfterNativePersistenceFailureWithoutRepeat(t *testing.T) {
	f := newCoCommitFixture(t)
	prepared := f.prepare()
	f.mutateState(func(s map[string]any) {
		s["native_write_drift"] = true
		s["native_intent_path"] = f.service.Store.path(prepared.Intent.ID)
	})
	partial, err := f.finalize(prepared)
	requireCoCommitCode(t, err, "committed_but_not_recorded")
	if partial == nil || partial.Status != "committed_but_not_recorded" {
		t.Fatalf("lost committed partial result: %+v", partial)
	}
	head := f.repo.Git("rev-parse", "HEAD")
	repaired, err := f.finalize(prepared)
	if err != nil || repaired.Intent.Status != Finalized || repaired.Intent.ArtifactCommit != head {
		t.Fatalf("receipt reconciliation=%+v %v", repaired, err)
	}
	if f.readState()["finalize_count"] != float64(1) || f.repo.Git("rev-parse", "HEAD") != head {
		t.Fatal("reconciliation re-invoked committing helper")
	}
}

func TestCoCommitExplicitPlanAndTranscriptSelection(t *testing.T) {
	f := newCoCommitFixture(t)
	alias := filepath.Join(f.repo.Root, ".specstory/history/alias.md")
	writeTranscript(t, alias, "Claude Code", coCommitTestSession, "unselected same-UUID alias")
	f.repo.Write(".claude/plans/selected.md", "# reviewed plan\n")
	f.repo.Write(".claude/plans/unrelated.md", "# unrelated plan\n")
	f.request.Plan = ".claude/plans/selected.md"
	f.request.NoPlan = false
	prepared := f.prepare()
	if prepared.Intent.CoCommit.PlanPolicy != "path" || len(prepared.Intent.PlanPaths) != 1 {
		t.Fatalf("lost explicit plan: %+v", prepared.Intent)
	}
	if _, err := f.finalize(prepared); err != nil {
		t.Fatal(err)
	}
	paths := f.repo.Git("show", "--pretty=", "--name-only", "HEAD")
	for _, wanted := range []string{"feature.go", ".specstory/history/exact.md", ".claude/plans/selected.md"} {
		if !strings.Contains(paths, wanted) {
			t.Fatalf("missing selected path: %s", paths)
		}
	}
	if strings.Contains(paths, "alias.md") || strings.Contains(paths, "unrelated.md") {
		t.Fatalf("unselected artifact committed: %s", paths)
	}
}

func TestCoCommitRejectsInitialArchivePolicy(t *testing.T) {
	f := newCoCommitFixture(t)
	archive := gittest.New(t)
	h, err := agenthistory.Open(t.Context(), agenthistory.Options{Root: f.repo.Root, StateDir: filepath.Dir(filepath.Dir(f.service.Store.Dir))})
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.PreviewSetup(t.Context(), agenthistory.SetupOptions{Mode: "archive", Source: "specstory", Archive: archive.Root, Protection: "off", ExportIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.ApplySetup(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.service.PrepareCoCommit(t.Context(), f.request)
	requireCoCommitCode(t, err, "incompatible_archive_policy")
	if f.readState()["queue_count"] != nil {
		t.Fatal("archive policy queued a tracked commit")
	}
}

func TestCoCommitRechecksGuardAfterPreparation(t *testing.T) {
	f := newCoCommitFixture(t)
	prepared := f.prepare()
	head := f.repo.Git("rev-parse", "HEAD")
	guards := 0
	_, err := f.service.FinalizeCoCommit(t.Context(), CoCommitFinalizeRequest{IntentID: prepared.Intent.ID, AllowCommit: true, Guard: func(context.Context, string, []string) error {
		guards++
		if guards > 1 {
			return errors.New("writer appeared after preparation")
		}
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "writer appeared") {
		t.Fatalf("late writer guard=%v", err)
	}
	state := f.readState()
	if state["prepare_count"] != float64(1) || state["finalize_count"] != nil || f.repo.Git("rev-parse", "HEAD") != head {
		t.Fatalf("late guard did not stop commit: %+v", state)
	}
}

func TestCoCommitReviewProjectionNeverReturnsRawFindingFields(t *testing.T) {
	for _, rawMask := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown fields omitted", true: "unsafe mask rejected"}[rawMask], func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			f.mutateState(func(s map[string]any) {
				o := s["observation"].(map[string]any)
				o["prepared_tree"] = o["input_index_tree"]
				o["receipt_revision"] = strings.Repeat("b", 64)
				if rawMask {
					s["review_fault"] = "raw_mask"
				}
			})
			preview, err := f.service.PreviewCoCommitReview(t.Context(), prepared.Intent.ID)
			if rawMask && err == nil {
				t.Fatal("raw value in mask accepted")
			}
			if !rawMask && err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(preview)
			text := string(encoded)
			if err != nil {
				text += err.Error()
			}
			if strings.Contains(text, "PRIVATE_TEST_SECRET") {
				t.Fatal("raw helper field escaped native review projection")
			}
		})
	}
}

func TestCoCommitInstalledToolIdentityIsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tool contract")
	}
	for _, name := range []string{"python3", "bash"} {
		t.Run(name, func(t *testing.T) {
			lookup := name
			if name == "bash" && runtime.GOOS == "darwin" {
				lookup = "/bin/bash"
			}
			installed, err := exec.LookPath(lookup)
			if err != nil {
				t.Skip(err)
			}
			installed, err = filepath.EvalSymlinks(installed)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(installed)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(installed)
			if err != nil {
				t.Fatal(err)
			}
			path, token, err := coCommitTool(t.Context(), name)
			// setup-python/toolcache distributions may intentionally be group-
			// or world-writable. Installation alone is not trust authority.
			if before.Mode().Perm()&0o022 != 0 {
				var rejected *CoCommitError
				if !errors.As(err, &rejected) || rejected.Code != "unverified_tool" || path != "" || token != "" {
					t.Fatalf("writable installed tool was not rejected: %v", err)
				}
			} else if err != nil || path != installed || !coCommitDigest.MatchString(token) {
				t.Fatalf("trusted installed tool identity failed: %v", err)
			}
			assertCoCommitToolUnchanged(t, installed, before, data)
		})
	}
}

func TestCoCommitExecutablePermissionContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tool contract")
	}
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		trusted bool
	}{
		{"owner-only", 0o700, true},
		{"readable-executable", 0o755, true},
		{"group-writable", 0o775, false},
		{"world-writable", 0o777, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "fixture-tool")
			data := []byte("#!/bin/sh\nexit 0\n")
			if err := os.WriteFile(path, data, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			resolved, token, err := coCommitTool(t.Context(), path)
			if tc.trusted {
				if err != nil || resolved != path || !coCommitDigest.MatchString(token) {
					t.Fatalf("safe fixture rejected: %v", err)
				}
			} else {
				var rejected *CoCommitError
				if !errors.As(err, &rejected) || rejected.Code != "unverified_tool" || resolved != "" || token != "" {
					t.Fatalf("writable fixture was not rejected: %v", err)
				}
			}
			assertCoCommitToolUnchanged(t, path, before, data)
		})
	}
}

func assertCoCommitToolUnchanged(t *testing.T, path string, before os.FileInfo, data []byte) {
	t.Helper()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(data, current) {
		t.Fatal("tool identity observation mutated the executable")
	}
}

func TestCoCommitRejectsArtifactPolicyDriftBeforeNewCommit(t *testing.T) {
	f := newCoCommitFixture(t)
	prepared := f.prepare()
	f.repo.Write(".dev-cli/artifacts.toml", "# policy appeared after reviewed preparation\n")
	if _, err := f.finalize(prepared); err == nil {
		t.Fatal("new artifact policy did not invalidate commit authority")
	}
	if f.readState()["finalize_count"] != nil {
		t.Fatal("changed policy reached committing helper")
	}
}

func TestCoCommitCompletedProofRetainsHistoricalPolicyLane(t *testing.T) {
	f := newCoCommitFixture(t)
	prepared := f.prepare()
	committed, err := f.finalize(prepared)
	if err != nil {
		t.Fatal(err)
	}
	f.repo.Write(".dev-cli/artifacts.toml", "# later artifact policy does not rewrite historical proof\n")
	observation, err := ObserveCoCommit(t.Context(), committed.Intent)
	if err != nil || !observation.CommitProven {
		t.Fatalf("historical proof was reinterpreted: %+v %v", observation, err)
	}
}

func TestCoCommitRevalidatesToolLookupAndGitEnvironment(t *testing.T) {
	for _, kind := range []string{"bash lookup", "python lookup", "git config parameters"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			if kind == "bash lookup" || kind == "python lookup" {
				tools, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				name := "bash"
				if kind == "python lookup" {
					name = "python3"
				}
				if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			} else {
				t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath=/unreviewed'")
			}
			if runtime.GOOS == "darwin" && kind == "bash lookup" {
				// Darwin deliberately executes the supported system Bash, not an
				// arbitrary PATH Bash. Its exact identity must remain unchanged.
				binding := prepared.Intent.CoCommit
				path, token, err := coCommitTool(t.Context(), "bash")
				if err != nil || binding.Bash != "/bin/bash" || path != binding.Bash || token != binding.BashToken {
					t.Fatalf("unused PATH Bash changed system binding: path=%q err=%v", path, err)
				}
				result, err := f.finalize(prepared)
				if err != nil || result.Intent.Status != Finalized || f.readState()["finalize_count"] != float64(1) {
					t.Fatalf("unchanged pinned system Bash could not finalize: %+v %v", result, err)
				}
				return
			}
			if _, err := f.finalize(prepared); err == nil {
				t.Fatal("execution context drift was ignored")
			}
			if f.readState()["finalize_count"] != nil {
				t.Fatal("unbound context reached committing helper")
			}
		})
	}
}

func TestCoCommitRequiresManualWrapperBeforeQueue(t *testing.T) {
	for _, automatic := range []any{nil, true} {
		name := "automatic"
		if automatic == nil {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoCommitFixture(t)
			f.mutateState(func(s map[string]any) { s["observation"].(map[string]any)["automatic_commit"] = automatic })
			_, err := f.service.PrepareCoCommit(t.Context(), f.request)
			requireCoCommitCode(t, err, "manual_wrapper_required")
			if f.readState()["queue_count"] != nil {
				t.Fatal("automatic or unproven wrapper queued native handoff")
			}
		})
	}
}

func TestCoCommitUnknownCommitUsesReconcileOnly(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no commit proof", true: "commit found"}[committed], func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			if committed {
				f.mutateState(func(s map[string]any) { s["uncertain_commit"] = true })
				if _, err := f.finalize(prepared); err == nil {
					t.Fatal("unknown helper commit outcome became success")
				}
			} else {
				f.mutateState(func(s map[string]any) { s["observation"].(map[string]any)["state"] = "committing" })
			}
			head := f.repo.Git("rev-parse", "HEAD")
			result, err := f.service.FinalizeCoCommit(t.Context(), CoCommitFinalizeRequest{IntentID: prepared.Intent.ID, AllowCommit: true})
			if committed {
				if err != nil || result.Intent.Status != Finalized {
					t.Fatalf("reconciliation=%+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("missing commit was silently created or proven")
			}
			state := f.readState()
			if state["reconcile_count"] != float64(1) || f.repo.Git("rev-parse", "HEAD") != head {
				t.Fatalf("reconciliation changed Git: %+v", state)
			}
			if committed {
				if state["finalize_count"] != float64(1) {
					t.Fatal("uncertain committed run was committed twice")
				}
			} else if state["finalize_count"] != nil || state["prepare_count"] != nil {
				t.Fatal("uncertain uncommitted run was retried")
			}
		})
	}
}

func TestCoCommitLateAuthorityDriftRefusesCommit(t *testing.T) {
	for _, journal := range []bool{false, true} {
		t.Run(map[bool]string{false: "native revision", true: "helper journal revision"}[journal], func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			calls := 0
			_, err := f.service.FinalizeCoCommit(t.Context(), CoCommitFinalizeRequest{IntentID: prepared.Intent.ID, AllowCommit: true, Guard: func(context.Context, string, []string) error {
				calls++
				if calls != 2 {
					return nil
				}
				if journal {
					f.mutateState(func(s map[string]any) {
						s["observation"].(map[string]any)["journal_revision"] = strings.Repeat("f", 64)
					})
					return nil
				}
				return f.service.Store.Update(t.Context(), prepared.Intent.ID, func(i *Intent) error { i.FailureCode = "concurrent_observation"; return nil })
			}})
			if err == nil {
				t.Fatal("late authority drift committed")
			}
			if !journal && !errors.Is(err, ErrStaleRevision) {
				t.Fatalf("native late CAS=%v", err)
			}
			state := f.readState()
			if state["prepare_count"] != float64(1) || state["finalize_count"] != nil {
				t.Fatalf("stale late authority reached commit: %+v", state)
			}
		})
	}
}

func TestCoCommitObservationProjectionRejectsSensitiveDTOs(t *testing.T) {
	for _, field := range []string{"status", "state", "sync_status", "request_id"} {
		t.Run(field, func(t *testing.T) {
			f := newCoCommitFixture(t)
			prepared := f.prepare()
			f.mutateState(func(s map[string]any) {
				s["observation_override"] = map[string]any{field: "private_test_secret", "capabilities": []string{"private_test_secret"}}
			})
			observation, err := ObserveCoCommit(t.Context(), prepared.Intent)
			if field != "status" && err == nil {
				t.Fatal("invalid observation enum/identity accepted")
			}
			if field == "status" && err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(observation)
			text := string(encoded)
			if err != nil {
				text += err.Error()
			}
			if strings.Contains(text, "private_test_secret") {
				t.Fatal("rejected or unprojected DTO leaked sensitive fields")
			}
		})
	}
}

func coCommitResultAck(t *testing.T, result *CoCommitResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var projected struct {
		QueueAck map[string]any `json:"queue_ack"`
	}
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatal(err)
	}
	return projected.QueueAck
}

func requireCoCommitQueueAck(t *testing.T, f *coCommitFixture, result *CoCommitResult) {
	t.Helper()
	ack := coCommitResultAck(t, result)
	observation := f.readState()["observation"].(map[string]any)
	want := map[string]any{
		"kind":             "agent_history_queue_ack",
		"schema_version":   float64(2),
		"request_id":       coCommitTestRun,
		"request_path":     observation["request_path"],
		"request_revision": strings.Repeat("a", 64),
		"helper_revision":  strings.Repeat("c", 64),
	}
	if len(ack) != len(want) {
		t.Fatalf("queue ACK schema=%+v; want exactly six fields", ack)
	}
	for key, value := range want {
		if ack[key] != value {
			t.Fatalf("queue ACK %s=%v want %v", key, ack[key], value)
		}
	}
}

func TestCoCommitRejectsMissingOrMismatchedQueueAck(t *testing.T) {
	for _, fault := range []string{"missing", "empty", "kind", "schema_version", "request_id", "request_path", "relative_path", "request_revision", "helper_revision"} {
		t.Run(fault, func(t *testing.T) {
			f := newCoCommitFixture(t)
			f.mutateState(func(s map[string]any) { s["ack_fault"] = fault })
			index, head := f.repo.Git("write-tree"), f.repo.Git("rev-parse", "HEAD")
			result, err := f.service.PrepareCoCommit(t.Context(), f.request)
			if err == nil {
				t.Fatalf("%s queue ACK accepted: %+v", fault, result)
			}
			if result != nil && result.Intent != nil {
				t.Fatalf("invalid ACK created native binding: %+v", result.Intent)
			}
			if coCommitResultAck(t, result) != nil {
				t.Fatal("invalid or mismatched ACK was exposed as queue evidence")
			}
			if records, e := f.service.Store.List(); e != nil || len(records) != 0 {
				t.Fatalf("ACK refusal wrote native intent: %+v %v", records, e)
			}
			if f.readState()["queue_count"] != float64(1) || f.repo.Git("write-tree") != index || f.repo.Git("rev-parse", "HEAD") != head {
				t.Fatal("ACK failure retried queue or changed source Git")
			}
			if fault == "missing" {
				if coCommitResultAck(t, result) != nil {
					t.Fatal("missing queue ACK was synthesized")
				}
				// A lost ACK is not permission to queue twice. The explicit old run
				// can repair its native binding but cannot invent the missing event.
				f.request.RunID = coCommitTestRun
				bound, e := f.service.PrepareCoCommit(t.Context(), f.request)
				if e != nil || bound.Status != "bound" || coCommitResultAck(t, bound) != nil || f.readState()["queue_count"] != float64(1) {
					t.Fatalf("missing-ACK repair=%+v %v", bound, e)
				}
			}
		})
	}
}
