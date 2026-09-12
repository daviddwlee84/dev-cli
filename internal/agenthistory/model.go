// Package agenthistory preserves selected conversation evidence independently
// of source commits. Native writers, Git remotes and shared policies retain
// their own authority; only explicit guarded plans change them.
package agenthistory

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	"github.com/google/uuid"
)

const SchemaVersion = 1
const MaxFiles = 256
const MaxFileBytes = 128 << 20
const maxRecordBytes = 8 << 20

var ErrStale = errors.New("artifact inputs changed; create a new preview")
var component = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,95}$`)

type Policy struct {
	Version      int      `toml:"version" json:"version"`
	ProjectID    string   `toml:"project_id" json:"project_id"`
	Mode         string   `toml:"mode" json:"mode"`
	Source       string   `toml:"source" json:"source"`
	Capture      string   `toml:"capture" json:"capture"`
	Paths        []string `toml:"paths" json:"paths"`
	ExportIgnore bool     `toml:"export_ignore" json:"export_ignore"`
}

// Binding is private host-local policy, never copied into the source tree.
type Binding struct {
	Version    int    `json:"version"`
	ProjectID  string `json:"project_id"`
	Archive    string `json:"archive"`
	Protection string `json:"protection"`
	CaptureDir string `json:"capture_dir,omitempty"`
}

type Options struct {
	Root, StateDir, GlobalHygiene string
	Engine                        hygiene.Engine
}

type SetupOptions struct {
	Mode, Source, Capture, Archive, Protection string
	Paths                                      []string
	ExportIgnore                               bool
}

type ArchiveOptions struct {
	Session string
	Files   []string
	Timeout time.Duration
}

type MigrationOptions struct {
	Mode   string
	Paths  []string
	Output string
}

type ApplyOptions struct {
	WriterStopped bool
	Guard         func(context.Context, string, []string) error
}

type FileSummary struct {
	File         string `json:"file"`
	Bytes        int64  `json:"bytes"`
	Digest       string `json:"digest"`
	Replacements int    `json:"replacements,omitempty"`
	Cached       bool   `json:"cached,omitempty"`
	Scan         string `json:"scan,omitempty"`
}

type Plan struct {
	SchemaVersion         int              `json:"schema_version"`
	ID                    string           `json:"id"`
	Kind                  string           `json:"kind"`
	Status                string           `json:"status"`
	ProjectID             string           `json:"project_id,omitempty"`
	Source                string           `json:"source,omitempty"`
	Protection            string           `json:"protection,omitempty"`
	Archive               string           `json:"archive,omitempty"`
	Remote                string           `json:"remote,omitempty"`
	Files                 []FileSummary    `json:"files"`
	Reports               []hygiene.Report `json:"reports,omitempty"`
	RequiresWriterStopped bool             `json:"requires_writer_stopped"`
	Notices               []string         `json:"notices,omitempty"`
	Completed             []string         `json:"completed,omitempty"`
	Recovery              []string         `json:"recovery,omitempty"`
	ArchiveCommit         string           `json:"archive_commit,omitempty"`
	Output                string           `json:"output,omitempty"`
	ReviewDir             string           `json:"review_dir,omitempty"`
}

type Status struct {
	SchemaVersion int      `json:"schema_version"`
	Configured    bool     `json:"configured"`
	Policy        Policy   `json:"policy"`
	Binding       *Binding `json:"binding,omitempty"`
	TrackedFiles  int      `json:"tracked_files"`
	PendingFiles  []string `json:"pending_files"`
	Notices       []string `json:"notices,omitempty"`
}

type fileChange struct{ Path, Token, Desired, Digest string }
type snapshot struct {
	Path, Logical, Token, Digest, DesiredDigest, Payload string
	Provider, SessionID                                  string
	ScanInputs                                           string
}
type planRecord struct {
	CaptureToken                                       string
	View                                               Plan
	Root, Common, RootToken, PolicyToken, BindingToken string
	ArchiveToken, ArchiveHead, ArchiveIndex            string
	Changes                                            []fileChange
	Snapshots                                          []snapshot
	Policy                                             Policy
	Binding                                            Binding
	Refs                                               map[string]string
	MigrationPaths                                     []string
	OutputAnchor, FilterExecutable, FilterDigest       string
	RemoteURL, RemoteName, RemoteBranch, RemoteOID     string
	AttributesToken                                    string
	Signature                                          string
}

// Record is portable evidence stored with the immutable snapshot in the archive.
// Paths are source-relative labels. Native machine paths belong only in private
// plans and receipts, not in this cross-machine record.
type Record struct {
	Version        int       `json:"version"`
	ProjectID      string    `json:"project_id"`
	Source         string    `json:"source"`
	Provider       string    `json:"provider,omitempty"`
	SessionID      string    `json:"session_id"`
	SourcePath     string    `json:"source_path"`
	SourceCommit   string    `json:"source_commit"`
	Content        string    `json:"content"`
	Digest         string    `json:"digest"`
	OriginalDigest string    `json:"original_digest"`
	Protection     string    `json:"protection"`
	Scan           string    `json:"scan"`
	Created        time.Time `json:"created"`
}

type Match struct {
	Record Record `json:"record"`
	Path   string `json:"path"`
	Lines  []int  `json:"lines,omitempty"`
}

func decodePolicy(data []byte) (Policy, error) {
	var p Policy
	meta, err := toml.Decode(string(data), &p)
	if err != nil || len(meta.Undecoded()) != 0 {
		return p, errors.New("invalid artifact policy TOML")
	}
	return p, p.validate()
}
func (p Policy) validate() error {
	if p.Version != 1 || !validID(p.ProjectID) {
		return errors.New("unsupported artifact policy or invalid project ID")
	}
	if p.Mode != "track" && p.Mode != "archive" && p.Mode != "unmanaged" {
		return errors.New("mode must be track, archive or unmanaged")
	}
	if p.Source != "specstory" && p.Source != "files" {
		return errors.New("source must be specstory or files")
	}
	if p.Capture != "project" && p.Capture != "external" {
		return errors.New("capture must be project or external")
	}
	if p.Capture == "external" && (p.Source != "specstory" || p.Mode != "archive") {
		return errors.New("external capture requires SpecStory archive mode")
	}
	if p.Capture == "external" && (len(p.Paths) != 1 || p.Paths[0] != ".specstory/history") {
		return errors.New("external SpecStory capture uses the .specstory/history logical scope")
	}
	if (len(p.Paths) == 0 && p.Mode != "unmanaged") || len(p.Paths) > MaxFiles {
		return errors.New("select 1 to 256 artifact paths")
	}
	seen := map[string]bool{}
	for _, path := range p.Paths {
		if !relative(path) || seen[path] {
			return errors.New("artifact paths must be unique clean relative paths")
		}
		seen[path] = true
	}
	return nil
}
func validID(s string) bool { id, e := uuid.Parse(s); return e == nil && id.String() == s }
func relative(s string) bool {
	if s == "" || s == "." || filepath.IsAbs(s) || filepath.ToSlash(filepath.Clean(s)) != s || strings.HasPrefix(s, "../") || strings.ContainsAny(s, "\x00\r\n\\") {
		return false
	}
	for _, c := range strings.Split(s, "/") {
		if strings.EqualFold(c, ".git") || strings.Contains(c, ":") {
			return false
		}
	}
	return true
}
func encode(v any) []byte { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
