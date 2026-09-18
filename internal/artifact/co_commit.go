package artifact

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// CoCommitPrepareRequest deliberately does not relax ordinary Prepare. Only a
// compatible canonical wrapper may queue this feature-plus-artifact snapshot.
type CoCommitPrepareRequest struct {
	Worktree      string
	TaskID        string
	Base          string
	Session       string
	SpecStoryPath string
	Plan          string
	NoPlan        bool
	MessageFile   string
	HelperDir     string
	RunID         string
	AllowLarge    bool
}

type CoCommitFinalizeRequest struct {
	IntentID          string
	Revision          string
	AllowCommit       bool
	ReviewFile        string
	RotationConfirmed bool
	Guard             func(context.Context, string, []string) error
}

// CoCommitQueueAck is the canonical queue's typed acknowledgement. The real
// recorder must retain it as a tool result; dev never fabricates source evidence.
type CoCommitQueueAck struct {
	Kind            string `json:"kind"`
	SchemaVersion   int    `json:"schema_version"`
	RequestID       string `json:"request_id"`
	RequestPath     string `json:"request_path"`
	RequestRevision string `json:"request_revision"`
	HelperRevision  string `json:"helper_revision"`
}

func (a *CoCommitQueueAck) valid(id, path, helperRevision string) bool {
	return a != nil && a.Kind == "agent_history_queue_ack" && a.SchemaVersion == 2 && a.RequestID == id && a.RequestPath == path && a.HelperRevision == helperRevision && coCommitDigest.MatchString(a.RequestRevision)
}

// CoCommitResult preserves partial queue/binding and reconciliation outcomes.
// A non-nil result accompanied by an error may describe retained helper effects.
type CoCommitResult struct {
	Kind          string               `json:"kind"`
	SchemaVersion int                  `json:"schema_version"`
	Status        string               `json:"status"`
	Intent        *Intent              `json:"intent,omitempty"`
	Revision      string               `json:"revision,omitempty"`
	RequestID     string               `json:"request_id,omitempty"`
	NextAction    string               `json:"next_action"`
	Observation   *CoCommitObservation `json:"observation,omitempty"`
	QueueAck      *CoCommitQueueAck    `json:"queue_ack,omitempty"`
}

func newCoCommitResult(status, requestID, next string) *CoCommitResult {
	return &CoCommitResult{Kind: "co_commit_handoff", SchemaVersion: 1, Status: status, RequestID: requestID, NextAction: next}
}

func coCommitGitDir(ctx context.Context, root string) (string, error) {
	path, err := gitx.RunReadOnly(ctx, root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", coCommitFailure("git_identity_unavailable", "inspect_checkout")
	}
	return pathx.Canonical(path)
}

func coCommitRequestPath(gitDir, id string) string {
	return filepath.Join(gitDir, "agent-history-hygiene", "runs", id, "request.json")
}

func coCommitRelative(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || strings.ContainsAny(relative, "\x00\r\n") {
		return "", coCommitFailure("unsafe_selection", "select_exact_checkout_paths")
	}
	return filepath.ToSlash(relative), nil
}

func coCommitMessage(ctx context.Context, root, path string) (string, string, error) {
	if path == "" {
		return "", "", coCommitFailure("message_required", "provide_base_message_file")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parent != filepath.Dir(path) || filepath.Clean(path) != path {
		return "", "", coCommitFailure("unsafe_message_path", "provide_regular_base_message_file")
	}
	data, err := safefile.ReadStablePath(ctx, path, 256<<10)
	if err != nil || len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data) {
		return "", "", coCommitFailure("invalid_message", "provide_utf8_base_message")
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, _, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "ai-assisted-by", "agent-transcript", "agent-plan", "agent-history-request":
			return "", "", coCommitFailure("managed_message_trailer", "let_canonical_finalizer_derive_provenance")
		}
	}
	digest := sha256.Sum256(data)
	return path, hex.EncodeToString(digest[:]), nil
}

