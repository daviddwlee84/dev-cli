// Package claudeworkflow adapts Claude Workflow's private, unversioned local
// metadata into provider-neutral ephemeral-worktree claims. It deliberately
// reads only bounded structural fields; prompts, scripts, logs, result bodies,
// and transcript content are never decoded.
package claudeworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/ephemeral"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const providerName = "claude-workflow"

type Limits struct {
	MaxProjects       int
	MaxSessions       int
	MaxWorkflows      int
	MaxAgents         int
	MaxJSONBytes      int64
	MaxJournalBytes   int64
	MaxJournalRecords int
}

var DefaultLimits = Limits{
	MaxProjects: 256, MaxSessions: 2048, MaxWorkflows: 2048, MaxAgents: 8192,
	MaxJSONBytes: 1 << 20, MaxJournalBytes: 4 << 20, MaxJournalRecords: 16_384,
}

type Option func(*Adapter)

func WithLimits(limits Limits) Option { return func(a *Adapter) { a.limits = limits } }

// withAfterRead is a deterministic mutation-race seam kept package-private so
// production callers cannot weaken or alter the read protocol.
func withAfterRead(hook func(string)) Option { return func(a *Adapter) { a.afterRead = hook } }

type Adapter struct {
	root      string
	limits    Limits
	afterRead func(string)
}

func New(root string, options ...Option) *Adapter {
	a := &Adapter{root: root, limits: DefaultLimits}
	for _, option := range options {
		option(a)
	}
	return a
}

type counts struct {
	projects  int
	sessions  int
	workflows int
	agents    int
}

type workflowRecord struct {
	id           string
	status       string
	activity     []time.Time
	timeKnown    bool
	mtime        time.Time
	progressByID map[string]progressRecord
}

type progressRecord struct {
	agentID   string
	isolation string
	state     string
	valid     bool
	activity  []time.Time
	timeKnown bool
}

type metaRecord struct {
	workflowID string
	runID      string
	agentID    string
	path       string
	spawned    bool
	isolation  string
	state      string
	updatedAt  time.Time
	timeKnown  bool
	mtime      time.Time
}

type journalEvent struct {
	kind       string
	workflowID string
	runID      string
	agentID    string
	key        string
	at         time.Time
	timeKnown  bool
}

type scanFailure struct {
	code string
}

func (e *scanFailure) Error() string { return e.code }

func failure(code string) error { return &scanFailure{code: code} }

func (a *Adapter) Collect(ctx context.Context, query ephemeral.SourceQuery) ephemeral.SourceResult {
	result := ephemeral.SourceResult{Provider: providerName, Complete: true}
	capability := ephemeral.Capability{Name: "claude-workflow-metadata", Available: true, Detail: "bounded local metadata"}
	result.Capabilities = []ephemeral.Capability{capability}
	if err := ctx.Err(); err != nil {
		return a.failed(result, "canceled")
	}
	if err := a.validateLimits(); err != nil {
		return a.failed(result, "metadata-bound")
	}
	rootInfo, err := os.Lstat(a.root)
	if errors.Is(err, fs.ErrNotExist) {
		result.Capabilities[0].Detail = "metadata root absent"
		return result
	}
	if err != nil || !safeMetadataInfo(a.root, rootInfo, true) {
		return a.failed(result, "unsafe-metadata")
	}

	var used counts
	projects, err := a.directories(a.root, &used.projects, a.limits.MaxProjects)
	if err != nil {
		return a.failed(result, failureCode(err))
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return a.failed(result, "canceled")
		}
		projectPath, err := fixedChild(a.root, project)
		if err != nil {
			return a.failed(result, "invalid-id")
		}
		sessions, err := a.directories(projectPath, &used.sessions, a.limits.MaxSessions)
		if err != nil {
			return a.failed(result, failureCode(err))
		}
		for _, session := range sessions {
			claims, scanErr := a.collectSession(ctx, query, projectPath, session, &used)
			if scanErr != nil {
				return a.failed(result, failureCode(scanErr))
			}
			result.Claims = append(result.Claims, claims...)
		}
	}

	a.rejectDuplicateClaims(&result)
	sort.Slice(result.Claims, func(i, j int) bool {
		left, right := result.Claims[i], result.Claims[j]
		if left.WorktreePath != right.WorktreePath {
			return left.WorktreePath < right.WorktreePath
		}
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		return left.AgentID < right.AgentID
	})
	return result
}

