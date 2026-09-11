package machineregistry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	_ "modernc.org/sqlite"
)

const (
	maxDatabaseBytes = 32 << 20
	maxMachines      = 10000
	maxBindings      = 50000
)

type Store struct{ Path string }

func DefaultPath() string {
	return filepath.Join(config.DataHome(), "dev", "machines", "registry.db")
}

func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return &Store{Path: filepath.Clean(path)}
}

// Read never creates directories, lock files, databases or schemas. Missing
// state is an empty registry. SQLite rollback journaling keeps these reads free
// of WAL/SHM creation; snapshots are read in a single read-only transaction.
func (s *Store) Read(ctx context.Context) (Snapshot, error) {
	snapshot, _, err := s.read(ctx)
	return snapshot, err
}

func (s *Store) read(ctx context.Context) (Snapshot, sourceState, error) {
	ctx = nonnilContext(ctx)
	empty := emptySnapshot()
	if err := ctx.Err(); err != nil {
		return empty, sourceState{}, err
	}
	if s == nil || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path {
		return empty, sourceState{}, ErrUnsafePath
	}
	source, err := inspectSource(s.Path)
	if err != nil || source.database == nil {
		return empty, source, err
	}
	db, err := openDatabase(s.Path, true)
	if err != nil {
		return empty, source, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, source, err
	}
	defer tx.Rollback()
	snapshot, err := readSnapshot(ctx, tx)
	if err != nil {
		return empty, source, err
	}
	if err := tx.Commit(); err != nil {
		return empty, source, err
	}
	if err := verifySource(s.Path, source); err != nil {
		return empty, source, err
	}
	return snapshot, source, nil
}

// Apply holds a controller-local lock, reloads the exact reviewed state, and
// changes every association in one SQLite transaction. No provider is invoked.
func (s *Store) Apply(ctx context.Context, plan Plan) (Result, error) {
	ctx = nonnilContext(ctx)
	result := Result{SchemaVersion: SchemaVersion, Status: "failed", Snapshot: emptySnapshot()}
	if s == nil || plan.state == nil || plan.state.path != s.Path {
		return result, ErrInvalidPlan
	}
	state := plan.state
	result.MachineID, result.Into = state.request.MachineID, state.request.Into
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Check before creating even the private directory/lock infrastructure.
	if err := s.checkPlan(ctx, state); err != nil {
		return failedResult(result, err)
	}
	if err := prepareDirectory(filepath.Dir(s.Path)); err != nil {
		return result, err
	}
	lockPath := filepath.Join(filepath.Dir(s.Path), ".registry.lock")
	lockInfo, err := preparePrivateFile(lockPath)
	if err != nil {
		return result, err
	}
	err = lockx.WithFile(ctx, lockPath, "machine registry", func() error {
		currentLock, err := inspectPrivateFile(lockPath, false)
		if err != nil || !os.SameFile(lockInfo, currentLock) {
			return errors.Join(ErrUnsafePath, err)
		}
		if err := s.checkPlan(ctx, state); err != nil {
			return err
		}
		if !state.changed {
			result.Status, result.Snapshot = "noop", cloneSnapshot(state.before)
			return nil
		}
		fileInfo, err := preparePrivateFile(s.Path)
		if err != nil {
			return err
		}
		db, err := openDatabase(s.Path, false)
		if err != nil {
			return err
		}
		defer db.Close()
		connection, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer connection.Close()
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		defer connection.ExecContext(context.Background(), "ROLLBACK") // harmless after commit
		current, err := readSnapshot(ctx, connection)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, state.before) {
			return ErrStale
		}
		if err := verifyDatabaseIdentity(s.Path, fileInfo); err != nil {
			return err
		}
		if err := migrate(ctx, connection); err != nil {
			return err
		}
		if err := writeSnapshot(ctx, connection, state.after); err != nil {
			return err
		}
		if err := verifyDatabaseIdentity(s.Path, fileInfo); err != nil {
			return err
		}
		result.Status = "unknown"
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		if err := verifyDatabaseIdentity(s.Path, fileInfo); err != nil {
			result.Status = "unknown"
			return err
		}
		result.Status, result.Snapshot = "changed", cloneSnapshot(state.after)
		return nil
	})
	return failedResult(result, err)
}

func failedResult(result Result, err error) (Result, error) {
	if errors.Is(err, ErrStale) && result.Status != "unknown" {
		result.Status = "stale"
	}
	return result, err
}

func (s *Store) checkPlan(ctx context.Context, state *planState) error {
	if err := verifyAnchor(state.source); err != nil {
		return err
	}
	current, source, err := s.read(ctx)
	if err != nil {
		return err
	}
	if !sameSourceIdentity(state.source, source) || !reflect.DeepEqual(current, state.before) {
		return ErrStale
	}
	return nil
}

func nonnilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func openDatabase(path string, readonly bool) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // file:///C:/...; do not turn the drive into a URI host
	}
	u := &url.URL{Scheme: "file", Path: uriPath}
	query := u.Query()
	if readonly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
	} else {
		query.Set("mode", "rw")
	}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "trusted_schema(0)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSnapshot(ctx context.Context, db queryer) (Snapshot, error) {
	snapshot := emptySnapshot()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return snapshot, fmt.Errorf("read machine registry schema: %w", err)
	}
	if version == 0 {
		var tables int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return snapshot, err
		}
		if tables != 0 {
			return snapshot, ErrSchema
		}
		return snapshot, nil
	}
	if version != SchemaVersion {
		return snapshot, fmt.Errorf("database version %d: %w", version, ErrSchema)
	}
	if err := validateTables(ctx, db); err != nil {
		return snapshot, err
	}
	var revision int64
	if err := db.QueryRowContext(ctx, "SELECT revision FROM registry_meta WHERE singleton=1").Scan(&revision); err != nil || revision < 0 {
		return snapshot, errors.Join(ErrSchema, err)
	}
	snapshot.Revision = uint64(revision)
	rows, err := db.QueryContext(ctx, "SELECT id,label,revision,COALESCE(merged_into,''),preferred_profile FROM machines ORDER BY id LIMIT ?", maxMachines+1)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var machine Machine
		var machineRevision int64
		if err := rows.Scan(&machine.ID, &machine.Label, &machineRevision, &machine.MergedInto, &machine.PreferredProfile); err != nil {
			rows.Close()
			return snapshot, err
		}
		machine.Revision = uint64(machineRevision)
		snapshot.Machines = append(snapshot.Machines, machine)
	}
	rowErr := rows.Err()
	if err := errors.Join(rowErr, rows.Close()); err != nil {
		return snapshot, err
	}
	rows, err = db.QueryContext(ctx, "SELECT provider,scope,native_id,fingerprint,COALESCE(machine_id,''),suppressed FROM bindings ORDER BY provider,scope,native_id LIMIT ?", maxBindings+1)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var binding Binding
		var suppressed int
		if err := rows.Scan(&binding.Provider, &binding.Scope, &binding.NativeID, &binding.Fingerprint, &binding.MachineID, &suppressed); err != nil {
			rows.Close()
			return snapshot, err
		}
		if suppressed != 0 && suppressed != 1 {
			rows.Close()
			return snapshot, ErrSchema
		}
		binding.Suppressed = suppressed == 1
		snapshot.Bindings = append(snapshot.Bindings, binding)
	}
	rowErr = rows.Err()
	if err := errors.Join(rowErr, rows.Close()); err != nil {
		return snapshot, err
	}
	sortSnapshot(&snapshot)
	return snapshot, validateSnapshot(snapshot)
}

func validateTables(ctx context.Context, db queryer) error {
	expected := map[string]string{
		"registry_meta": "singleton,revision",
		"machines":      "id,label,revision,merged_into,preferred_profile",
		"bindings":      "provider,scope,native_id,fingerprint,machine_id,suppressed",
	}
	rows, err := db.QueryContext(ctx, "SELECT name,type FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND type!='index'")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		if _, found := expected[name]; !found || kind != "table" {
			rows.Close()
			return ErrSchema
		}
		count++
	}
	rowErr := rows.Err()
	if err := errors.Join(rowErr, rows.Close()); err != nil {
		return err
	}
	if count != len(expected) {
		return ErrSchema
	}
	for table, want := range expected {
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")") // table is program-owned
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var index, notnull, primary int
			var name, kind string
			var fallback any
			if err := rows.Scan(&index, &name, &kind, &notnull, &fallback, &primary); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, name)
		}
		rowErr := rows.Err()
		if err := errors.Join(rowErr, rows.Close()); err != nil {
			return err
		}
		if strings.Join(columns, ",") != want {
			return ErrSchema
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.Conn) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == SchemaVersion {
		return nil
	}
	if version != 0 {
		return ErrSchema
	}
	_, err := db.ExecContext(ctx, `
CREATE TABLE registry_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), revision INTEGER NOT NULL CHECK(revision>=0));
INSERT INTO registry_meta VALUES(1,0);
CREATE TABLE machines(
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK(revision>0),
  merged_into TEXT REFERENCES machines(id) DEFERRABLE INITIALLY DEFERRED,
  preferred_profile TEXT NOT NULL DEFAULT ''
);
CREATE TABLE bindings(
  provider TEXT NOT NULL,
  scope TEXT NOT NULL,
  native_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL DEFAULT '',
  machine_id TEXT REFERENCES machines(id) DEFERRABLE INITIALLY DEFERRED,
  suppressed INTEGER NOT NULL CHECK(suppressed IN(0,1)),
  PRIMARY KEY(provider,scope,native_id),
  CHECK((suppressed=1 AND machine_id IS NULL) OR (suppressed=0 AND machine_id IS NOT NULL))
);
PRAGMA user_version=1;`)
	return err
}

func writeSnapshot(ctx context.Context, db *sql.Conn, snapshot Snapshot) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM bindings; DELETE FROM machines;"); err != nil {
		return err
	}
	for _, machine := range snapshot.Machines {
		if _, err := db.ExecContext(ctx, "INSERT INTO machines(id,label,revision,merged_into,preferred_profile) VALUES(?,?,?,?,?)", machine.ID, machine.Label, int64(machine.Revision), nullable(machine.MergedInto), machine.PreferredProfile); err != nil {
			return err
		}
	}
	for _, binding := range snapshot.Bindings {
		if _, err := db.ExecContext(ctx, "INSERT INTO bindings(provider,scope,native_id,fingerprint,machine_id,suppressed) VALUES(?,?,?,?,?,?)", binding.Provider, binding.Scope, binding.NativeID, binding.Fingerprint, nullable(binding.MachineID), binding.Suppressed); err != nil {
			return err
		}
	}
	_, err := db.ExecContext(ctx, "UPDATE registry_meta SET revision=? WHERE singleton=1", int64(snapshot.Revision))
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