func coCommitFeatureIndex(ctx context.Context, root string) (string, error) {
	paths, err := gitx.RunReadOnly(ctx, root, "diff", "--cached", "--name-only", "-z")
	if err != nil || paths == "" {
		return "", coCommitFailure("feature_index_required", "stage_only_reviewed_feature_paths")
	}
	for _, path := range strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00") {
		if isRecognizedArtifact(path) {
			return "", coCommitFailure("artifact_already_staged", "preserve_index_then_prepare_feature_only_selection")
		}
	}
	unmerged, err := gitx.RunReadOnly(ctx, root, "ls-files", "--unmerged", "-z")
	if err != nil || unmerged != "" {
		return "", coCommitFailure("unmerged_index", "resolve_git_operation")
	}
	entries, err := gitx.RunReadOnly(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return "", coCommitFailure("index_unavailable", "inspect_checkout")
	}
	digest := sha256.Sum256([]byte(entries))
	return hex.EncodeToString(digest[:]), nil
}

func verifyCoCommitRequestLocation(observation CoCommitObservation, root, gitDir string) error {
	if observation.WorktreeRoot != root || observation.GitDir != gitDir || observation.RequestID != observation.RunID || !canonicalSessionID(observation.RequestID) || observation.RequestPath != coCommitRequestPath(gitDir, observation.RequestID) {
		return coCommitFailure("request_identity_mismatch", "inspect_existing_request_do_not_retry")
	}
	if !coCommitDigest.MatchString(observation.RequestRevision) || !coCommitDigest.MatchString(observation.MessageSHA256) || !coCommitOID.MatchString(observation.HeadOID) || !coCommitOID.MatchString(observation.InputIndexTree) || !strings.HasPrefix(observation.HeadRef, "refs/heads/") || strings.ContainsAny(observation.HeadRef, "\x00\r\n") {
		return coCommitFailure("request_identity_incomplete", "inspect_existing_request_do_not_retry")
	}
	return nil
}