func (a *Adapter) failed(result ephemeral.SourceResult, code string) ephemeral.SourceResult {
	result.Complete = false
	result.Claims = nil
	result.Capabilities[0].Available = false
	result.Capabilities[0].Detail = "metadata could not be safely verified"
	result.Diagnostics = append(result.Diagnostics, ephemeral.Diagnostic{
		Source: providerName, Code: code, Message: diagnosticMessage(code),
	})
	return result
}

func diagnosticMessage(code string) string {
	switch code {
	case "metadata-bound":
		return "provider metadata exceeded a fixed V1 bound"
	case "metadata-mutated":
		return "provider metadata changed while it was inspected"
	case "unsafe-metadata":
		return "provider metadata was not a private regular non-link path"
	case "invalid-workflow":
		return "workflow metadata has an invalid required field"
	case "invalid-agent-mapping":
		return "agent mapping metadata has an invalid required field"
	case "invalid-journal":
		return "workflow journal has an invalid required record"
	case "invalid-worktree-path":
		return "agent mapping contains an invalid or unrelated worktree path"
	case "invalid-id":
		return "provider metadata contains an invalid identifier"
	case "canceled":
		return "provider metadata inspection was canceled"
	default:
		return "provider metadata could not be safely verified"
	}
}

func (a *Adapter) validateLimits() error {
	values := []int{a.limits.MaxProjects, a.limits.MaxSessions, a.limits.MaxWorkflows, a.limits.MaxAgents, a.limits.MaxJournalRecords}
	for _, value := range values {
		if value < 0 {
			return failure("metadata-bound")
		}
	}
	if a.limits.MaxJSONBytes <= 0 || a.limits.MaxJournalBytes <= 0 {
		return failure("metadata-bound")
	}
	return nil
}

func (a *Adapter) collectSession(ctx context.Context, query ephemeral.SourceQuery, projectPath, session string, used *counts) ([]ephemeral.Claim, error) {
	if err := pathx.ValidateComponent(session); err != nil {
		return nil, failure("invalid-id")
	}
	sessionPath, err := fixedChild(projectPath, session)
	if err != nil {
		return nil, failure("invalid-id")
	}
	workflowsPath := filepath.Join(sessionPath, "workflows")
	if exists, err := safeOptionalDirectory(workflowsPath); err != nil {
		return nil, err
	} else if !exists {
		return nil, nil
	}
	entries, err := a.readDirectory(workflowsPath, a.limits.MaxWorkflows)
	if err != nil {
		return nil, err
	}
	var claims []ephemeral.Claim
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "wf_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		used.workflows++
		if used.workflows > a.limits.MaxWorkflows {
			return nil, failure("metadata-bound")
		}
		runID := strings.TrimSuffix(name, ".json")
		if err := validateID(runID, "wf_"); err != nil {
			return nil, failure("invalid-id")
		}
		workflowPath, err := fixedChild(workflowsPath, name)
		if err != nil {
			return nil, failure("invalid-id")
		}
		workflow, err := a.readWorkflow(workflowPath, runID)
		if err != nil {
			return nil, err
		}
		found, err := a.collectWorkflow(ctx, query, sessionPath, session, workflow, used)
		if err != nil {
			return nil, err
		}
		claims = append(claims, found...)
	}
	return claims, nil
}

