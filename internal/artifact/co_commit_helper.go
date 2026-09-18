package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

func canonicalSessionID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

const coCommitProtocolVersion = 2
const coCommitOutputLimit = 1 << 20

var coCommitDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var coCommitOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var coCommitCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,95}$`)

// CoCommitBinding refers to the canonical helper's authority. It is not another
// journal: native dev never fabricates child-exit, sync, review or commit proof.
type CoCommitBinding struct {
	ProtocolVersion     int    `json:"protocol_version"`
	HelperDir           string `json:"helper_dir"`
	HelperToken         string `json:"helper_identity"`
	HelperRevision      string `json:"helper_revision"`
	Python              string `json:"python"`
	PythonToken         string `json:"python_identity"`
	Bash                string `json:"bash"`
	BashToken           string `json:"bash_identity"`
	RequestPath         string `json:"request_path"`
	RequestID           string `json:"request_id"`
	RequestRevision     string `json:"request_revision"`
	InputIndexTree      string `json:"input_index_tree"`
	MessageSHA256       string `json:"message_sha256"`
	PlanPolicy          string `json:"plan_policy"`
	ArtifactPolicyToken string `json:"artifact_policy_identity"`
	PreparedTree        string `json:"prepared_tree,omitempty"`
	ReceiptRevision     string `json:"receipt_revision,omitempty"`
}

func (b CoCommitBinding) validate() error {
	if b.ProtocolVersion != coCommitProtocolVersion || !canonicalSessionID(b.RequestID) {
		return errors.New("unsupported co-commit binding")
	}
	for _, path := range []string{b.HelperDir, b.Python, b.Bash, b.RequestPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("co-commit binding has an unsafe path")
		}
	}
	for _, digest := range []string{b.HelperToken, b.HelperRevision, b.PythonToken, b.BashToken, b.RequestRevision, b.MessageSHA256, b.ArtifactPolicyToken} {
		if !coCommitDigest.MatchString(digest) {
			return errors.New("co-commit binding lacks exact identity")
		}
	}
	if !coCommitOID.MatchString(b.InputIndexTree) || b.PreparedTree != "" && !coCommitOID.MatchString(b.PreparedTree) {
		return errors.New("co-commit binding has an invalid tree")
	}
	if b.ReceiptRevision != "" && !coCommitDigest.MatchString(b.ReceiptRevision) {
		return errors.New("co-commit binding has an invalid review receipt")
	}
	if b.PlanPolicy != "path" && b.PlanPolicy != "none" {
		return errors.New("co-commit binding lacks explicit plan policy")
	}
	return nil
}

// CoCommitObservation is the bounded v2 helper inspection. Raw findings and the
// base/composed message are deliberately absent from this transport.
type CoCommitObservation struct {
	SchemaVersion           int      `json:"schema_version"`
	ProtocolVersion         int      `json:"protocol_version"`
	HelperRevision          string   `json:"helper_revision"`
	Capabilities            []string `json:"capabilities,omitempty"`
	Status                  string   `json:"status"`
	RunID                   string   `json:"run_id"`
	RequestPath             string   `json:"request_path"`
	RequestID               string   `json:"request_id"`
	RequestRevision         string   `json:"request_revision"`
	JournalRevision         string   `json:"journal_revision"`
	State                   string   `json:"state"`
	WorktreeRoot            string   `json:"worktree_root"`
	GitDir                  string   `json:"git_dir"`
	HeadRef                 string   `json:"head_ref"`
	HeadOID                 string   `json:"head_oid"`
	InputIndexTree          string   `json:"input_index_tree"`
	Provider                string   `json:"provider"`
	SessionID               string   `json:"session_id"`
	SpecStoryPath           string   `json:"specstory_path"`
	PlanPolicy              string   `json:"plan_policy"`
	PlanPath                string   `json:"plan_path"`
	MessageSHA256           string   `json:"message_sha256"`
	ComposedMessageSHA256   string   `json:"composed_message_sha256"`
	NormalizedMessageSHA256 string   `json:"normalized_message_sha256"`
	PreparedTree            string   `json:"prepared_tree"`
	CommitOID               string   `json:"commit_oid"`
	ChildExitCode           *int     `json:"child_exit_code"`
	ChildSignal             *int     `json:"child_signal"`
	GroupQuiescent          *bool    `json:"group_quiescent"`
	SyncStatus              string   `json:"sync_status"`
	FreshnessStatus         string   `json:"freshness_status"`
	ReviewStatus            string   `json:"review_status"`
	ReceiptRevision         string   `json:"receipt_revision"`
	AuthenticContext        bool     `json:"authentic_context"`
	AutomaticCommit         *bool    `json:"automatic_commit"`
	CloudSync               *bool    `json:"cloud_sync"`
	CommitProven            bool     `json:"commit_proven"`
}

type coCommitCapabilities struct {
	SchemaVersion   int      `json:"schema_version"`
	ProtocolVersion int      `json:"protocol_version"`
	HelperRevision  string   `json:"helper_revision"`
	Capabilities    []string `json:"capabilities"`
}

// CoCommitError is safe for terminal and JSON output even if a native tool
// failed with sensitive diagnostics. Started effects must never be auto-retried.
type CoCommitError struct {
	Code       string `json:"code"`
	RequestID  string `json:"request_id,omitempty"`
	NextAction string `json:"next_action"`
	MayHaveRun bool   `json:"may_have_run,omitempty"`
}

func (e *CoCommitError) Error() string {
	return fmt.Sprintf("co-commit %s; next: %s", e.Code, e.NextAction)
}

func coCommitFailure(code, next string) error {
	return &CoCommitError{Code: code, NextAction: next}
}

type coCommitHelper struct {
	root    string
	binding CoCommitBinding
}

func coCommitEnvironment() ([]string, error) {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		// A foreign index/worktree or injected Python import path cannot name
		// the authority reviewed by the caller. Do not silently reinterpret it.
		if strings.HasPrefix(key, "GIT_") && key != "GIT_OPTIONAL_LOCKS" {
			if key == "GIT_INDEX_FILE" || key == "GIT_DIR" || key == "GIT_WORK_TREE" || key == "GIT_COMMON_DIR" || key == "GIT_OBJECT_DIRECTORY" || key == "GIT_ALTERNATE_OBJECT_DIRECTORIES" || key == "GIT_CONFIG_COUNT" || key == "GIT_CONFIG_PARAMETERS" || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
				return nil, coCommitFailure("git_environment_override", "use_canonical_checkout_environment")
			}
		}
		if strings.HasPrefix(key, "PYTHON") || strings.HasPrefix(key, "BASH_FUNC_") || key == "BASH_ENV" || key == "ENV" || key == "SHELLOPTS" || key == "BASHOPTS" || key == "GIT_OPTIONAL_LOCKS" {
			continue
		}
		env = append(env, value)
	}
	return append(env, "PYTHONDONTWRITEBYTECODE=1", "GIT_OPTIONAL_LOCKS=0"), nil
}

func coCommitTool(ctx context.Context, name string) (string, string, error) {
	if name == "bash" && runtime.GOOS == "darwin" {
		name = "/bin/bash" // canonical helper uses the verified stock Bash 3.2 boundary
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", "", coCommitFailure("dependency_missing", "install_native_helper_dependencies")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return "", "", coCommitFailure("unsafe_tool", "inspect_native_helper_dependencies")
	}
	token, err := coCommitExecutableToken(ctx, path)
	if err != nil {
		return "", "", coCommitFailure("unverified_tool", "inspect_native_helper_dependencies")
	}
	return path, token, nil
}

// Executables are observed, never replaced. Unlike configuration transactions,
// this permits root-owned system tools and standard package-manager ancestors.
// The held-path read and persistent inode/content identity still reject drift.
func coCommitExecutableToken(ctx context.Context, path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 || before.Mode().Perm()&0o022 != 0 {
		return "", errors.New("unverified executable")
	}
	metadata := reflect.Indirect(reflect.ValueOf(before.Sys()))
	if metadata.Kind() != reflect.Struct {
		return "", errors.New("unsupported executable identity")
	}
	dev, ino, uid, gid := metadata.FieldByName("Dev"), metadata.FieldByName("Ino"), metadata.FieldByName("Uid"), metadata.FieldByName("Gid")
	if !dev.IsValid() || !ino.IsValid() || !uid.IsValid() || !gid.IsValid() || !uid.CanUint() || (uid.Uint() != 0 && uid.Uint() != uint64(os.Getuid())) {
		return "", errors.New("unsupported executable ownership")
	}
	data, err := safefile.ReadStablePath(ctx, path, 128<<20)
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("executable changed during observation")
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%v:%v:%v:%v:%d:%d:%d\x00", path, dev.Interface(), ino.Interface(), uid.Interface(), gid.Interface(), before.Size(), before.ModTime().UnixNano(), before.Mode())
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func coCommitHelperToken(ctx context.Context, dir string) (string, error) {
	root := filepath.Dir(dir)
	if filepath.Base(dir) != "scripts" {
		return "", errors.New("co-commit helper must name its canonical scripts directory")
	}
	var members []string
	var total int64
	for _, sub := range []string{"scripts", "assets"} {
		base := filepath.Join(root, sub)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
				return errors.New("unsafe co-commit helper member")
			}
			if entry.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() || len(members) >= 256 || info.Size() > 8<<20 {
				return errors.New("unsupported co-commit helper layout")
			}
			total += info.Size()
			if total > 32<<20 {
				return errors.New("co-commit helper exceeds source limit")
			}
			token, err := configedit.InspectToken(ctx, path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			members = append(members, filepath.ToSlash(relative)+"\x00"+token)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	for _, name := range []string{"_post_session.py", "queue-agent-commit.sh", "finalize-agent-commit.sh"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("canonical co-commit helper is unavailable")
		}
	}
	sort.Strings(members)
	digest := sha256.Sum256([]byte(dir + "\x00" + strings.Join(members, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func resolveCoCommitHelper(ctx context.Context, root, selected string) (*coCommitHelper, error) {
	if runtime.GOOS == "windows" {
		return nil, coCommitFailure("unsupported_platform", "use_verified_posix_helper")
	}
	if _, err := coCommitEnvironment(); err != nil {
		return nil, err
	}
	var candidates []string
	if selected != "" {
		if !filepath.IsAbs(selected) {
			selected = filepath.Join(root, selected)
		}
		candidates = []string{selected}
	} else {
		for _, agent := range []string{".claude", ".agents"} {
			candidates = append(candidates, filepath.Join(root, agent, "skills", "agent-history-hygiene", "scripts"))
		}
	}
	unique := make(map[string]bool)
	for _, candidate := range candidates {
		canonical, err := filepath.EvalSymlinks(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !filepath.IsAbs(canonical) {
			return nil, coCommitFailure("unsafe_helper", "inspect_canonical_helper_installation")
		}
		if _, err := os.Stat(filepath.Join(canonical, "_post_session.py")); errors.Is(err, os.ErrNotExist) {
			continue
		}
		unique[canonical] = true
	}
	if len(unique) != 1 {
		return nil, coCommitFailure("helper_unavailable_or_ambiguous", "select_installed_v2_helper")
	}
	var dir string
	for path := range unique {
		dir = path
	}
	token, err := coCommitHelperToken(ctx, dir)
	if err != nil {
		return nil, coCommitFailure("unsafe_helper", "inspect_canonical_helper_installation")
	}
	python, pythonToken, err := coCommitTool(ctx, "python3")
	if err != nil {
		return nil, err
	}
	bash, bashToken, err := coCommitTool(ctx, "bash")
	if err != nil {
		return nil, err
	}
	h := &coCommitHelper{root: root, binding: CoCommitBinding{ProtocolVersion: coCommitProtocolVersion, HelperDir: dir, HelperToken: token, Python: python, PythonToken: pythonToken, Bash: bash, BashToken: bashToken}}
	var capabilities coCommitCapabilities
	if err := h.call(ctx, "capabilities", []string{"--json"}, &capabilities); err != nil {
		return nil, err
	}
	if capabilities.SchemaVersion != 2 || capabilities.ProtocolVersion != 2 || !coCommitDigest.MatchString(capabilities.HelperRevision) {
		return nil, coCommitFailure("unsupported_helper", "install_compatible_v2_helper")
	}
	set := make(map[string]bool)
	for _, capability := range capabilities.Capabilities {
		set[capability] = true
	}
	for _, required := range []string{"inspect_v2", "authentic_context_v2", "queue_idempotent_v2", "sanitation_receipt_v2", "review_v2", "exact_export_v2", "no_cloud_v2", "prepare_only_v2", "revision_guard_v2", "reconcile_only_v2"} {
		if !set[required] {
			return nil, coCommitFailure("unsupported_helper", "install_compatible_v2_helper")
		}
	}
	h.binding.HelperRevision = capabilities.HelperRevision
	return h, nil
}

func boundCoCommitHelper(ctx context.Context, intent *Intent) (*coCommitHelper, error) {
	if intent.CoCommit == nil || intent.CoCommit.validate() != nil {
		return nil, coCommitFailure("invalid_binding", "inspect_native_binding")
	}
	h := &coCommitHelper{root: intent.WorktreePath, binding: *intent.CoCommit}
	if err := h.revalidate(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *coCommitHelper) revalidate(ctx context.Context) error {
	current, err := coCommitHelperToken(ctx, h.binding.HelperDir)
	if err != nil || current != h.binding.HelperToken {
		return coCommitFailure("helper_changed", "review_helper_binding")
	}
	for _, tool := range []struct{ name, path, token string }{{"python3", h.binding.Python, h.binding.PythonToken}, {"bash", h.binding.Bash, h.binding.BashToken}} {
		path, token, err := coCommitTool(ctx, tool.name)
		if err != nil || path != tool.path || token != tool.token {
			return coCommitFailure("tool_changed", "review_helper_binding")
		}
	}
	return nil
}

type coCommitOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *coCommitOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := coCommitOutputLimit - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (h *coCommitHelper) call(ctx context.Context, operation string, args []string, target any) error {
	if err := h.revalidate(ctx); err != nil {
		return err
	}
	env, err := coCommitEnvironment()
	if err != nil {
		return err
	}
	timeout := 2 * time.Minute
	binary := h.binding.Python
	argv := []string{"-I", "-B", filepath.Join(h.binding.HelperDir, "_post_session.py"), operation}
	if operation == "queue" || operation == "finalize" {
		timeout = 20 * time.Minute
		argv = append(argv, "--script-dir", h.binding.HelperDir)
	}
	argv = append(argv, args...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir, cmd.Env = h.root, env
	var stdout, stderr coCommitOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if stdout.overflow || stderr.overflow {
		return &CoCommitError{Code: "helper_output_incomplete", NextAction: "inspect_existing_request_do_not_retry", MayHaveRun: operation == "queue" || operation == "finalize"}
	}
	if err != nil {
		var result struct {
			Status string `json:"status"`
		}
		code, next := "helper_failed", "inspect_existing_request_do_not_retry"
		if json.Unmarshal(stdout.Bytes(), &result) == nil {
			switch result.Status {
			case "review_required":
				code, next = result.Status, "review_exact_prepared_snapshot"
			case "rotation_required":
				code, next = result.Status, "rotate_then_review_exact_recovery"
			case "authorization_required":
				code, next = result.Status, "review_exact_run_then_allow_commit"
			case "stale_revision", "stale_state", "stale_prepared_state", "draft_conflict", "scan_failed", "staging_failed":
				code = result.Status
			case "active_writer":
				code, next = result.Status, "wait_for_real_writer_exit"
			case "missing_runner_context":
				code, next = "requires_wrapper", "start_real_wrapper_with_explicit_approval"
			case "commit_failed":
				code, next = result.Status, "inspect_retained_prepared_snapshot"
			case "reconciliation_required", "commit_unproven", "commit_outcome_unknown":
				code, next = result.Status, "reconcile_canonical_receipt_only"
			}
		}
		return &CoCommitError{Code: code, NextAction: next, MayHaveRun: operation == "queue" || operation == "finalize"}
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(target); err != nil {
		return &CoCommitError{Code: "invalid_helper_response", NextAction: "inspect_existing_request_do_not_retry", MayHaveRun: operation == "queue" || operation == "finalize"}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return &CoCommitError{Code: "invalid_helper_response", NextAction: "inspect_existing_request_do_not_retry", MayHaveRun: operation == "queue" || operation == "finalize"}
	}
	if err := h.revalidate(ctx); err != nil {
		return &CoCommitError{Code: "helper_changed_during_operation", NextAction: "inspect_existing_request_do_not_retry", MayHaveRun: operation == "queue" || operation == "finalize"}
	}
	return nil
}

func (h *coCommitHelper) inspect(ctx context.Context, requestPath string, active bool) (observation CoCommitObservation, err error) {
	defer func() {
		if err != nil {
			observation = CoCommitObservation{}
		}
	}()
	args := []string{"--request", requestPath, "--json"}
	if active {
		args = []string{"--context", "--json"}
	}
	if err := h.call(ctx, "inspect", args, &observation); err != nil {
		return observation, err
	}
	if observation.SchemaVersion != 2 || observation.ProtocolVersion != 2 || observation.HelperRevision != h.binding.HelperRevision || !coCommitDigest.MatchString(observation.JournalRevision) || !canonicalSessionID(observation.RunID) {
		return observation, coCommitFailure("invalid_helper_observation", "inspect_existing_request_do_not_retry")
	}
	switch observation.State {
	case "running", "pending", "child_exited", "syncing", "synced", "prepared", "review_required", "rotation_required", "committing", "done", "failed":
	default:
		return observation, coCommitFailure("invalid_helper_state", "inspect_existing_request_do_not_retry")
	}
	if !coCommitOneOf(observation.SyncStatus, "not_started", "pending", "proven", "failed") || !coCommitOneOf(observation.FreshnessStatus, "unproven", "proven", "stale") || !coCommitOneOf(observation.ReviewStatus, "not_required", "unresolved", "reviewed_noncredential", "credential_rotation_required", "stale") {
		return observation, coCommitFailure("invalid_helper_proof", "inspect_existing_request_do_not_retry")
	}
	// Do not forward arbitrary provider status labels or capability strings.
	observation.Status, observation.Capabilities = "observed", nil
	root, err := pathx.Canonical(observation.WorktreeRoot)
	if err != nil || root != h.root || observation.WorktreeRoot != h.root || active && !observation.AuthenticContext {
		return observation, coCommitFailure("requires_wrapper", "start_real_wrapper_with_explicit_approval")
	}
	return observation, nil
}

func coCommitOneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
