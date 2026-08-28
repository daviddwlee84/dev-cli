// Package stats records where development time actually goes, and renders it
// as a contribution-style heatmap.
//
// Two sources, because neither alone is honest. A sampler watches live agent
// and terminal sessions, which is the only way to count the hours spent
// reading and debugging rather than committing. Git history backfills the days
// before dev was installed and survives a lost database. WakaTime, when
// enabled, adds editor time dev cannot observe from the terminal.
package stats

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Source labels where a measurement came from, so double counting is visible
// rather than silent: the same hour can legitimately appear from the sampler
// and from WakaTime, and a query decides which to trust.
type Source string

const (
	// SourceSession is dev's own sampler watching live runtime sessions.
	SourceSession Source = "session"
	// SourceGit is derived from commit timestamps.
	SourceGit Source = "git"
	// SourceWakaTime is imported from the WakaTime API.
	SourceWakaTime Source = "wakatime"
)

// Store is the activity database.
type Store struct{ db *sql.DB }

// Open creates or opens the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// A short busy timeout matters: the sampler may run from cron while an
	// interactive `dev stats` is reading.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an existing stats database without creating directories,
// running migrations or otherwise changing durable observation data.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open stats read-only: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS activity (
  day     TEXT    NOT NULL,   -- local date, YYYY-MM-DD
  repo    TEXT    NOT NULL,
  branch  TEXT    NOT NULL DEFAULT '',
  source  TEXT    NOT NULL,
  seconds INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, repo, branch, source)
);
CREATE INDEX IF NOT EXISTS activity_day  ON activity(day);
CREATE INDEX IF NOT EXISTS activity_repo ON activity(repo);

-- Records the last successful run of each collector, so a backfill can pick up
-- where it left off instead of rescanning every repo's whole history.
CREATE TABLE IF NOT EXISTS collector (
  name    TEXT PRIMARY KEY,
  last_at INTEGER NOT NULL
);
`)
	return err
}

// Entry is one measurement.
type Entry struct {
	Day     time.Time
	Repo    string
	Branch  string
	Source  Source
	Seconds int
}

// Add accumulates seconds onto a (day, repo, branch, source) bucket.
func (s *Store) Add(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
INSERT INTO activity (day, repo, branch, source, seconds) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (day, repo, branch, source) DO UPDATE SET seconds = seconds + excluded.seconds`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if e.Seconds <= 0 || e.Repo == "" {
			continue
		}
		if _, err := stmt.Exec(e.Day.Format(dayFormat), e.Repo, e.Branch, string(e.Source), e.Seconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Set replaces a bucket rather than accumulating. Used by importers that are
// re-runnable: WakaTime reports the same day's total again on every import, so
// adding would inflate it.
func (s *Store) Set(entries ...Entry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
INSERT INTO activity (day, repo, branch, source, seconds) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (day, repo, branch, source) DO UPDATE SET seconds = excluded.seconds`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if e.Repo == "" {
			continue
		}
		if _, err := stmt.Exec(e.Day.Format(dayFormat), e.Repo, e.Branch, string(e.Source), e.Seconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const dayFormat = "2006-01-02"

// Query narrows a report.
type Query struct {
	Since time.Time
	Until time.Time
	Repo  string
	// ExactRepo uses equality rather than the report-friendly substring match.
	// Per-repo TUI panels set it so "api" does not include "my-api".
	ExactRepo bool
	// Sources limits which collectors count. Empty means all of them.
	Sources []Source
}

func (q Query) where() (string, []any) {
	clause := "WHERE day >= ? AND day <= ?"
	args := []any{q.Since.Format(dayFormat), q.Until.Format(dayFormat)}
	if q.Repo != "" {
		if q.ExactRepo {
			clause += " AND repo = ?"
			args = append(args, q.Repo)
		} else {
			clause += " AND repo LIKE ?"
			args = append(args, "%"+q.Repo+"%")
		}
	}
	if len(q.Sources) > 0 {
		clause += " AND source IN (" + placeholders(len(q.Sources)) + ")"
		for _, s := range q.Sources {
			args = append(args, string(s))
		}
	}
	return clause, args
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	out := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}

// DayTotals returns seconds per calendar day.
func (s *Store) DayTotals(q Query) (map[string]int, error) {
	clause, args := q.where()
	rows, err := s.db.Query(`SELECT day, SUM(seconds) FROM activity `+clause+` GROUP BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var day string
		var secs int
		if err := rows.Scan(&day, &secs); err != nil {
			return nil, err
		}
		out[day] = secs
	}
	return out, rows.Err()
}

