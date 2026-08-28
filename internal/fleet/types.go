package fleet

import (
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

const SnapshotSchemaVersion = 1

type TaskCounts struct {
	Hot  int `json:"hot"`
	Warm int `json:"warm"`
	Cold int `json:"cold"`
	Done int `json:"done"`
}

func (c TaskCounts) Total() int { return c.Hot + c.Warm + c.Cold + c.Done }

type RepoSnapshot struct {
	Name             string                `json:"name"`
	Display          string                `json:"display"`
	Category         string                `json:"category,omitempty"`
	Path             string                `json:"path"`
	RealPath         string                `json:"real_path,omitempty"`
	RemoteIdentities []string              `json:"remote_identities,omitempty"`
	Branch           string                `json:"branch,omitempty"`
	Status           gitx.Status           `json:"status"`
	LastActivity     time.Time             `json:"last_activity,omitempty"`
	Worktrees        int                   `json:"worktrees"`
	Tasks            TaskCounts            `json:"tasks"`
	Live             bool                  `json:"live"`
	Runtime          string                `json:"runtime,omitempty"`
	RuntimeHandle    string                `json:"runtime_handle,omitempty"`
	AgentStatus      string                `json:"agent_status,omitempty"`
	Topology         gitx.RecoveryTopology `json:"topology"`
}

type Snapshot struct {
	SchemaVersion int            `json:"schema_version"`
	Host          string         `json:"host"`
	DevVersion    string         `json:"dev_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Runtime       string         `json:"runtime"`
	Repositories  []RepoSnapshot `json:"repositories"`
}

type HostState string

const (
	HostOK           HostState = "ok"
	HostStale        HostState = "stale"
	HostNoDev        HostState = "no-dev"
	HostUnreachable  HostState = "unreachable"
	HostTimeout      HostState = "timeout"
	HostIncompatible HostState = "incompatible"
	HostInvalid      HostState = "invalid-response"
)

type HostResult struct {
	Name         string     `json:"name"`
	Local        bool       `json:"local"`
	State        HostState  `json:"state"`
	Snapshot     *Snapshot  `json:"snapshot,omitempty"`
	CachedAt     *time.Time `json:"cached_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	FromCache    bool       `json:"from_cache"`
	PasswordAuth bool       `json:"password_auth,omitempty"`
	EndpointID   string     `json:"-"`
}

type OpenRequest struct {
	RemoteIdentity string `json:"remote_identity,omitempty"`
	Path           string `json:"path,omitempty"`
	Name           string `json:"name,omitempty"`
}

func (r HostResult) StrictFailure() bool {
	switch r.State {
	case HostOK, HostNoDev:
		return false
	case HostStale:
		return r.Error != ""
	default:
		return true
	}
}
