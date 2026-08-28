package note

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const indexSchemaVersion = 1

var ErrIndexSchema = fmt.Errorf("incompatible notes index schema")

// Index is a disposable FTS5 database over durable Markdown notes.
type Index struct {
	Path string
	db   *sql.DB
}

func OpenIndex(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	i := &Index{Path: path, db: db}
	if err := i.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The index contains full note bodies, including private thoughts.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return i, nil
}

func (i *Index) Close() error { return i.db.Close() }

func (i *Index) migrate() error {
	var version int
	if err := i.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > indexSchemaVersion {
		return fmt.Errorf("%w: database version %d is newer than supported %d",
			ErrIndexSchema, version, indexSchemaVersion)
	}
	if _, err := i.db.Exec(`
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
  id UNINDEXED,
  repository_id UNINDEXED,
  repository,
  tags,
  body,
  tokenize = 'unicode61'
);
CREATE TABLE IF NOT EXISTS note_manifest (
  id TEXT PRIMARY KEY,
  updated TEXT NOT NULL
);`); err != nil {
		return err
	}
	var definition string
	if err := i.db.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'notes_fts'").Scan(&definition); err != nil {
		return fmt.Errorf("%w: notes_fts missing: %v", ErrIndexSchema, err)
	}
	upper := strings.ToUpper(definition)
	if !strings.Contains(upper, "VIRTUAL TABLE") || !strings.Contains(strings.ToLower(definition), "fts5") {
		return fmt.Errorf("%w: notes_fts is not an FTS5 virtual table", ErrIndexSchema)
	}
	if err := validateColumns(i.db, "notes_fts", []string{"id", "repository_id", "repository", "tags", "body"}); err != nil {
		return err
	}
	if err := validateColumns(i.db, "note_manifest", []string{"id", "updated"}); err != nil {
		return err
	}
	_, err := i.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", indexSchemaVersion))
	return err
}

func validateColumns(db *sql.DB, table string, expected []string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("%w: %s columns %v, want %v", ErrIndexSchema, table, actual, expected)
	}
	return nil
}

// Hit is one ranked search match.
type Hit struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id"`
	Repository   string `json:"repository"`
	Snippet      string `json:"snippet"`
}

// Rebuild replaces the complete derived index in one transaction.
func (i *Index) Rebuild(notes []*Note) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM notes_fts"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM note_manifest"); err != nil {
		return err
	}
	fts, err := tx.Prepare("INSERT INTO notes_fts (id, repository_id, repository, tags, body) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer fts.Close()
	manifest, err := tx.Prepare("INSERT INTO note_manifest (id, updated) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer manifest.Close()
	for _, n := range notes {
		if _, err := fts.Exec(n.ID, n.RepositoryID, n.Repository, strings.Join(n.Tags, " "), n.Body); err != nil {
			return err
		}
		if _, err := manifest.Exec(n.ID, fingerprint(n)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Upsert incrementally updates one note after a create/edit.
func (i *Index) Upsert(n *Note) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM notes_fts WHERE id = ?", n.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO notes_fts
(id, repository_id, repository, tags, body) VALUES (?, ?, ?, ?, ?)`,
		n.ID, n.RepositoryID, n.Repository, strings.Join(n.Tags, " "), n.Body); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO note_manifest (id, updated) VALUES (?, ?)
ON CONFLICT(id) DO UPDATE SET updated = excluded.updated`, n.ID, fingerprint(n)); err != nil {
		return err
	}
	return tx.Commit()
}

func (i *Index) Delete(id string) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM notes_fts WHERE id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM note_manifest WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// Current reports whether the manifest exactly matches the durable notes.
func (i *Index) Current(notes []*Note) bool {
	rows, err := i.db.Query("SELECT id, updated FROM note_manifest")
	if err != nil {
		return false
	}
	defer rows.Close()
	manifest := map[string]string{}
	for rows.Next() {
		var id, updated string
		if rows.Scan(&id, &updated) != nil {
			return false
		}
		manifest[id] = updated
	}
	if len(manifest) != len(notes) {
		return false
	}
	for _, n := range notes {
		if manifest[n.ID] != fingerprint(n) {
			return false
		}
	}
	return true
}

// Ensure rebuilds only when source and manifest differ.
func (i *Index) Ensure(notes []*Note) error {
	if i.Current(notes) {
		return nil
	}
	return i.Rebuild(notes)
}

// Search performs term-wise prefix search. Literal quoting prevents punctuation
// in a quick thought from becoming accidental FTS syntax.
func (i *Index) Search(query, repositoryID string, limit int) ([]Hit, error) {
	rawQuery := strings.TrimSpace(query)
	if rawQuery == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	// unicode61 does not segment CJK words: a body token "記得改善" cannot
	// match the substring "改善". Use term-wise literal LIKE for non-ASCII
	// input while retaining FTS ranking/snippets for Latin-script searches.
	if containsNonASCII(rawQuery) {
		return i.searchLiteral(rawQuery, repositoryID, limit)
	}
	query = ftsQuery(rawQuery)
	if query == "" {
		return nil, nil
	}
	statement := `SELECT id, repository_id, repository,
  snippet(notes_fts, 4, '[', ']', '…', 16)
FROM notes_fts WHERE notes_fts MATCH ?`
	args := []any{query}
	if repositoryID != "" {
		statement += " AND repository_id = ?"
		args = append(args, repositoryID)
	}
	statement += " ORDER BY rank LIMIT ?"
	args = append(args, limit)
	rows, err := i.db.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.RepositoryID, &h.Repository, &h.Snippet); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func containsNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func (i *Index) searchLiteral(query, repositoryID string, limit int) ([]Hit, error) {
	terms := strings.Fields(query)
	statement := `SELECT id, repository_id, repository, substr(body, 1, 180)
FROM notes_fts WHERE `
	var clauses []string
	var args []any
	for _, term := range terms {
		clauses = append(clauses, "(repository LIKE ? OR tags LIKE ? OR body LIKE ?)")
		pattern := "%" + term + "%"
		args = append(args, pattern, pattern, pattern)
	}
	statement += strings.Join(clauses, " AND ")
	if repositoryID != "" {
		statement += " AND repository_id = ?"
		args = append(args, repositoryID)
	}
	statement += " ORDER BY rowid DESC LIMIT ?"
	args = append(args, limit)
	rows, err := i.db.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("search notes literally: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.RepositoryID, &h.Repository, &h.Snippet); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func ftsQuery(query string) string {
	var terms []string
	for _, term := range strings.Fields(query) {
		term = strings.ReplaceAll(term, `"`, `""`)
		if term != "" {
			terms = append(terms, `"`+term+`"*`)
		}
	}
	return strings.Join(terms, " AND ")
}

func fingerprint(n *Note) string { return n.Revision() }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// SortHits provides a deterministic fallback for callers that combine result
// sets. FTS Search itself returns rank order.
func SortHits(hits []Hit) {
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Repository != hits[b].Repository {
			return hits[a].Repository < hits[b].Repository
		}
		return hits[a].ID < hits[b].ID
	})
}