// PrepareCoCommit queues once in a real wrapper context, or repairs only the
// binding of an explicitly selected already-queued run. No source/index writes
// are performed by this adapter; the canonical queue owns its inert request.
func (s *Service) PrepareCoCommit(ctx context.Context, request CoCommitPrepareRequest) (*CoCommitResult, error) {
	if s.Store == nil {
		return nil, errors.New("artifact service needs a store")
	}
	if _, err := coCommitEnvironment(); err != nil {
		return nil, err
	}
	provider, sessionID, err := ParseSession(request.Session)
	if err != nil || provider != "claude" || !canonicalSessionID(sessionID) {
		return nil, coCommitFailure("unsupported_session", "select_exact_supported_provider_session")
	}
	if request.SpecStoryPath == "" || (request.NoPlan && request.Plan != "") || (!request.NoPlan && request.Plan == "") {
		return nil, coCommitFailure("explicit_selection_required", "select_transcript_and_one_plan_or_no_plan")
	}
	repository, err := gitx.Discover(ctx, request.Worktree)
	if err != nil {
		return nil, coCommitFailure("git_identity_unavailable", "inspect_checkout")
	}
	root, err := pathx.Canonical(repository.Root)
	if err != nil {
		return nil, err
	}
	if operation, active, err := gitx.InProgress(root); err != nil || active {
		_ = operation
		return nil, coCommitFailure("git_operation_active", "finish_git_operation")
	}
	if _, err := os.Lstat(filepath.Join(root, ".dev-cli", "artifacts.toml")); !errors.Is(err, os.ErrNotExist) {
		history, openErr := agenthistory.Open(ctx, agenthistory.Options{Root: root, StateDir: filepath.Dir(filepath.Dir(s.Store.Dir))})
		if openErr != nil {
			return nil, coCommitFailure("artifact_policy_unavailable", "inspect_artifact_policy")
		}
		if history.Policy.Mode == "archive" {
			return nil, coCommitFailure("incompatible_archive_policy", "use_archive_handoff")
		}
	}
	policyToken, err := configedit.InspectToken(ctx, filepath.Join(root, ".dev-cli", "artifacts.toml"))
	if err != nil {
		return nil, coCommitFailure("artifact_policy_unavailable", "inspect_artifact_policy")
	}
	transcript, err := SelectTranscript(root, request.SpecStoryPath, provider, sessionID)
	if err != nil {
		return nil, coCommitFailure("transcript_selection_invalid", "review_exact_transcript_selection")
	}
	info, err := os.Lstat(transcript.Path)
	if err != nil {
		return nil, err
	}
	tracked, err := gitTracked(ctx, root, transcript.Path)
	if err != nil {
		return nil, err
	}
	limit := s.LargeLimit
	if limit <= 0 {
		limit = DefaultLargeLimit
	}
	if !tracked && info.Size() > limit && !request.AllowLarge {
		return nil, coCommitFailure("large_transcript_review_required", "review_growth_then_use_allow_large")
	}
	transcriptRelative, err := coCommitRelative(root, transcript.Path)
	if err != nil {
		return nil, err
	}
	var plans []string
	planRelative, planPolicy := "", "none"
	if !request.NoPlan {
		plans, err = normalizePlans(root, []string{request.Plan})
		if err != nil {
			return nil, coCommitFailure("plan_selection_invalid", "review_exact_plan_selection")
		}
		planRelative, err = coCommitRelative(root, plans[0])
		if err != nil {
			return nil, err
		}
		planPolicy = "path"
	}
	messagePath, messageDigest, err := coCommitMessage(ctx, root, request.MessageFile)
	if err != nil {
		return nil, err
	}
	gitDir, err := coCommitGitDir(ctx, root)
	if err != nil {
		return nil, err
	}
	helper, err := resolveCoCommitHelper(ctx, root, request.HelperDir)
	if err != nil {
		return nil, err
	}
	var observation CoCommitObservation
	queued := false
	var queueAck *CoCommitQueueAck
	queuedID := request.RunID
	if request.RunID != "" {
		if !canonicalSessionID(request.RunID) {
			return nil, coCommitFailure("invalid_run_selection", "select_existing_canonical_run")
		}
		observation, err = helper.inspect(ctx, coCommitRequestPath(gitDir, request.RunID), false)
	} else {
		if os.Getenv("AGENT_HISTORY_REQUEST_PATH") == "" || !canonicalSessionID(os.Getenv("AGENT_HISTORY_RUN_ID")) {
			return nil, coCommitFailure("requires_wrapper", "start_real_wrapper_with_explicit_approval")
		}
		observation, err = helper.inspect(ctx, "", true)
		if err == nil && observation.RequestID == "" {
			if observation.AutomaticCommit == nil || *observation.AutomaticCommit {
				return nil, coCommitFailure("manual_wrapper_required", "start_wrapper_without_automatic_commit")
			}
			if observation.GitDir != gitDir || observation.RequestPath != coCommitRequestPath(gitDir, observation.RunID) || observation.State != "running" {
				return nil, coCommitFailure("wrapper_context_mismatch", "inspect_real_wrapper_context")
			}
			beforeIndex, indexErr := coCommitFeatureIndex(ctx, root)
			if indexErr != nil {
				return nil, indexErr
			}
			args := []string{"--session-id", sessionID, "--specstory-path", transcriptRelative, "--message-file", messagePath}
			if request.NoPlan {
				args = append(args, "--no-plan")
			} else {
				args = append(args, "--plan", planRelative)
			}
			var reply struct {
				Status   string            `json:"status"`
				QueueAck *CoCommitQueueAck `json:"queue_ack"`
			}
			if queueErr := helper.call(ctx, "queue", args, &reply); queueErr != nil {
				return newCoCommitResult("queue_outcome_unproven", observation.RunID, "inspect_existing_request_do_not_requeue"), queueErr
			}
			queued = true
			queuedID = observation.RunID
			if reply.Status != "queued" || !reply.QueueAck.valid(observation.RunID, observation.RequestPath, helper.binding.HelperRevision) {
				return newCoCommitResult("queue_outcome_unproven", observation.RunID, "inspect_existing_request_do_not_requeue"), coCommitFailure("queue_outcome_unproven", "inspect_existing_request_do_not_requeue")
			}
			queueAck = reply.QueueAck
			requestPath := observation.RequestPath
			observation, err = helper.inspect(ctx, requestPath, false)
			if err == nil {
				afterIndex, indexErr := coCommitFeatureIndex(ctx, root)
				if indexErr != nil || beforeIndex != afterIndex {
					err = coCommitFailure("index_changed_during_queue", "preserve_request_and_exit_agent")
				}
			}
		}
	}
	if queuedID == "" {
		queuedID = observation.RunID
	}
	partial := newCoCommitResult("queued_but_not_bound", queuedID, "exit_agent_then_repair_exact_binding")
	partial.QueueAck = queueAck
	if err != nil {
		if queued {
			return partial, err
		}
		return nil, err
	}
	if err := verifyCoCommitRequestLocation(observation, root, gitDir); err != nil {
		return partial, err
	}
	if !observation.CommitProven && (observation.AutomaticCommit == nil || *observation.AutomaticCommit) {
		return partial, coCommitFailure("manual_wrapper_required", "start_wrapper_without_automatic_commit")
	}
	if queueAck != nil && queueAck.RequestRevision != observation.RequestRevision {
		partial.QueueAck = nil
		return partial, coCommitFailure("queue_ack_mismatch", "inspect_existing_request_do_not_requeue")
	}
	if observation.Provider != provider || observation.SessionID != sessionID || observation.SpecStoryPath != transcriptRelative || observation.PlanPolicy != planPolicy || observation.PlanPath != planRelative || observation.MessageSHA256 != messageDigest {
		return partial, coCommitFailure("queued_selection_mismatch", "inspect_existing_request_do_not_requeue")
	}
	binding := helper.binding
	binding.RequestPath, binding.RequestID, binding.RequestRevision = observation.RequestPath, observation.RequestID, observation.RequestRevision
	binding.InputIndexTree, binding.MessageSHA256, binding.PlanPolicy = observation.InputIndexTree, observation.MessageSHA256, observation.PlanPolicy
	binding.ArtifactPolicyToken = policyToken
	intent := &Intent{
		ID: "co-" + observation.RequestID, RunID: observation.RunID, Provider: provider, SessionID: sessionID,
		TaskID: request.TaskID, RepoPath: repository.MainRoot, GitCommonDir: repository.GitCommonDir,
		WorktreePath: root, Branch: strings.TrimPrefix(observation.HeadRef, "refs/heads/"), Base: request.Base,
		Head: observation.HeadOID, PlanPaths: plans, SpecStoryPath: transcript.Path, AllowLarge: request.AllowLarge,
		Destination: "co-commit", CoCommit: &binding,
	}
	common, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return partial, err
	}
	var record *Record
	err = lockx.WithDir(ctx, filepath.Join(common, "dev-artifact-bind"), "co-commit binding", func() error {
		existing, err := s.Store.GetRecord(intent.ID)
		if err == nil {
			if err := coCommitBindingMatches(existing.Intent, observation); err != nil {
				return coCommitFailure("binding_conflict", "inspect_existing_native_binding")
			}
			old := existing.Intent.CoCommit
			if old.HelperDir != binding.HelperDir || old.HelperToken != binding.HelperToken || old.Python != binding.Python || old.PythonToken != binding.PythonToken || old.Bash != binding.Bash || old.BashToken != binding.BashToken {
				return coCommitFailure("binding_conflict", "inspect_existing_native_binding")
			}
			if _, err := boundCoCommitHelper(ctx, existing.Intent); err != nil {
				return err
			}
			record = existing
			return nil
		}
		if !errors.Is(err, ErrIntentNotFound) {
			return err
		}
		if err := coCommitCurrentRequest(ctx, intent, observation); err != nil {
			return err
		}
		if err := s.Store.Create(ctx, intent); err != nil {
			return err
		}
		record, err = s.Store.GetRecord(intent.ID)
		return err
	})
	if err != nil {
		return partial, &CoCommitError{Code: "queued_but_not_bound", RequestID: observation.RequestID, NextAction: "exit_agent_then_repair_exact_binding", MayHaveRun: true}
	}
	result := newCoCommitResult("queued", observation.RequestID, "report_finalization_queued_then_exit_agent")
	if request.RunID != "" {
		result.Status, result.NextAction = "bound", "inspect_helper_status_before_finalization"
	}
	result.Intent, result.Revision, result.Observation = record.Intent, record.Revision, &observation
	result.QueueAck = queueAck
	return result, nil
}

