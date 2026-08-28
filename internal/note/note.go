// Package note stores append-only repository thoughts as ordinary Markdown
// files. SQLite indexes them for search, but the files are the durable source
// of truth: readable without dev, syncable without database merge conflicts,
// and recoverable after every cache is deleted.
package note

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

const CurrentSchemaVersion = 1

var (
	ErrNotFound   = errors.New("note not found")
	ErrConflict   = errors.New("note changed since it was opened")
	ErrInvalid    = errors.New("invalid note")
	ErrRepository = errors.New("invalid note repository")
)

// Note is one timestamped thought attached to a stable catalog asset ID.
type Note struct {
	SchemaVersion int       `toml:"schema_version" json:"schema_version"`
	ID            string    `toml:"id" json:"id"`
	RepositoryID  string    `toml:"repository_id" json:"repository_id"`
	Repository    string    `toml:"repository" json:"repository"`
	Created       time.Time `toml:"created" json:"created"`
	Updated       time.Time `toml:"updated" json:"updated"`
	Tags          []string  `toml:"tags" json:"tags"`

	Body string `toml:"-" json:"body"`
	Path string `toml:"-" json:"path,omitempty"`
}

func (n *Note) Normalize() {
	if n == nil {
		return
	}
	n.Repository = strings.TrimSpace(n.Repository)
	n.Body = normalizeBody(n.Body)
	n.Tags = catalog.NormalizeTags(n.Tags)
}

func (n Note) Validate() error {
	if n.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("note %s schema version %d: %w", n.ID, n.SchemaVersion, ErrInvalid)
	}
	if err := catalog.ValidateID(n.ID); err != nil {
		return fmt.Errorf("note ID: %w", err)
	}
	if err := catalog.ValidateID(n.RepositoryID); err != nil {
		return fmt.Errorf("repository ID: %w: %w", err, ErrRepository)
	}
	if strings.TrimSpace(n.Repository) == "" {
		return fmt.Errorf("note %s: repository name is required: %w", n.ID, ErrRepository)
	}
	if strings.TrimSpace(n.Body) == "" {
		return fmt.Errorf("note %s: body is empty: %w", n.ID, ErrInvalid)
	}
	if n.Created.IsZero() || n.Updated.IsZero() {
		return fmt.Errorf("note %s: created and updated are required: %w", n.ID, ErrInvalid)
	}
	if n.Updated.Before(n.Created) {
		return fmt.Errorf("note %s: updated precedes created: %w", n.ID, ErrInvalid)
	}
	return nil
}

// Revision fingerprints every field an editor is allowed to preserve or
// replace. Update compares it while holding the mutation lock, preventing a
// stale editor session from silently overwriting another process's changes.
func (n Note) Revision() string {
	payload := strings.Join([]string{
		n.ID, n.RepositoryID, n.Repository,
		n.Created.UTC().Format(time.RFC3339Nano),
		n.Updated.UTC().Format(time.RFC3339Nano),
		strings.Join(n.Tags, "\x00"), n.Body,
	}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

// Preview returns the first non-empty body line, collapsed and truncated.
func (n Note) Preview(limit int) string {
	line := ""
	for _, candidate := range strings.Split(n.Body, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.Join(strings.Fields(candidate), " ")
			break
		}
	}
	if limit <= 0 || len([]rune(line)) <= limit {
		return line
	}
	runes := []rune(line)
	return string(runes[:limit-1]) + "…"
}

func normalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return body + "\n"
}
