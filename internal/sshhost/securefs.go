package sshhost

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const maxConfigBytes = 2 << 20

type fileSnapshot struct {
	path     string
	exists   bool
	info     fs.FileInfo
	data     []byte
	digest   string
	metadata fileMetadata
	hasMeta  bool
}

func (s fileSnapshot) public() FileState {
	state := FileState{Path: s.path, Exists: s.exists, Digest: s.digest}
	if s.info != nil {
		state.Mode = s.info.Mode().Perm()
		state.Size = s.info.Size()
		state.ModTime = s.info.ModTime()
	}
	return state
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func inspectExistingTree(paths Paths) error {
	if err := validateHomeDirectory(paths.Home); err != nil {
		return err
	}
	if err := validateOptionalPrivateDirectory(paths.SSHDir); err != nil {
		return err
	}
	if err := validateOptionalPrivateDirectory(paths.ManagedDir); err != nil {
		return err
	}
	return nil
}

func ensureMutationTree(paths Paths) error {
	if err := validateHomeDirectory(paths.Home); err != nil {
		return err
	}
	if err := ensurePrivateChild(paths.Home, ".ssh", false); err != nil {
		return fmt.Errorf("prepare %s: %w", paths.SSHDir, err)
	}
	if err := ensurePrivateChild(paths.SSHDir, "dev.d", true); err != nil {
		return fmt.Errorf("prepare %s: %w", paths.ManagedDir, err)
	}
	return nil
}

func validateHomeDirectory(path string) error {
	if err := platformRejectReparsePath(path, false); err != nil {
		return fmt.Errorf("validate home path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect home directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("home is not a direct directory: %w", ErrUnsafePath)
	}
	if err := platformValidateHome(path, info); err != nil {
		return fmt.Errorf("validate home directory: %w", err)
	}
	return nil
}

func validateOptionalPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect private directory %s: %w", path, err)
	}
	return validatePrivateDirectory(path, info)
}

func validatePrivateDirectory(path string, info fs.FileInfo) error {
	if err := platformRejectReparsePath(path, false); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a direct directory: %w", path, ErrUnsafePath)
	}
	if err := platformValidatePrivateDirectory(path, info); err != nil {
		return fmt.Errorf("validate private directory %s: %w", path, err)
	}
	return nil
}

func ensurePrivateChild(parentPath, name string, parentPrivate bool) error {
	parent, held, err := openHeldDirectory(parentPath, parentPrivate)
	if err != nil {
		return err
	}
	defer parent.Close()
	childPath := filepath.Join(parentPath, name)
	before, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := platformMakePrivateDirectory(childPath); err != nil {
			return err
		}
		before, err = parent.Lstat(name)
	}
	if err != nil {
		return err
	}
	if err := validatePrivateDirectory(childPath, before); err != nil {
		return err
	}
	if err := verifyHeldDirectory(parentPath, held, parentPrivate); err != nil {
		return fmt.Errorf("parent changed while preparing %s: %w", childPath, err)
	}
	return nil
}

func openHeldDirectory(path string, private bool) (*os.Root, fs.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if private {
		if err := validatePrivateDirectory(path, before); err != nil {
			return nil, nil, err
		}
	} else if err := validateHomeDirectory(path); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	held, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(before, held) || !os.SameFile(current, held) {
		root.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("directory changed while opening: %w", ErrUnsafePath)
	}
	return root, held, nil
}

func verifyHeldDirectory(path string, held fs.FileInfo, private bool) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) {
		return ErrSourceChanged
	}
	if private {
		return validatePrivateDirectory(path, current)
	}
	return validateHomeDirectory(path)
}

func readSecureFile(path string, captureMetadata bool) (fileSnapshot, error) {
	parentPath := filepath.Dir(path)
	root, held, err := openHeldDirectory(parentPath, true)
	if err != nil {
		return fileSnapshot{path: path}, err
	}
	defer root.Close()
	snapshot, err := readSecureFileAt(root, filepath.Base(path), path, captureMetadata)
	if err != nil {
		return snapshot, err
	}
	if err := verifyHeldDirectory(parentPath, held, true); err != nil {
		return fileSnapshot{}, fmt.Errorf("parent changed while reading %s: %w", path, err)
	}
	return snapshot, nil
}