func (a *Adapter) collectWorkflow(ctx context.Context, query ephemeral.SourceQuery, sessionPath, session string, workflow workflowRecord, used *counts) ([]ephemeral.Claim, error) {
	mappingPath := filepath.Join(sessionPath, "subagents", "workflows", workflow.id)
	if exists, err := safeFixedDirectoryChain(sessionPath, "subagents", "workflows", workflow.id); err != nil {
		return nil, err
	} else if !exists {
		return nil, failure("invalid-agent-mapping")
	}
	mappingBound, err := boundPlusOne(a.limits.MaxAgents)
	if err != nil {
		return nil, err
	}
	entries, err := a.readDirectory(mappingPath, mappingBound)
	if err != nil {
		return nil, err
	}
	journal, journalMtime, journalTimeKnown, err := a.readJournal(mappingPath, workflow.id)
	if err != nil {
		return nil, err
	}
	var claims []ephemeral.Claim
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, failure("canceled")
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		used.agents++
		if used.agents > a.limits.MaxAgents {
			return nil, failure("metadata-bound")
		}
		filenameID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".meta.json")
		if err := validateID(filenameID, ""); err != nil {
			return nil, failure("invalid-id")
		}
		metaPath, err := fixedChild(mappingPath, name)
		if err != nil {
			return nil, failure("invalid-id")
		}
		meta, mappingEntry, err := a.readMeta(metaPath, workflow.id, filenameID)
		if err != nil {
			return nil, err
		}
		if !mappingEntry {
			continue
		}
		canonicalPath, related, err := validateWorktreePath(query, meta.path)
		if err != nil {
			return nil, failure("invalid-worktree-path")
		}
		if !related {
			continue
		}
		progress, progressOK := workflow.progressByID[filenameID]
		meta.isolation, meta.state = progress.isolation, progress.state
		mappingOK := filenameID == meta.agentID && workflow.id == meta.workflowID && workflow.id == meta.runID &&
			meta.spawned && progressOK && progress.valid && meta.isolation == "worktree"
		started, result, startedAt, resultAt, eventTimesKnown := journalFacts(journal, workflow.id, meta.runID, meta.agentID)
		resumed, resumeMtime, err := resumeExists(sessionPath, meta.agentID)
		if err != nil {
			return nil, err
		}
		activity := append([]time.Time(nil), workflow.activity...)
		activity = append(activity, progress.activity...)
		activity = append(activity, meta.updatedAt, startedAt, resultAt, workflow.mtime, meta.mtime, journalMtime, resumeMtime)
		last, timeKnown := lastActivity(query.Now, activity,
			workflow.timeKnown && progress.timeKnown && meta.timeKnown && journalTimeKnown && eventTimesKnown,
		)
		// Claude Workflow 2.1.259 does not record branch, HEAD, common-dir,
		// or a non-replayable worktree-registration generation. GitIdentity must
		// therefore remain unknown: a terminal path claim is report-only.
		claims = append(claims, ephemeral.Claim{
			Provider: providerName, SessionID: session, RunID: workflow.id, AgentID: filenameID,
			WorktreePath: canonicalPath, Owned: ephemeral.KnownFact(true), Unique: ephemeral.KnownFact(true),
			Mapping: ephemeral.KnownFact(mappingOK), WorkflowState: normalizeWorkflowState(workflow.status),
			WorkflowTerminal: ephemeral.KnownFact(workflow.status == "completed" || workflow.status == "killed"),
			AgentState:       normalizeAgentState(meta.state), AgentDone: ephemeral.KnownFact(meta.state == "done"),
			JournalStarted: ephemeral.KnownFact(started), JournalResult: ephemeral.KnownFact(result),
			NotResumed: ephemeral.KnownFact(!resumed), LastActivity: last, LastActivityKnown: timeKnown,
		})
	}
	return claims, nil
}