func coCommitBindingMatches(intent *Intent, observation CoCommitObservation) error {
	if intent == nil || intent.Destination != "co-commit" || intent.CoCommit == nil || intent.CoCommit.validate() != nil {
		return coCommitFailure("invalid_binding", "inspect_native_binding")
	}
	b := intent.CoCommit
	if observation.HelperRevision != b.HelperRevision || observation.RequestID != b.RequestID || observation.RequestPath != b.RequestPath || observation.RequestRevision != b.RequestRevision || observation.WorktreeRoot != intent.WorktreePath || observation.HeadRef != "refs/heads/"+intent.Branch || observation.HeadOID != intent.Head || observation.InputIndexTree != b.InputIndexTree || observation.Provider != intent.Provider || observation.SessionID != intent.SessionID || observation.MessageSHA256 != b.MessageSHA256 || observation.PlanPolicy != b.PlanPolicy {
		return coCommitFailure("request_changed", "inspect_existing_request_do_not_retry")
	}
	transcript, err := coCommitRelative(intent.WorktreePath, intent.SpecStoryPath)
	if err != nil || observation.SpecStoryPath != transcript {
		return coCommitFailure("selector_changed", "inspect_existing_request_do_not_retry")
	}
	plan := ""
	if b.PlanPolicy == "path" {
		if len(intent.PlanPaths) != 1 {
			return coCommitFailure("invalid_plan_binding", "inspect_native_binding")
		}
		plan, err = coCommitRelative(intent.WorktreePath, intent.PlanPaths[0])
		if err != nil {
			return err
		}
	} else if len(intent.PlanPaths) != 0 {
		return coCommitFailure("invalid_plan_binding", "inspect_native_binding")
	}
	if observation.PlanPath != plan {
		return coCommitFailure("plan_changed", "inspect_existing_request_do_not_retry")
	}
	return nil
}