func readSecureFileAt(root *os.Root, name, path string, captureMetadata bool) (fileSnapshot, error) {
	snapshot := fileSnapshot{path: path}
	before, err := root.Lstat(name)
	if err != nil {
		return snapshot, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return snapshot, fmt.Errorf("%s is not a direct regular file: %w", path, ErrUnsafePath)
	}
	file, err := root.Open(name)
	if err != nil {
		return snapshot, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return snapshot, err
	}
	if !opened.Mode().IsRegular() || !stableFileInfo(before, opened) {
		return snapshot, fmt.Errorf("%s changed while opening: %w", path, ErrSourceChanged)
	}
	if err := platformValidatePrivateFile(path, file, opened); err != nil {
		return snapshot, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return snapshot, err
	}
	if len(data) > maxConfigBytes {
		return snapshot, fmt.Errorf("%s exceeds %d bytes", path, maxConfigBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return snapshot, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return snapshot, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !stableFileInfo(opened, after) || !stableFileInfo(after, current) || after.Size() != int64(len(data)) {
		return snapshot, fmt.Errorf("%s changed while reading: %w", path, ErrSourceChanged)
	}
	snapshot.exists = true
	snapshot.info = opened
	snapshot.data = data
	snapshot.digest = digestBytes(data)
	if captureMetadata {
		metadata, err := platformCaptureMetadata(path, file, opened)
		if err != nil {
			return fileSnapshot{}, fmt.Errorf("capture metadata for %s: %w", path, err)
		}
		if err := platformMetadataRoundTrip(metadata); err != nil {
			return fileSnapshot{}, fmt.Errorf("metadata for %s cannot be safely round-tripped: %w", path, err)
		}
		if err := platformVerifyMetadata(path, file, metadata); err != nil {
			return fileSnapshot{}, fmt.Errorf("metadata for %s changed while it was captured: %w", path, ErrSourceChanged)
		}
		snapshot.metadata = metadata
		snapshot.hasMeta = true
		final, statErr := root.Lstat(name)
		if statErr != nil || final.Mode()&os.ModeSymlink != 0 || !stableFileInfo(opened, final) {
			if statErr != nil {
				return fileSnapshot{}, statErr
			}
			return fileSnapshot{}, fmt.Errorf("%s changed while reading metadata: %w", path, ErrSourceChanged)
		}
	}
	return snapshot, nil
}

func readSecureFileIfExists(path string, captureMetadata bool) (fileSnapshot, error) {
	snapshot, err := readSecureFile(path, captureMetadata)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	return snapshot, err
}

func readSecureFileAtIfExists(root *os.Root, name, path string, captureMetadata bool) (fileSnapshot, error) {
	snapshot, err := readSecureFileAt(root, name, path, captureMetadata)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	return snapshot, err
}

func stableFileInfo(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func snapshotStillCurrent(expected fileSnapshot) (fileSnapshot, error) {
	current, err := readSecureFileIfExists(expected.path, expected.hasMeta)
	if err != nil {
		return current, err
	}
	return compareFileSnapshot(expected, current)
}

func snapshotStillCurrentAt(root *os.Root, name string, expected fileSnapshot) (fileSnapshot, error) {
	current, err := readSecureFileAtIfExists(root, name, expected.path, expected.hasMeta)
	if err != nil {
		return current, err
	}
	return compareFileSnapshot(expected, current)
}

func compareFileSnapshot(expected, current fileSnapshot) (fileSnapshot, error) {
	if expected.exists != current.exists {
		return current, ErrSourceChanged
	}
	if !expected.exists {
		return current, nil
	}
	if !stableFileInfo(expected.info, current.info) || expected.digest != current.digest {
		return current, ErrSourceChanged
	}
	return current, nil
}

func bytesAlreadyDesired(path string, desired []byte) (fileSnapshot, bool) {
	current, err := readSecureFileIfExists(path, false)
	if err != nil || !current.exists {
		return current, false
	}
	return current, bytes.Equal(current.data, desired)
}

type stagedFile struct {
	dir      string
	name     string
	root     *os.Root
	held     fs.FileInfo
	snapshot fileSnapshot
}

func createStagedFile(dir string, data []byte, metadata *fileMetadata) (*stagedFile, error) {
	root, held, err := openHeldDirectory(dir, true)
	if err != nil {
		return nil, err
	}
	var file *os.File
	var name string
	keep := false
	defer func() {
		if keep {
			return
		}
		if file != nil {
			_ = file.Close()
		}
		if name != "" {
			_ = root.Remove(name)
		}
		_ = root.Close()
	}()
	for attempts := 0; attempts < 32; attempts++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		name = ".dev-tmp-" + hex.EncodeToString(random[:]) + ".tmp"
		file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if errors.Is(err, fs.ErrExist) {
			file = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	if file == nil {
		return nil, errors.New("could not allocate a private staging file")
	}
	path := filepath.Join(dir, name)
	if err := platformApplyPrivateFile(path, file); err != nil {
		return nil, err
	}
	for remaining := data; len(remaining) > 0; {
		written, err := file.Write(remaining)
		if err != nil {
			return nil, err
		}
		if written == 0 {
			return nil, io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if metadata != nil {
		if err := platformApplyMetadata(path, file, *metadata); err != nil {
			return nil, fmt.Errorf("apply source metadata to staging file: %w", err)
		}
		if err := platformVerifyMetadata(path, file, *metadata); err != nil {
			return nil, fmt.Errorf("verify staged metadata: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	file = nil
	if err := verifyHeldDirectory(dir, held, true); err != nil {
		return nil, err
	}
	snapshot, err := readSecureFileAt(root, name, path, false)
	if err != nil {
		return nil, fmt.Errorf("revalidate staging file: %w", err)
	}
	if !bytes.Equal(snapshot.data, data) {
		return nil, errors.New("staging file did not retain desired bytes")
	}
	keep = true
	return &stagedFile{dir: dir, name: name, root: root, held: held, snapshot: snapshot}, nil
}

func (s *stagedFile) discard() error {
	if s == nil || s.root == nil {
		return nil
	}
	removeErr := s.root.Remove(s.name)
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	closeErr := s.root.Close()
	s.root = nil
	return errors.Join(removeErr, closeErr)
}

func (s *stagedFile) destinationName(destination string) (string, error) {
	if s == nil || s.root == nil || !samePath(s.dir, filepath.Dir(destination)) || filepath.Clean(destination) != destination {
		return "", fmt.Errorf("staging file is not beside destination: %w", ErrUnsafePath)
	}
	name := filepath.Base(destination)
	if name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("destination has no filename: %w", ErrUnsafePath)
	}
	return name, nil
}

func (s *stagedFile) revalidate() error {
	if _, err := snapshotStillCurrentAt(s.root, s.name, s.snapshot); err != nil {
		return fmt.Errorf("staging file changed before publication: %w", ErrSourceChanged)
	}
	if err := verifyHeldDirectory(s.dir, s.held, true); err != nil {
		return fmt.Errorf("staging parent changed before publication: %w", ErrSourceChanged)
	}
	return nil
}

func commitReplace(staged *stagedFile, destination string, expected fileSnapshot) error {
	name, err := staged.destinationName(destination)
	if err != nil {
		return err
	}
	if err := staged.revalidate(); err != nil {
		return err
	}
	if _, err := snapshotStillCurrentAt(staged.root, name, expected); err != nil {
		return fmt.Errorf("destination changed before replacement: %w", ErrSourceChanged)
	}
	if err := verifyHeldDirectory(staged.dir, staged.held, true); err != nil {
		return fmt.Errorf("destination parent changed before replacement: %w", ErrSourceChanged)
	}
	if err := staged.root.Rename(staged.name, name); err != nil {
		return err
	}
	if err := verifyHeldDirectory(staged.dir, staged.held, true); err != nil {
		return fmt.Errorf("destination parent changed during replacement: %w", ErrSourceChanged)
	}
	return platformSyncDirectory(staged.dir)
}

func commitNoReplace(staged *stagedFile, destination string, expected fileSnapshot) error {
	name, err := staged.destinationName(destination)
	if err != nil {
		return err
	}
	if err := staged.revalidate(); err != nil {
		return err
	}
	if _, err := snapshotStillCurrentAt(staged.root, name, expected); err != nil {
		return fmt.Errorf("destination appeared before creation: %w", ErrSourceChanged)
	}
	if err := verifyHeldDirectory(staged.dir, staged.held, true); err != nil {
		return fmt.Errorf("destination parent changed before creation: %w", ErrSourceChanged)
	}
	if err := staged.root.Link(staged.name, name); err != nil {
		return err
	}
	if err := staged.root.Remove(staged.name); err != nil {
		cleanupErr := staged.root.Remove(name)
		return errors.Join(err, cleanupErr)
	}
	if err := verifyHeldDirectory(staged.dir, staged.held, true); err != nil {
		return fmt.Errorf("destination parent changed during creation: %w", ErrSourceChanged)
	}
	return platformSyncDirectory(staged.dir)
}

func removeSecureFile(expected fileSnapshot) error {
	if !expected.exists || expected.path == "" {
		return fmt.Errorf("removal requires an existing source snapshot: %w", ErrSourceChanged)
	}
	parentPath := filepath.Dir(expected.path)
	root, held, err := openHeldDirectory(parentPath, true)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(expected.path)
	if _, err := snapshotStillCurrentAt(root, name, expected); err != nil {
		return fmt.Errorf("file changed before removal: %w", ErrSourceChanged)
	}
	if err := verifyHeldDirectory(parentPath, held, true); err != nil {
		return fmt.Errorf("parent changed before removal: %w", ErrSourceChanged)
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	if err := verifyHeldDirectory(parentPath, held, true); err != nil {
		return fmt.Errorf("parent changed during removal: %w", ErrSourceChanged)
	}
	return platformSyncDirectory(parentPath)
}