func (a *Adapter) readWorkflow(path, filenameID string) (workflowRecord, error) {
	data, info, err := a.readStable(path, a.limits.MaxJSONBytes)
	if err != nil {
		return workflowRecord{}, err
	}
	object, err := decodeObject(data)
	if err != nil {
		return workflowRecord{}, failure("invalid-workflow")
	}
	runID, okRun := requiredString(object, "runId")
	if !okRun || runID != filenameID {
		return workflowRecord{}, failure("workflow-id-mismatch")
	}
	if _, present := object["id"]; present {
		id, okID := requiredString(object, "id")
		if !okID || id != filenameID {
			return workflowRecord{}, failure("workflow-id-mismatch")
		}
	}
	status, okStatus := requiredString(object, "status")
	if !okStatus {
		return workflowRecord{}, failure("invalid-workflow-status")
	}
	if err := validateID(runID, "wf_"); err != nil {
		return workflowRecord{}, failure("invalid-id")
	}
	activity, timeKnown := timestampFields(object, []string{"timestamp", "updatedAt"}, []string{"startTime"})
	progressByID, progressActivity, progressTimeKnown, err := decodeProgress(object["workflowProgress"])
	if err != nil {
		return workflowRecord{}, err
	}
	activity = append(activity, progressActivity...)
	return workflowRecord{
		id: runID, status: strings.ToLower(status), activity: activity,
		timeKnown: timeKnown && progressTimeKnown, mtime: info.ModTime(), progressByID: progressByID,
	}, nil
}

func decodeProgress(raw json.RawMessage) (map[string]progressRecord, []time.Time, bool, error) {
	if len(raw) == 0 {
		return map[string]progressRecord{}, nil, true, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, nil, false, failure("invalid-workflow-progress")
	}
	byID := make(map[string]progressRecord, len(entries))
	var allActivity []time.Time
	allKnown := true
	for _, entry := range entries {
		object, err := decodeObject(entry)
		if err != nil {
			allKnown = false
			continue
		}
		agentID, okAgent := requiredString(object, "agentId")
		if !okAgent || validateID(agentID, "") != nil {
			allKnown = false
			continue
		}
		isolation, okIsolation := requiredString(object, "isolation")
		state, okState := requiredString(object, "state")
		valid := okIsolation && okState
		if _, duplicate := byID[agentID]; duplicate {
			return nil, nil, false, failure("duplicate-agent-progress")
		}
		queued, queuedKnown := optionalEpochMillis(object, "queuedAt")
		started, startedKnown := optionalEpochMillis(object, "startedAt")
		last, lastKnown := optionalEpochMillis(object, "lastProgressAt")
		known := queuedKnown && startedKnown && lastKnown
		if !queued.IsZero() && !started.IsZero() && started.Before(queued) {
			known = false
		}
		if !started.IsZero() && !last.IsZero() && last.Before(started) {
			known = false
		}
		activity := nonZeroTimes(queued, started, last)
		byID[agentID] = progressRecord{
			agentID: agentID, isolation: strings.ToLower(isolation), state: strings.ToLower(state), valid: valid,
			activity: activity, timeKnown: known,
		}
		allActivity = append(allActivity, activity...)
		allKnown = allKnown && known
	}
	return byID, allActivity, allKnown, nil
}

func timestampFields(object map[string]json.RawMessage, stringKeys, millisKeys []string) ([]time.Time, bool) {
	known := true
	var values []time.Time
	for _, key := range stringKeys {
		value, ok := optionalTimestamp(object, key)
		known = known && ok
		if !value.IsZero() {
			values = append(values, value)
		}
	}
	for _, key := range millisKeys {
		value, ok := optionalEpochMillis(object, key)
		known = known && ok
		if !value.IsZero() {
			values = append(values, value)
		}
	}
	return values, known
}

func optionalEpochMillis(object map[string]json.RawMessage, key string) (time.Time, bool) {
	raw, present := object[key]
	if !present {
		return time.Time{}, true
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return time.Time{}, false
	}
	millis, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || millis <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(millis).UTC(), true
}

func nonZeroTimes(values ...time.Time) []time.Time {
	out := make([]time.Time, 0, len(values))
	for _, value := range values {
		if !value.IsZero() {
			out = append(out, value)
		}
	}
	return out
}