func coCommitCurrentRequest(ctx context.Context, intent *Intent, observation CoCommitObservation) error {
	if err := coCommitBindingMatches(intent, observation); err != nil {
		return err
	}
	gitDir, err := coCommitGitDir(ctx, intent.WorktreePath)
	if err != nil {
		return err
	}
	if err := verifyCoCommitRequestLocation(observation, intent.WorktreePath, gitDir); err != nil {
		return err
	}
	if observation.CommitProven {
		return verifyCoCommitObject(ctx, intent.WorktreePath, intent, observation, true)
	}
	policyToken, err := configedit.InspectToken(ctx, filepath.Join(intent.WorktreePath, ".dev-cli", "artifacts.toml"))
	if err != nil || policyToken != intent.CoCommit.ArtifactPolicyToken {
		return coCommitFailure("artifact_policy_changed", "review_existing_handoff_policy")
	}
	// An uncertain commit may already have moved HEAD and emptied the index.
	// The only permitted operation is explicit canonical reconciliation.
	if observation.State == "committing" {
		return nil
	}
	head, err := gitx.RunReadOnly(ctx, intent.WorktreePath, "rev-parse", "HEAD")
	if err != nil || head != intent.Head {
		return coCommitFailure("head_changed", "preserve_snapshot_and_inspect")
	}
	ref, err := gitx.RunReadOnly(ctx, intent.WorktreePath, "symbolic-ref", "HEAD")
	if err != nil || ref != "refs/heads/"+intent.Branch {
		return coCommitFailure("ref_changed", "preserve_snapshot_and_inspect")
	}
	tree := observation.InputIndexTree
	if observation.PreparedTree != "" {
		if !coCommitOID.MatchString(observation.PreparedTree) {
			return coCommitFailure("invalid_prepared_tree", "inspect_existing_request_do_not_retry")
		}
		tree = observation.PreparedTree
	}
	if _, err := gitx.RunReadOnly(ctx, intent.WorktreePath, "diff", "--cached", "--quiet", tree, "--"); err != nil {
		return coCommitFailure("prepared_index_changed", "preserve_snapshot_and_inspect")
	}
	return nil
}

// ObserveCoCommit reads canonical proof and verifies the Git object independently.
// It does not repair native status, stage files, scan, sync or contact remotes.
func ObserveCoCommit(ctx context.Context, intent *Intent) (observation CoCommitObservation, err error) {
	defer func() {
		if err != nil {
			observation = CoCommitObservation{}
		}
	}()
	helper, err := boundCoCommitHelper(ctx, intent)
	if err != nil {
		return observation, err
	}
	observation, err = helper.inspect(ctx, intent.CoCommit.RequestPath, false)
	if err != nil {
		return observation, err
	}
	if err = coCommitBindingMatches(intent, observation); err != nil {
		return observation, err
	}
	gitDir, err := coCommitGitDir(ctx, intent.WorktreePath)
	if err != nil {
		return observation, err
	}
	if err = verifyCoCommitRequestLocation(observation, intent.WorktreePath, gitDir); err != nil {
		return observation, err
	}
	if observation.CommitProven {
		err = verifyCoCommitObject(ctx, intent.WorktreePath, intent, observation, false)
	}
	return observation, err
}

