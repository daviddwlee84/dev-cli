// Package artifact finalizes agent transcripts only after their writer exits.
package artifact

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

type Status string

const (
	Armed      Status = "armed"
	Finalizing Status = "finalizing"
	Finalized  Status = "finalized"
	Failed     Status = "failed"
)

// Intent is a durable handoff from an active agent to an external finalizer.
type Intent struct {
	SchemaVersion      int      `json:"schema_version"`
	ID                 string   `json:"id"`
	RunID              string   `json:"run_id"`
	Provider           string   `json:"provider"`
	SessionID          string   `json:"session_id"`
	TaskID             string   `json:"task_id,omitempty"`
	RepoPath           string   `json:"repo_path"`
	GitCommonDir       string   `json:"git_common_dir"`
	WorktreePath       string   `json:"worktree_path"`
	Branch             string   `json:"branch"`
	Base               string   `json:"base,omitempty"`
	Head               string   `json:"head"`
	PlanPaths          []string `json:"plan_paths,omitempty"`
	UnrelatedArtifacts []string `json:"unrelated_artifacts,omitempty"`
	AllowLarge         bool     `json:"allow_large,omitempty"`

	Status         Status    `json:"status"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	ArtifactCommit string    `json:"artifact_commit,omitempty"`
	FailureCode    string    `json:"failure_code,omitempty"`
	SessionEndedAt time.Time `json:"session_ended_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

var (
	idPattern      = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	sessionPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+:[0-9a-fA-F-]{16,}$`)
)

func (i Intent) Validate() error {
	switch {
	case i.SchemaVersion != SchemaVersion:
		return fmt.Errorf("unsupported artifact intent schema %d", i.SchemaVersion)
	case !idPattern.MatchString(i.ID):
		return fmt.Errorf("invalid artifact intent id %q", i.ID)
	case i.RunID == "" || !idPattern.MatchString(i.RunID):
		return fmt.Errorf("invalid artifact run id %q", i.RunID)
	case i.Provider == "" || i.SessionID == "":
		return fmt.Errorf("artifact intent needs provider and session id")
	case i.RepoPath == "" || i.WorktreePath == "" || i.GitCommonDir == "":
		return fmt.Errorf("artifact intent needs repository, worktree and git common-dir paths")
	case i.Branch == "" || i.Head == "":
		return fmt.Errorf("artifact intent needs branch and head")
	}
	if !sessionPattern.MatchString(i.Provider + ":" + i.SessionID) {
		return fmt.Errorf("invalid agent session %q", i.Provider+":"+i.SessionID)
	}
	switch i.Status {
	case Armed, Finalizing, Finalized, Failed:
		return nil
	default:
		return fmt.Errorf("invalid artifact intent status %q", i.Status)
	}
}

func ParseSession(value string) (provider, sessionID string, err error) {
	provider, sessionID, ok := strings.Cut(strings.TrimSpace(value), ":")
	provider = strings.ToLower(provider)
	if !ok || !sessionPattern.MatchString(provider+":"+sessionID) {
		return "", "", fmt.Errorf("invalid session %q: want provider:uuid", value)
	}
	return provider, sessionID, nil
}