func (a *Adapter) readMeta(path, pathWorkflowID, filenameAgentID string) (metaRecord, bool, error) {
	data, info, err := a.readStable(path, a.limits.MaxJSONBytes)
	if err != nil {
		return metaRecord{}, false, err
	}
	object, err := decodeObject(data)
	if err != nil {
		return metaRecord{}, false, failure("invalid-agent-mapping")
	}
	_, pathPresent := object["worktreePath"]
	_, snakePathPresent := object["worktree_path"]
	_, shortPathPresent := object["worktree"]
	_, spawnedPresent := object["spawnedWithWorktree"]
	if !pathPresent && !snakePathPresent && !shortPathPresent && !spawnedPresent {
		return metaRecord{}, false, nil
	}
	workflowID := pathWorkflowID
	if _, present := object["workflowId"]; present {
		var ok bool
		workflowID, ok = requiredString(object, "workflowId")
		if !ok || workflowID != pathWorkflowID {
			return metaRecord{}, false, failure("invalid-agent-workflow")
		}
	}
	runID := pathWorkflowID
	if _, present := object["runId"]; present {
		var ok bool
		runID, ok = requiredString(object, "runId")
		if !ok || runID != pathWorkflowID {
			return metaRecord{}, false, failure("invalid-agent-run")
		}
	}
	agentID := filenameAgentID
	if _, present := object["agentId"]; present {
		var ok bool
		agentID, ok = requiredString(object, "agentId")
		if !ok {
			return metaRecord{}, false, failure("invalid-agent-id")
		}
	}
	worktreePath, okPath := oneString(object, "worktreePath", "worktree_path", "worktree")
	spawned, okSpawned := requiredBool(object, "spawnedWithWorktree")
	if !okPath || !okSpawned {
		return metaRecord{}, false, failure("invalid-agent-mapping")
	}
	updated, timeKnown := optionalTimestamp(object, "updatedAt")
	for _, item := range []struct {
		value  string
		prefix string
	}{{workflowID, "wf_"}, {runID, "wf_"}, {agentID, ""}} {
		if err := validateID(item.value, item.prefix); err != nil {
			return metaRecord{}, false, failure("invalid-id")
		}
	}
	return metaRecord{
		workflowID: workflowID, runID: runID, agentID: agentID, path: worktreePath,
		spawned: spawned, updatedAt: updated, timeKnown: timeKnown, mtime: info.ModTime(),
	}, true, nil
}

func (a *Adapter) readJournal(mappingPath, pathWorkflowID string) ([]journalEvent, time.Time, bool, error) {
	path := filepath.Join(mappingPath, "journal.jsonl")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, true, nil
	}
	if err != nil {
		return nil, time.Time{}, false, failure("unsafe-metadata")
	}
	data, stableInfo, err := a.readStableWithInfo(path, info, a.limits.MaxJournalBytes)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > a.limits.MaxJournalRecords {
		return nil, time.Time{}, false, failure("metadata-bound")
	}
	events := make([]journalEvent, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		object, err := decodeObject(line)
		if err != nil {
			return nil, time.Time{}, false, failure("invalid-journal")
		}
		kind, ok := oneString(object, "type", "event")
		if !ok {
			return nil, time.Time{}, false, failure("invalid-journal-kind")
		}
		kind = strings.ToLower(kind)
		if kind != "started" && kind != "result" {
			continue
		}
		workflowID := pathWorkflowID
		if _, present := object["workflowId"]; present {
			var okWorkflow bool
			workflowID, okWorkflow = requiredString(object, "workflowId")
			if !okWorkflow || workflowID != pathWorkflowID {
				return nil, time.Time{}, false, failure("invalid-journal-workflow")
			}
		}
		runID := pathWorkflowID
		if _, present := object["runId"]; present {
			var okRun bool
			runID, okRun = requiredString(object, "runId")
			if !okRun || runID != pathWorkflowID {
				return nil, time.Time{}, false, failure("invalid-journal-run")
			}
		}
		agentID, okAgent := requiredString(object, "agentId")
		if !okAgent {
			return nil, time.Time{}, false, failure("invalid-journal-agent")
		}
		key, okKey := requiredString(object, "key")
		if !okKey {
			return nil, time.Time{}, false, failure("invalid-journal-key")
		}
		at, timeKnown := optionalTimestamp(object, "timestamp")
		if validateID(workflowID, "wf_") != nil || validateID(runID, "wf_") != nil || validateID(agentID, "") != nil {
			return nil, time.Time{}, false, failure("invalid-id")
		}
		events = append(events, journalEvent{kind: kind, workflowID: workflowID, runID: runID, agentID: agentID, key: key, at: at, timeKnown: timeKnown})
	}
	return events, stableInfo.ModTime(), true, nil
}

