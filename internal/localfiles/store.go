package localfiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const journalFilename = "journal.json"

type journalPhase string

const (
	phaseStaged     journalPhase = "staged"
	phaseApplying   journalPhase = "applying"
	phaseCompleted  journalPhase = "completed"
	phaseRolledBack journalPhase = "rolled-back"
	phaseReconcile  journalPhase = "reconcile"
)

type journalFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	Action    Action `json:"action"`
	State     State  `json:"state"`
	Published bool   `json:"published"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	OldMode   string `json:"old_mode,omitempty"`
}

type journal struct {
	SchemaVersion  int           `json:"schema_version"`
	RequestID      string        `json:"request_id"`
	PlanDigest     string        `json:"plan_digest"`
	ManifestDigest string        `json:"manifest_digest"`
	RetainForEvict bool          `json:"retain_for_evict"`
	Phase          journalPhase  `json:"phase"`
	Files          []journalFile `json:"files"`
}

type operationStore struct {
	root      string
	requestID string
	operation string
}

func defaultStoreRoot() string {
	return filepath.Join(devconfig.DataHome(), "dev", "local-files", "v1")
}

func newOperationStore(root, requestID string) (*operationStore, error) {
	if root == "" {
		root = defaultStoreRoot()
	}
	canonical, err := pathx.Canonical(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local-files store: %w", err)
	}
	if err := ensurePrivateDirectory(canonical); err != nil {
		return nil, err
	}
	operation := filepath.Join(canonical, requestID)
	return &operationStore{root: canonical, requestID: requestID, operation: operation}, nil
}

func openOperationStore(root, requestID string) (*operationStore, bool, error) {
	if root == "" {
		root = defaultStoreRoot()
	}
	canonical, err := pathx.Canonical(root)
	if err != nil {
		return nil, false, err
	}
	store := &operationStore{root: canonical, requestID: requestID, operation: filepath.Join(canonical, requestID)}
	info, err := os.Lstat(store.operation)
	if errors.Is(err, fs.ErrNotExist) {
		return store, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, false, errors.New("local-files operation store is not a private directory")
	}
	if journalInfo, journalErr := os.Lstat(store.journalPath()); errors.Is(journalErr, fs.ErrNotExist) {
		// Older interrupted builds could stage a manifest and blobs before their
		// first durable journal write. Publication always followed that write, so a
		// private store with no journal is safe to discard only after its complete
		// operation-owned layout has been verified.
		if err := store.discardUninitialized(info); err != nil {
			return nil, false, err
		}
		return store, false, nil
	} else if journalErr != nil {
		return nil, false, journalErr
	} else if !journalInfo.Mode().IsRegular() || journalInfo.Mode()&os.ModeSymlink != 0 || journalInfo.Mode().Perm()&0o077 != 0 {
		return nil, false, errors.New("local-files journal is not a private regular file")
	}
	return store, true, nil
}

func (s *operationStore) ensureOperation() error {
	return ensurePrivateDirectory(s.operation)
}

func (s *operationStore) ensurePayloadLayout() error {
	for _, directory := range []string{s.blobDir(), s.rollbackDir()} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func (s *operationStore) discardUninitialized(expected fs.FileInfo) error {
	walkErr := filepath.WalkDir(s.operation, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == s.operation {
			return nil
		}
		relative, err := filepath.Rel(s.operation, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("unjournaled local-files store contains an unsafe entry")
		}
		parts := strings.Split(relative, "/")
		switch {
		case len(parts) == 1 && parts[0] == "manifest.json" && info.Mode().IsRegular():
			return nil
		case len(parts) == 1 && (parts[0] == "blobs" || parts[0] == "rollback") && info.IsDir():
			return nil
		case len(parts) == 1 && strings.HasPrefix(parts[0], ".dev-safefile-") && info.Mode().IsRegular():
			return nil
		case len(parts) == 2 && (parts[0] == "blobs" || parts[0] == "rollback") && info.Mode().IsRegular():
			if isOperationBlobName(parts[1]) || strings.HasPrefix(parts[1], ".dev-safefile-") {
				return nil
			}
		}
		return errors.New("unjournaled local-files store contains an unknown entry")
	})
	if walkErr != nil {
		return fmt.Errorf("inspect unjournaled local-files store: %w", walkErr)
	}
	current, err := os.Lstat(s.operation)
	if err != nil || !os.SameFile(expected, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("unjournaled local-files store changed during inspection")
	}
	if err := os.RemoveAll(s.operation); err != nil {
		return fmt.Errorf("remove unjournaled local-files store: %w", err)
	}
	return syncDirectoryPath(s.root)
}

func isOperationBlobName(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (s *operationStore) journalPath() string  { return filepath.Join(s.operation, journalFilename) }
func (s *operationStore) manifestPath() string { return filepath.Join(s.operation, "manifest.json") }
func (s *operationStore) blobDir() string      { return filepath.Join(s.operation, "blobs") }
func (s *operationStore) rollbackDir() string  { return filepath.Join(s.operation, "rollback") }
func (s *operationStore) blobPath(index int) string {
	return filepath.Join(s.blobDir(), fmt.Sprintf("%03d", index))
}
func (s *operationStore) rollbackPath(index int) string {
	return filepath.Join(s.rollbackDir(), fmt.Sprintf("%03d", index))
}

func (s *operationStore) loadJournal() (journal, error) {
	data, err := readPrivateFile(s.journalPath(), 2<<20)
	if err != nil {
		return journal{}, err
	}
	var value journal
	if err := fleet.UnmarshalStrict(data, 2<<20, &value); err != nil {
		return journal{}, fmt.Errorf("decode local-files journal: %w", err)
	}
	if err := validateJournal(value); err != nil {
		return journal{}, err
	}
	return value, nil
}

func validateJournal(value journal) error {
	if value.SchemaVersion != SchemaVersion || value.RequestID == "" || !validDigest(value.PlanDigest) || !validDigest(value.ManifestDigest) {
		return errors.New("invalid local-files journal binding")
	}
	switch value.Phase {
	case phaseStaged, phaseApplying, phaseCompleted, phaseRolledBack, phaseReconcile:
	default:
		return errors.New("invalid local-files journal phase")
	}
	for index, file := range value.Files {
		if index > 0 && value.Files[index-1].Path >= file.Path {
			return errors.New("local-files journal paths are not sorted")
		}
		switch file.Action {
		case actionCurrent, actionCreate, actionReplace:
		default:
			return errors.New("local-files journal has invalid action")
		}
		if !file.State.validResult() {
			return errors.New("local-files journal has invalid result state")
		}
		if file.Size < 0 || file.Mode != "0600" && file.Mode != "0700" {
			return errors.New("local-files journal has invalid public metadata")
		}
		if file.Action == actionReplace && (!validDigest(file.OldSHA256) || !validFileMode(file.OldMode)) {
			return errors.New("local-files replacement journal lacks rollback binding")
		}
	}
	return nil
}

func (s *operationStore) writeJournal(value journal) error {
	if err := validateJournal(value); err != nil {
		return err
	}
	return writePrivateJSON(s.journalPath(), value)
}

func (s *operationStore) writeManifest(envelope ApplyEnvelope) error {
	manifest := struct {
		SchemaVersion  int        `json:"schema_version"`
		RequestID      string     `json:"request_id"`
		Binding        Binding    `json:"binding"`
		ManifestDigest string     `json:"manifest_digest"`
		PlanDigest     string     `json:"plan_digest"`
		Files          []FileSpec `json:"files"`
	}{SchemaVersion, envelope.Request.RequestID, envelope.Request.Binding,
		envelope.Request.ManifestDigest, envelope.Plan.PlanDigest, envelope.Request.Files}
	return writePrivateJSON(s.manifestPath(), manifest)
}

func (s *operationStore) writeBlob(filename string, content []byte, executable bool) error {
	if existing, err := readPrivateFile(filename, safefile.CompiledMaxFileBytes); err == nil {
		if digestBytes(existing) != digestBytes(content) {
			return errors.New("existing local-files blob does not match operation")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parentPath := filepath.Dir(filename)
	root, rootInfo, err := safefile.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(filename)
	if _, err := safefile.CreatePrivateNoClobber(context.Background(), root, name, content, executable); err != nil {
		return err
	}
	if err := safefile.VerifyRoot(parentPath, rootInfo); err != nil {
		return err
	}
	readback, err := readPrivateFile(filename, safefile.CompiledMaxFileBytes)
	if err != nil || digestBytes(readback) != digestBytes(content) {
		return errors.New("local-files blob failed post-write verification")
	}
	return nil
}

func (s *operationStore) readBlob(index int, rollback bool, maxBytes int64) ([]byte, error) {
	path := s.blobPath(index)
	if rollback {
		path = s.rollbackPath(index)
	}
	return readPrivateFile(path, maxBytes)
}

func (s *operationStore) cleanupPayloads(retain bool) error {
	var result error
	if !retain {
		result = errors.Join(result, os.RemoveAll(s.blobDir()), removeIfPresent(s.manifestPath()))
	}
	result = errors.Join(result, os.RemoveAll(s.rollbackDir()))
	return result
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func syncDirectoryPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local-files state path is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("local-files state directory is not owner-private")
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	parentPath := filepath.Dir(path)
	if err := ensurePrivateDirectory(parentPath); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	root, rootInfo, err := safefile.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	current, statErr := root.Lstat(name)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		_, err = safefile.CreatePrivateNoClobber(context.Background(), root, name, data, false)
	case statErr != nil:
		return statErr
	case !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || current.Mode().Perm()&0o077 != 0:
		return errors.New("local-files state file is not a private regular file")
	default:
		_, err = safefile.AtomicReplacePrivate(context.Background(), root, name, current, data, false)
	}
	if err != nil {
		return err
	}
	if err := safefile.VerifyRoot(parentPath, rootInfo); err != nil {
		return err
	}
	readback, err := readPrivateFile(path, 2<<20)
	if err != nil || string(readback) != string(data) {
		return errors.New("local-files state file failed post-write verification")
	}
	return nil
}

func readPrivateFile(path string, maxBytes int64) ([]byte, error) {
	parentPath := filepath.Dir(path)
	root, rootInfo, err := safefile.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("local-files state file is not a private regular file")
	}
	data, _, err := safefile.ReadStableRegular(context.Background(), root, name, info, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := safefile.VerifyRoot(parentPath, rootInfo); err != nil {
		return nil, err
	}
	return data, nil
}