func coCommitPostWriter(observation CoCommitObservation) bool {
	return observation.ChildExitCode != nil && *observation.ChildExitCode == 0 && observation.ChildSignal == nil && observation.GroupQuiescent != nil && *observation.GroupQuiescent && observation.SyncStatus == "proven" && observation.FreshnessStatus == "proven"
}

func verifyCoCommitObject(ctx context.Context, root string, intent *Intent, observation CoCommitObservation, requireHead bool) error {
	if !observation.CommitProven || observation.State != "done" || !coCommitPostWriter(observation) || !coCommitOID.MatchString(observation.CommitOID) || !coCommitOID.MatchString(observation.PreparedTree) {
		return coCommitFailure("commit_unproven", "reconcile_canonical_receipt_only")
	}
	if requireHead {
		head, err := gitx.RunReadOnly(ctx, root, "rev-parse", "HEAD")
		if err != nil || head != observation.CommitOID {
			return coCommitFailure("commit_head_changed", "reconcile_canonical_receipt_only")
		}
	}
	env, err := coCommitEnvironment()
	if err != nil {
		return err
	}
	read := exec.CommandContext(ctx, "git", "--no-replace-objects", "cat-file", "commit", observation.CommitOID)
	read.Dir, read.Env = root, env
	var object coCommitOutput
	read.Stdout = &object
	if err := read.Run(); err != nil || object.overflow || object.Len() > 512<<10 {
		return coCommitFailure("commit_message_unavailable", "reconcile_canonical_receipt_only")
	}
	// Verify the actual immutable object, not a replace-ref view or merely an
	// object filename. A forged receipt cannot substitute another parent/tree.
	objectBytes := append([]byte(fmt.Sprintf("commit %d\x00", object.Len())), object.Bytes()...)
	var objectID string
	if len(observation.CommitOID) == 40 {
		hash := sha1.Sum(objectBytes)
		objectID = hex.EncodeToString(hash[:])
	} else {
		hash := sha256.Sum256(objectBytes)
		objectID = hex.EncodeToString(hash[:])
	}
	if objectID != observation.CommitOID {
		return coCommitFailure("commit_identity_mismatch", "reconcile_canonical_receipt_only")
	}
	headers, message, found := strings.Cut(object.String(), "\n\n")
	if !found {
		return coCommitFailure("commit_message_invalid", "reconcile_canonical_receipt_only")
	}
	var trees, parents []string
	for _, line := range strings.Split(headers, "\n") {
		if value, ok := strings.CutPrefix(line, "tree "); ok {
			trees = append(trees, value)
		}
		if value, ok := strings.CutPrefix(line, "parent "); ok {
			parents = append(parents, value)
		}
	}
	if len(trees) != 1 || trees[0] != observation.PreparedTree || len(parents) != 1 || parents[0] != intent.Head {
		return coCommitFailure("commit_identity_mismatch", "reconcile_canonical_receipt_only")
	}
	count := 0
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(strings.ToLower(line), "agent-history-request:") {
			if line != "Agent-History-Request: "+intent.CoCommit.RequestID {
				return coCommitFailure("commit_request_mismatch", "reconcile_canonical_receipt_only")
			}
			count++
		}
	}
	if count != 1 {
		return coCommitFailure("commit_request_mismatch", "reconcile_canonical_receipt_only")
	}
	if !coCommitDigest.MatchString(observation.ComposedMessageSHA256) || !coCommitDigest.MatchString(observation.NormalizedMessageSHA256) {
		return coCommitFailure("message_proof_missing", "reconcile_canonical_receipt_only")
	}
	cmd := exec.CommandContext(ctx, "git", "stripspace")
	cmd.Dir, cmd.Env = root, env
	cmd.Stdin = strings.NewReader(message)
	var normalized coCommitOutput
	cmd.Stdout = &normalized
	if err := cmd.Run(); err != nil || normalized.overflow {
		return coCommitFailure("message_proof_unavailable", "reconcile_canonical_receipt_only")
	}
	digest := sha256.Sum256(normalized.Bytes())
	if hex.EncodeToString(digest[:]) != observation.NormalizedMessageSHA256 {
		return coCommitFailure("commit_message_mismatch", "reconcile_canonical_receipt_only")
	}
	return nil
}