func journalFacts(events []journalEvent, workflowID, runID, agentID string) (started, result bool, startedAt, resultAt time.Time, timesKnown bool) {
	timesKnown = true
	var startedKey, resultKey string
	startedCount, resultCount := 0, 0
	for _, event := range events {
		if event.workflowID != workflowID || event.runID != runID || event.agentID != agentID {
			continue
		}
		switch event.kind {
		case "started":
			startedCount++
			startedKey = event.key
			if event.at.After(startedAt) {
				startedAt = event.at
			}
		case "result":
			resultCount++
			resultKey = event.key
			if event.at.After(resultAt) {
				resultAt = event.at
			}
		}
		timesKnown = timesKnown && event.timeKnown
	}
	started = startedCount == 1
	result = started && resultCount == 1 && resultKey == startedKey
	if startedCount > 1 || resultCount > 1 || (resultCount == 1 && (!started || resultKey != startedKey)) ||
		(started && result && !startedAt.IsZero() && !resultAt.IsZero() && resultAt.Before(startedAt)) {
		timesKnown = false
	}
	return
}

func resumeExists(sessionPath, agentID string) (bool, time.Time, error) {
	if err := validateID(agentID, ""); err != nil {
		return false, time.Time{}, failure("invalid-id")
	}
	path := filepath.Join(sessionPath, "subagents", "agent-"+agentID+".jsonl")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, time.Time{}, nil
	}
	if err != nil || !safeMetadataInfo(path, info, false) {
		return false, time.Time{}, failure("unsafe-metadata")
	}
	return true, info.ModTime(), nil
}

func lastActivity(now time.Time, values []time.Time, known bool) (time.Time, bool) {
	var last time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		value = value.UTC()
		if value.After(now.UTC()) {
			known = false
		}
		if value.After(last) {
			last = value
		}
	}
	if last.IsZero() {
		known = false
	}
	return last, known
}

func (a *Adapter) readStable(path string, limit int64) ([]byte, fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, failure("unsafe-metadata")
	}
	return a.readStableWithInfo(path, info, limit)
}

func (a *Adapter) readStableWithInfo(path string, before fs.FileInfo, limit int64) ([]byte, fs.FileInfo, error) {
	if !safeMetadataInfo(path, before, false) {
		return nil, nil, failure("unsafe-metadata")
	}
	first, opened, err := readBounded(path, limit)
	if err != nil {
		return nil, nil, err
	}
	if a.afterRead != nil {
		a.afterRead(path)
	}
	after, err := os.Lstat(path)
	if err != nil || !safeMetadataInfo(path, after, false) || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		!os.SameFile(before, opened) {
		return nil, nil, failure("metadata-mutated")
	}
	second, reopened, err := readBounded(path, limit)
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(before, reopened) || !bytes.Equal(first, second) {
		return nil, nil, failure("metadata-mutated")
	}
	return first, after, nil
}

func readBounded(path string, limit int64) ([]byte, fs.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, failure("unsafe-metadata")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		if err == nil && info.Size() > limit {
			return nil, nil, failure("metadata-bound")
		}
		return nil, nil, failure("unsafe-metadata")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, nil, failure("unsafe-metadata")
	}
	if int64(len(data)) > limit {
		return nil, nil, failure("metadata-bound")
	}
	return data, info, nil
}

