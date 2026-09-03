package agentskill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const maxLockBytes int64 = 4 << 20

type lockDocument struct {
	Path        string
	Scope       Scope
	Version     int
	Entries     map[string]LockMetadata
	Diagnostics []Diagnostic
	normalized  map[string][]string
}

type rawLockFile struct {
	Version *int            `json:"version"`
	Skills  json.RawMessage `json:"skills"`
}

type rawLockEntry struct {
	Source          string   `json:"source"`
	SourceURL       string   `json:"sourceUrl"`
	SourceType      string   `json:"sourceType"`
	Ref             string   `json:"ref"`
	SkillPath       string   `json:"skillPath"`
	ComputedHash    string   `json:"computedHash"`
	ContentHash     string   `json:"contentHash"`
	SkillFolderHash string   `json:"skillFolderHash"`
	InstalledAt     string   `json:"installedAt"`
	UpdatedAt       string   `json:"updatedAt"`
	PluginName      string   `json:"pluginName"`
	SourceBaseURL   string   `json:"sourceBaseUrl"`
	WellKnownDigest string   `json:"wellKnownDigest"`
	Subagents       []string `json:"subagents"`
}

func readProjectLock(filename string) lockDocument {
	return readLockDocument(filename, ScopeProject, 1, 1)
}

func readGlobalLock(filename string) lockDocument {
	return readLockDocument(filename, ScopeGlobal, 1, 3)
}

func readLockDocument(filename string, scope Scope, minimumVersion, maximumVersion int) lockDocument {
	document := lockDocument{Path: filename, Scope: scope, Entries: map[string]LockMetadata{}}
	data, err := safefile.ReadRegular(context.Background(), filename, maxLockBytes)
	if errors.Is(err, os.ErrNotExist) {
		return document
	}
	if err != nil {
		kind := DiagnosticLockUnreadable
		message := fmt.Sprintf("could not read lock file: %v", err)
		if errors.Is(err, safefile.ErrTooLarge) {
			kind = DiagnosticLockOversized
			message = fmt.Sprintf("lock file exceeds %d bytes", maxLockBytes)
		} else if errors.Is(err, safefile.ErrNotRegular) {
			message = "lock path is not a regular file"
		}
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(kind, scope, filename, message))
		return document
	}

	var raw rawLockFile
	if err := json.Unmarshal(data, &raw); err != nil {
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(
			DiagnosticLockMalformed, scope, filename, fmt.Sprintf("invalid lock JSON: %v", err)))
		return document
	}
	if raw.Version == nil {
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(
			DiagnosticLockMalformed, scope, filename, "lock file has no integer version"))
		return document
	}
	document.Version = *raw.Version
	if document.Version < minimumVersion || document.Version > maximumVersion {
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(
			DiagnosticLockUnsupported, scope, filename,
			fmt.Sprintf("lock version %d is unsupported (want %d through %d)", document.Version, minimumVersion, maximumVersion)))
		return document
	}
	if len(raw.Skills) == 0 || string(raw.Skills) == "null" {
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(
			DiagnosticLockMalformed, scope, filename, "lock file has no skills object"))
		return document
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw.Skills, &entries); err != nil || entries == nil {
		document.Diagnostics = append(document.Diagnostics, lockDiagnostic(
			DiagnosticLockMalformed, scope, filename, "lock file skills must be an object"))
		return document
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !safeDisplayValue(name) || strings.HasPrefix(name, "-") {
			document.Diagnostics = append(document.Diagnostics, Diagnostic{
				Kind: DiagnosticLockMalformed, Scope: scope, Path: filename,
				Message: "lock entry has an unsafe display name",
			})
			continue
		}
		normalized := normalizedName(name)
		if normalized == "" {
			document.Diagnostics = append(document.Diagnostics, Diagnostic{
				Kind: DiagnosticNameCollision, Scope: scope, Path: filename, Name: name,
				Message: fmt.Sprintf("lock name %q has no usable normalized name", name),
			})
			continue
		}
		rawEntry := bytes.TrimSpace(entries[name])
		var entry rawLockEntry
		if len(rawEntry) == 0 || rawEntry[0] != '{' || json.Unmarshal(rawEntry, &entry) != nil {
			document.Diagnostics = append(document.Diagnostics, Diagnostic{
				Kind: DiagnosticLockMalformed, Scope: scope, Path: filename, Name: name,
				Message: fmt.Sprintf("lock entry %q is not an object", name),
			})
			continue
		}
		source := entry.Source
		if entry.SourceType == "local" && source != "" && !filepath.IsAbs(source) {
			source = filepath.Clean(filepath.Join(filepath.Dir(filename), source))
		}
		metadata := LockMetadata{
			Name: name, NormalizedName: normalized, File: filename,
			Version: document.Version, Scope: scope, Source: source,
			SourceURL: entry.SourceURL, SourceType: entry.SourceType, Ref: entry.Ref,
			SkillPath: entry.SkillPath, ComputedHash: entry.ComputedHash,
			ContentHash: entry.ContentHash, SkillFolderHash: entry.SkillFolderHash,
			InstalledAt: entry.InstalledAt, UpdatedAt: entry.UpdatedAt,
			PluginName: entry.PluginName, SourceBaseURL: entry.SourceBaseURL,
			WellKnownDigest: entry.WellKnownDigest,
			Subagents:       append([]string(nil), entry.Subagents...),
		}
		normalizeRecordedHash(&metadata)
		document.Entries[name] = metadata
	}
	document.normalized = document.buildNamesByNormalized()
	document.Diagnostics = append(document.Diagnostics, document.collisionDiagnostics()...)
	return document
}