// FinalizeCoCommit delegates exactly one authorized attempt. A committed helper
// outcome whose native persistence fails is repaired only by reconciliation.
func (s *Service) FinalizeCoCommit(ctx context.Context, request CoCommitFinalizeRequest) (*CoCommitResult, error) {
	if request.ReviewFile != "" && (!filepath.IsAbs(request.ReviewFile) || filepath.Clean(request.ReviewFile) != request.ReviewFile || strings.ContainsAny(request.ReviewFile, "\x00\r\n")) {
		return nil, coCommitFailure("unsafe_review_path", "select_exact_absolute_private_review")
	}
	if !request.AllowCommit {
		return nil, coCommitFailure("authorization_required", "review_exact_run_then_allow_commit")
	}
	if s.Store == nil {
		return nil, errors.New("artifact service needs a store")
	}
	record, err := s.Store.GetRecord(request.IntentID)
	if err != nil {
		return nil, err
	}
	if request.Revision != "" && request.Revision != record.Revision {
		return nil, ErrStaleRevision
	}
	if record.Intent.Destination != "co-commit" {
		return nil, coCommitFailure("not_co_commit", "use_existing_artifact_finalizer")
	}
	common, err := pathx.Canonical(record.Intent.GitCommonDir)
	if err != nil {
		return nil, err
	}
	var result *CoCommitResult
	err = lockx.WithDir(ctx, filepath.Join(common, "dev-artifact-finalize"), "co-commit finalization", func() error {
		current, err := s.Store.GetRecord(request.IntentID)
		if err != nil {
			return err
		}
		if current.Revision != record.Revision {
			return ErrStaleRevision
		}
		intent := current.Intent
		helper, err := boundCoCommitHelper(ctx, intent)
		if err != nil {
			return err
		}
		observation, err := helper.inspect(ctx, intent.CoCommit.RequestPath, false)
		if err != nil {
			return err
		}
		if err := coCommitCurrentRequest(ctx, intent, observation); err != nil {
			return err
		}
		if !coCommitPostWriter(observation) {
			return coCommitFailure("writer_or_sync_unproven", "finish_real_wrapper_before_finalization")
		}
		if !observation.CommitProven {
			if intent.Status == Finalized {
				return coCommitFailure("recorded_commit_proof_missing", "reconcile_canonical_receipt_only")
			}
			if observation.AutomaticCommit == nil || *observation.AutomaticCommit {
				return coCommitFailure("manual_wrapper_required", "inspect_canonical_parent_authority")
			}
			reconcileOnly := observation.State == "committing"
			paths := append([]string{intent.SpecStoryPath}, intent.PlanPaths...)
			if !reconcileOnly {
				if request.Guard == nil {
					return coCommitFailure("writer_guard_missing", "use_guarded_external_finalizer")
				}
				if err := request.Guard(ctx, intent.WorktreePath, paths); err != nil {
					return err
				}
			}
			current, err = s.Store.UpdateIfRevision(ctx, intent.ID, current.Revision, func(candidate *Intent) error {
				candidate.Status = Finalizing
				candidate.FailureCode = ""
				return nil
			})
			if err != nil {
				return err
			}
			invoke := func(extra ...string) error {
				args := []string{"--request", intent.CoCommit.RequestPath, "--allow-commit", "--expected-revision", observation.JournalRevision}
				if request.ReviewFile != "" {
					args = append(args, "--review-file", request.ReviewFile)
				}
				if request.RotationConfirmed {
					args = append(args, "--rotation-confirmed")
				}
				args = append(args, extra...)
				var reply struct {
					Status string `json:"status"`
				}
				invokeErr := helper.call(ctx, "finalize", args, &reply)
				observation, err = helper.inspect(ctx, intent.CoCommit.RequestPath, false)
				if err != nil {
					result = newCoCommitResult("finalization_outcome_unproven", intent.CoCommit.RequestID, "inspect_canonical_run_do_not_retry")
					return &CoCommitError{Code: "finalization_outcome_unproven", RequestID: intent.CoCommit.RequestID, NextAction: "inspect_canonical_run_do_not_retry", MayHaveRun: true}
				}
				if err := coCommitBindingMatches(intent, observation); err != nil {
					return err
				}
				if !observation.CommitProven && invokeErr != nil {
					result = newCoCommitResult(observation.State, intent.CoCommit.RequestID, "review_canonical_run_no_automatic_retry")
					result.Intent, result.Revision, result.Observation = current.Intent, current.Revision, &observation
					return invokeErr
				}
				return nil
			}
			if reconcileOnly {
				if err := invoke("--reconcile-only"); err != nil {
					return err
				}
			} else {
				// Preparation is canonical and cannot commit. Release its run
				// lock, reload authority, and check the live native writer guard
				// again before the single commit-capable delegation.
				if err := invoke("--prepare-only"); err != nil {
					return err
				}
				if !observation.CommitProven {
					if observation.State != "prepared" || !coCommitPostWriter(observation) {
						return coCommitFailure("preparation_unproven", "inspect_retained_prepared_snapshot")
					}
					if err := coCommitCurrentRequest(ctx, intent, observation); err != nil {
						return err
					}
					if err := request.Guard(ctx, intent.WorktreePath, paths); err != nil {
						return err
					}
					latest, err := s.Store.GetRecord(intent.ID)
					if err != nil || latest.Revision != current.Revision {
						return ErrStaleRevision
					}
					if err := coCommitCurrentRequest(ctx, intent, observation); err != nil {
						return err
					}
					if err := invoke(); err != nil {
						return err
					}
				}
			}
			if !observation.CommitProven {
				return coCommitFailure("commit_unproven", "reconcile_canonical_receipt_only")
			}
		}
		if err := verifyCoCommitObject(ctx, intent.WorktreePath, intent, observation, intent.Status != Finalized); err != nil {
			return err
		}
		updated, err := s.Store.UpdateIfRevision(ctx, intent.ID, current.Revision, func(candidate *Intent) error {
			candidate.Status = Finalized
			candidate.ArtifactCommit = observation.CommitOID
			candidate.TranscriptPath = candidate.SpecStoryPath
			candidate.FailureCode = ""
			binding := *candidate.CoCommit
			binding.PreparedTree = observation.PreparedTree
			binding.ReceiptRevision = observation.ReceiptRevision
			candidate.CoCommit = &binding
			return nil
		})
		if err != nil {
			result = newCoCommitResult("committed_but_not_recorded", intent.CoCommit.RequestID, "reconcile_native_receipt_only")
			return &CoCommitError{Code: "committed_but_not_recorded", RequestID: intent.CoCommit.RequestID, NextAction: "reconcile_native_receipt_only", MayHaveRun: true}
		}
		result = newCoCommitResult("committed", intent.CoCommit.RequestID, "verify_before_history_movement")
		result.Intent, result.Revision, result.Observation = updated.Intent, updated.Revision, &observation
		return nil
	})
	return result, err
}