// readDirectory returns a stable, sorted snapshot without ever asking the OS
// for an unbounded entry list. Every entry consumes the per-directory budget,
// including unknown add-only names that callers later ignore.
func (a *Adapter) readDirectory(path string, maximum int) ([]fs.DirEntry, error) {
	if maximum < 0 {
		return nil, failure("metadata-bound")
	}
	before, err := os.Lstat(path)
	if err != nil || !safeMetadataInfo(path, before, true) {
		return nil, failure("unsafe-metadata")
	}
	first, opened, err := readDirectorySnapshot(path, maximum)
	if err != nil {
		return nil, err
	}
	if a.afterRead != nil {
		a.afterRead(path)
	}
	after, err := os.Lstat(path)
	if err != nil || !stableDirectoryInfo(path, before, opened, after) {
		return nil, failure("metadata-mutated")
	}
	second, reopened, err := readDirectorySnapshot(path, maximum)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, reopened) || !directoryEntriesEqual(first, second) {
		return nil, failure("metadata-mutated")
	}
	for _, entry := range second {
		if err := pathx.ValidateComponent(entry.Name()); err != nil {
			return nil, failure("invalid-id")
		}
		child := filepath.Join(path, entry.Name())
		info, err := os.Lstat(child)
		if err != nil || !safeMetadataInfo(child, info, info.IsDir()) {
			return nil, failure("unsafe-metadata")
		}
	}
	final, err := os.Lstat(path)
	if err != nil || !stableDirectoryInfo(path, before, reopened, final) {
		return nil, failure("metadata-mutated")
	}
	third, finalOpened, err := readDirectorySnapshot(path, maximum)
	if err != nil {
		return nil, err
	}
	if !stableDirectoryInfo(path, before, finalOpened, finalOpened) || !directoryEntriesEqual(second, third) {
		return nil, failure("metadata-mutated")
	}
	return third, nil
}

func readDirectorySnapshot(path string, maximum int) ([]fs.DirEntry, fs.FileInfo, error) {
	readLimit, err := boundPlusOne(maximum)
	if err != nil {
		return nil, nil, err
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, nil, failure("unsafe-metadata")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !safeMetadataInfo(path, info, true) {
		return nil, nil, failure("unsafe-metadata")
	}
	entries, readErr := directory.ReadDir(readLimit)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, nil, failure("unsafe-metadata")
	}
	if len(entries) > maximum {
		return nil, nil, failure("metadata-bound")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, info, nil
}

func boundPlusOne(value int) (int, error) {
	if value < 0 || value == int(^uint(0)>>1) {
		return 0, failure("metadata-bound")
	}
	return value + 1, nil
}

func stableDirectoryInfo(path string, expected, opened, current fs.FileInfo) bool {
	return safeMetadataInfo(path, current, true) && os.SameFile(expected, opened) && os.SameFile(expected, current) &&
		expected.Size() == current.Size() && expected.ModTime().Equal(current.ModTime())
}

func directoryEntriesEqual(left, right []fs.DirEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name() != right[i].Name() || left[i].Type() != right[i].Type() || left[i].IsDir() != right[i].IsDir() {
			return false
		}
	}
	return true
}

func (a *Adapter) directories(path string, total *int, maximum int) ([]string, error) {
	entries, err := a.readDirectory(path, maximum)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, failure("unsafe-metadata")
			}
			continue
		}
		name := entry.Name()
		if err := pathx.ValidateComponent(name); err != nil {
			return nil, failure("invalid-id")
		}
		child, err := fixedChild(path, name)
		if err != nil {
			return nil, failure("invalid-id")
		}
		info, err := os.Lstat(child)
		if err != nil || !safeMetadataInfo(child, info, true) {
			return nil, failure("unsafe-metadata")
		}
		*total++
		if *total > maximum {
			return nil, failure("metadata-bound")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func safeOptionalDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !safeMetadataInfo(path, info, true) {
		return false, failure("unsafe-metadata")
	}
	return true, nil
}

func safeFixedDirectoryChain(root string, components ...string) (bool, error) {
	current := root
	for _, component := range components {
		if err := pathx.ValidateComponent(component); err != nil {
			return false, failure("invalid-id")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil || !safeMetadataInfo(current, info, true) {
			return false, failure("unsafe-metadata")
		}
	}
	return true, nil
}

func fixedChild(root, component string) (string, error) {
	if err := pathx.ValidateComponent(component); err != nil {
		return "", err
	}
	return filepath.Join(root, component), nil
}

func validateID(id, prefix string) error {
	if err := pathx.ValidateComponent(id); err != nil {
		return err
	}
	if prefix != "" && !strings.HasPrefix(id, prefix) {
		return fmt.Errorf("id lacks prefix")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			continue
		}
		return fmt.Errorf("id contains unsupported character")
	}
	return nil
}