// RepoTotal is one row of the per-repo breakdown.
type RepoTotal struct {
	Repo    string
	Seconds int
	Days    int
	Last    string
}

// ActivityTotal is one source-separated repo/branch total. Callers must not
// sum sources blindly: session and WakaTime can describe the same interval.
type ActivityTotal struct {
	Repo    string
	Branch  string
	Source  Source
	Seconds int
}

// ActivityTotals returns source-separated totals for contextual reports.
func (s *Store) ActivityTotals(q Query) ([]ActivityTotal, error) {
	clause, args := q.where()
	rows, err := s.db.Query(`
SELECT repo, branch, source, SUM(seconds)
FROM activity `+clause+`
GROUP BY repo, branch, source
ORDER BY repo, branch, source`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityTotal
	for rows.Next() {
		var item ActivityTotal
		if err := rows.Scan(&item.Repo, &item.Branch, &item.Source, &item.Seconds); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RepoTotals returns seconds per repository, busiest first.
func (s *Store) RepoTotals(q Query) ([]RepoTotal, error) {
	clause, args := q.where()
	rows, err := s.db.Query(`
SELECT repo, SUM(seconds) AS secs, COUNT(DISTINCT day) AS days, MAX(day)
FROM activity `+clause+`
GROUP BY repo ORDER BY secs DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RepoTotal
	for rows.Next() {
		var r RepoTotal
		if err := rows.Scan(&r.Repo, &r.Seconds, &r.Days, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoDayTotals returns per-day seconds for one repository, for a per-repo
// heatmap.
func (s *Store) RepoDayTotals(repo string, q Query) (map[string]int, error) {
	q.Repo = repo
	return s.DayTotals(q)
}

// MarkCollected records that a collector ran, so incremental runs know their
// starting point.
func (s *Store) MarkCollected(name string, at time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO collector (name, last_at) VALUES (?, ?)
ON CONFLICT (name) DO UPDATE SET last_at = excluded.last_at`, name, at.Unix())
	return err
}

// LastCollected reports when a collector last ran, zero when never.
func (s *Store) LastCollected(name string) time.Time {
	var unix int64
	if err := s.db.QueryRow(`SELECT last_at FROM collector WHERE name = ?`, name).Scan(&unix); err != nil {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// Empty reports whether anything has been recorded yet, so the CLI can explain
// how to start collecting instead of printing a blank chart.
func (s *Store) Empty() bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activity`).Scan(&n); err != nil {
		return true
	}
	return n == 0
}

// Path is where the database lives inside a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, "stats.db") }

// ClearQuery selects durable activity rows to delete. Repo is an exact name —
// deletion must not use the fuzzy LIKE semantics a report does.
type ClearQuery struct {
	Repo    string
	Sources []Source
}

// Clear deletes matching activity and returns the number of rows removed.
// With an empty query it clears all activity and collector checkpoints.
func (s *Store) Clear(q ClearQuery) (int64, error) {
	clause := ""
	var args []any
	if q.Repo != "" {
		clause = " WHERE repo = ?"
		args = append(args, q.Repo)
	}
	if len(q.Sources) > 0 {
		if clause == "" {
			clause = " WHERE "
		} else {
			clause += " AND "
		}
		clause += "source IN (" + placeholders(len(q.Sources)) + ")"
		for _, source := range q.Sources {
			args = append(args, string(source))
		}
	}
	result, err := s.db.Exec("DELETE FROM activity"+clause, args...)
	if err != nil {
		return 0, err
	}
	if q.Repo == "" && len(q.Sources) == 0 {
		if _, err := s.db.Exec("DELETE FROM collector"); err != nil {
			return 0, err
		}
	}
	return result.RowsAffected()
}