func inspectCoCommitReadiness(ctx context.Context, checkout string, intent Intent) (IntentReadiness, error) {
	evidence := IntentReadiness{Intent: intent, State: ReadinessPending}
	observation, err := ObserveCoCommit(ctx, &intent)
	if err != nil {
		evidence.State, evidence.ObservationError = ReadinessObservationError, err
		return evidence, err
	}
	evidence.CoCommit = &observation
	if !observation.CommitProven {
		if observation.State == "failed" || observation.State == "committing" {
			evidence.State = ReadinessFailed
		}
		return evidence, nil
	}
	if err := verifyCoCommitObject(ctx, checkout, &intent, observation, false); err != nil {
		evidence.State, evidence.ObservationError = ReadinessObservationError, err
		return evidence, err
	}
	proof := intent
	proof.ArtifactCommit = observation.CommitOID
	evidence.Finalized = true
	evidence.ReceiptReachable, err = receiptRemainsReachable(ctx, checkout, proof)
	if err != nil {
		evidence.State, evidence.ObservationError = ReadinessObservationError, err
		return evidence, err
	}
	if evidence.ReceiptReachable {
		evidence.State = ReadinessFinalizedReachable
	} else {
		evidence.State = ReadinessFinalizedUnreachable
	}
	return evidence, nil
}

// SafeCoCommitError keeps private tool and receipt diagnostics out of adapters.
func SafeCoCommitError(err error) error {
	if err == nil {
		return nil
	}
	var safe *CoCommitError
	if errors.As(err, &safe) || errors.Is(err, ErrStaleRevision) {
		return err
	}
	return fmt.Errorf("co-commit observation unavailable; inspect private local state")
}
