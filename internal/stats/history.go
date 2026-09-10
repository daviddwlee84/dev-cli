package stats

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// RefreshGit seeds all locally reachable history only when its identity changes.
// Existing observations, including other sources and no-longer-reachable days,
// remain durable. The checkpoint and the new buckets commit together.
func (s *Store) RefreshGit(ctx context.Context, r repo.Repo, force bool) error {
	common, err := gitx.Run(ctx, r.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(common+"\x00"+r.Name)))
	fingerprint := func() (string, error) {
		refs, e := gitx.Run(ctx, r.Path, "for-each-ref", "--format=%(refname) %(objectname)")
		if e != nil {
			return "", e
		}
		head, e := gitx.Run(ctx, r.Path, "rev-parse", "--verify", "HEAD")
		if e != nil {
			if _, symbolicErr := gitx.Run(ctx, r.Path, "symbolic-ref", "HEAD"); symbolicErr != nil {
				return "", e
			}
		}
		refs += "\x00" + head
		shallow, e := os.ReadFile(filepath.Join(common, "shallow"))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(refs+"\x00"+string(shallow)+"\x00"+time.Local.String()))), nil
	}
	before, err := fingerprint()
	if err != nil {
		return err
	}
	var previous string
	err = s.db.QueryRowContext(ctx, "SELECT fingerprint FROM git_history WHERE identity = ?", key).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !force && previous == before {
		return nil
	}
	output, err := gitx.Run(ctx, r.Path, "log", "--all", "--no-merges", "--format=%at")
	if err != nil {
		return err
	}
	totals := map[string]int{}
	for _, line := range strings.Fields(output) {
		unix, e := strconv.ParseInt(line, 10, 64)
		if e != nil {
			return fmt.Errorf("invalid Git commit timestamp: %w", e)
		}
		totals[time.Unix(unix, 0).In(time.Local).Format(dayFormat)] += int(CommitWeight.Seconds())
	}
	after, err := fingerprint()
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("Git history changed during backfill; refresh to retry")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for day, seconds := range totals {
		if _, err = tx.ExecContext(ctx, `INSERT INTO activity(day,repo,branch,source,seconds) VALUES(?,?,'','git',?)
   ON CONFLICT(day,repo,branch,source) DO UPDATE SET seconds=excluded.seconds`, day, r.Name, seconds); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_history(identity,repo,fingerprint) VALUES(?,?,?)
  ON CONFLICT(identity) DO UPDATE SET repo=excluded.repo,fingerprint=excluded.fingerprint`, key, r.Name, before); err != nil {
		return err
	}
	return tx.Commit()
}

// Year is a calendar-year projection of durable day totals.
type Year struct {
	Year                int
	Totals              map[string]int
	Seconds, ActiveDays int
	Until               time.Time
}

func Years(totals map[string]int, now time.Time) []Year {
	first := now.Year()
	found := false
	for day, seconds := range totals {
		d, err := time.ParseInLocation(dayFormat, day, now.Location())
		if err == nil && seconds > 0 && !d.After(now) {
			first = min(first, d.Year())
			found = true
		}
	}
	if !found {
		return nil
	}
	var years []Year
	for year := first; year <= now.Year(); year++ {
		item := Year{Year: year, Totals: map[string]int{}, Until: time.Date(year, 12, 31, 0, 0, 0, 0, now.Location())}
		if year == now.Year() {
			item.Until = now
		}
		for day, seconds := range totals {
			d, err := time.ParseInLocation(dayFormat, day, now.Location())
			if err == nil && d.Year() == year && !d.After(now) && seconds > 0 {
				item.Totals[day] = seconds
				item.Seconds += seconds
				item.ActiveDays++
			}
		}
		years = append(years, item)
	}
	return years
}

// YearHeatmaps wraps by whole week columns on narrow terminals.
func YearHeatmaps(years []Year, width int) string {
	var out strings.Builder
	for _, year := range years {
		fmt.Fprintf(&out, "  %d · %s · %d active days\n", year.Year, HumanDuration(year.Seconds), year.ActiveDays)
		start := time.Date(year.Year, 1, 1, 0, 0, 0, 0, year.Until.Location())
		columns := max(1, width-9)
		for !start.After(year.Until) {
			end := startOfWeek(start).AddDate(0, 0, columns*7-1)
			if end.After(year.Until) {
				end = year.Until
			}
			out.WriteString(Heatmap(year.Totals, HeatmapOptions{Since: start, Until: end, WeekdayLabels: true}))
			start = end.AddDate(0, 0, 1)
		}
		out.WriteByte('\n')
	}
	return out.String()
}