func lockDiagnostic(kind DiagnosticKind, scope Scope, filename, message string) Diagnostic {
	return Diagnostic{Kind: kind, Scope: scope, Path: filename, Message: message}
}

func (document lockDocument) buildNamesByNormalized() map[string][]string {
	groups := map[string][]string{}
	for name, entry := range document.Entries {
		normalized := entry.NormalizedName
		if normalized == "" {
			normalized = normalizedName(name)
		}
		groups[normalized] = append(groups[normalized], name)
	}
	for normalized := range groups {
		sort.Strings(groups[normalized])
	}
	return groups
}

func (document lockDocument) namesByNormalized() map[string][]string {
	if document.normalized != nil {
		return document.normalized
	}
	return document.buildNamesByNormalized()
}

func (document lockDocument) collisionDiagnostics() []Diagnostic {
	var diagnostics []Diagnostic
	groups := document.namesByNormalized()
	keys := make([]string, 0, len(groups))
	for normalized := range groups {
		keys = append(keys, normalized)
	}
	sort.Strings(keys)
	for _, normalized := range keys {
		names := groups[normalized]
		if normalized != "" && len(names) < 2 {
			continue
		}
		message := fmt.Sprintf("lock names %s normalize to %q", strings.Join(names, ", "), normalized)
		if normalized == "" {
			message = fmt.Sprintf("lock name %s has no usable normalized name", strings.Join(names, ", "))
		}
		diagnostics = append(diagnostics, Diagnostic{
			Kind: DiagnosticNameCollision, Scope: document.Scope, Path: document.Path,
			Name: normalized, Message: message,
		})
	}
	return diagnostics
}

// find applies deterministic lock matching: exact spelling wins; a normalized
// spelling is accepted only when it identifies one lock entry.
func (document lockDocument) find(name string) (LockMetadata, bool) {
	if entry, ok := document.Entries[name]; ok {
		return entry, true
	}
	names := document.namesByNormalized()[normalizedName(name)]
	if len(names) != 1 {
		return LockMetadata{}, false
	}
	entry, ok := document.Entries[names[0]]
	return entry, ok
}

func (document lockDocument) candidates(normalized string) []LockMetadata {
	names := document.namesByNormalized()[normalized]
	entries := make([]LockMetadata, 0, len(names))
	for _, name := range names {
		entries = append(entries, document.Entries[name])
	}
	return entries
}

func globalLockPath() string {
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); filepath.IsAbs(stateHome) {
		return filepath.Join(filepath.Clean(stateHome), "skills", ".skill-lock.json")
	}
	home := homeDirectory()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agents", ".skill-lock.json")
}

// GlobalLockPath returns the native global lock location. Relative
// XDG_STATE_HOME values are ignored as required by the XDG specification.
func GlobalLockPath() string { return globalLockPath() }

func normalizedName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_'
		if valid {
			builder.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