func validateWorktreePath(query ephemeral.SourceQuery, raw string) (string, bool, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", false, failure("invalid-worktree-path")
	}
	canonical, err := pathx.Canonical(raw)
	if err != nil {
		return "", false, failure("invalid-worktree-path")
	}
	insideRepository, err := pathx.Contains(query.Repository.Root, canonical)
	if err != nil {
		return "", false, failure("invalid-worktree-path")
	}
	if !insideRepository {
		return canonical, false, nil
	}
	harnessRoot := filepath.Join(query.Repository.Root, ".claude", "worktrees")
	insideHarness, err := pathx.IsChild(harnessRoot, canonical)
	if err != nil || !insideHarness {
		return "", false, failure("invalid-worktree-path")
	}
	for _, target := range query.Targets {
		candidate, candidateErr := pathx.Canonical(target.Path)
		if candidateErr == nil && candidate == canonical {
			return canonical, true, nil
		}
	}
	return canonical, true, nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("decode object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode object key")
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("decode object key")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("duplicate JSON field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode object value")
		}
		object[key] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("decode object close")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("extra JSON value")
	}
	return object, nil
}

func requiredString(object map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := object[key]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false
	}
	return value, true
}

func oneString(object map[string]json.RawMessage, keys ...string) (string, bool) {
	value := ""
	found := false
	for _, key := range keys {
		if _, present := object[key]; !present {
			continue
		}
		candidate, ok := requiredString(object, key)
		if !ok || (found && candidate != value) {
			return "", false
		}
		value, found = candidate, true
	}
	return value, found
}

func requiredBool(object map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := object[key]
	if !ok {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func requiredTimestamp(object map[string]json.RawMessage, key string) (time.Time, bool) {
	value, ok := requiredString(object, key)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func optionalTimestamp(object map[string]json.RawMessage, key string) (time.Time, bool) {
	if _, present := object[key]; !present {
		return time.Time{}, true
	}
	return requiredTimestamp(object, key)
}

func normalizeWorkflowState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed":
		return "completed"
	case "killed":
		return "killed"
	case "progress", "running", "in_progress", "in-progress", "pending":
		return "progress"
	default:
		return "unknown"
	}
}

func normalizeAgentState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "done":
		return "done"
	case "progress", "running", "in_progress", "in-progress", "pending", "working":
		return "progress"
	default:
		return "unknown"
	}
}

func (a *Adapter) rejectDuplicateClaims(result *ephemeral.SourceResult) {
	byPath := make(map[string][]int)
	byIdentity := make(map[string][]int)
	for i, claim := range result.Claims {
		byPath[claim.WorktreePath] = append(byPath[claim.WorktreePath], i)
		identity := strings.Join([]string{claim.Provider, claim.SessionID, claim.RunID, claim.AgentID}, "\x00")
		byIdentity[identity] = append(byIdentity[identity], i)
	}
	mark := func(groups map[string][]int, code, message string) {
		for _, indices := range groups {
			if len(indices) < 2 {
				continue
			}
			for _, index := range indices {
				result.Claims[index].Unique = ephemeral.KnownFact(false)
			}
			result.Diagnostics = append(result.Diagnostics, ephemeral.Diagnostic{
				Source: providerName, Code: code, Message: message,
			})
		}
	}
	mark(byPath, "duplicate-worktree-claim", "multiple provider claims name one worktree path")
	mark(byIdentity, "duplicate-provider-identity", "one provider run and agent identity names multiple worktree paths")
}

func failureCode(err error) string {
	var typed *scanFailure
	if errors.As(err, &typed) {
		return typed.code
	}
	return "unsafe-metadata"
}
