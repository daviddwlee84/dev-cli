// Package machineid owns dev's private, durable host UUID.
package machineid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/google/uuid"
)

const SchemaVersion = 1

type document struct {
	SchemaVersion int    `json:"schema_version"`
	MachineID     string `json:"machine_id"`
}

type Store struct {
	Path     string
	generate func() string
}

func DefaultPath() string {
	return filepath.Join(devconfig.DataHome(), "dev", "machine", "v1", "identity.json")
}

func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return &Store{Path: filepath.Clean(path), generate: uuid.NewString}
}

// LoadOrCreate returns this machine's stable ID from the default private store.
func LoadOrCreate(ctx context.Context) (string, error) {
	return NewStore("").LoadOrCreate(ctx)
}

func Validate(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("machine_id %q is not a canonical non-zero UUID", value)
	}
	return nil
}

func (s *Store) Load() (string, error) {
	if s == nil || s.Path == "" {
		return "", errors.New("load machine identity from nil store")
	}
	if err := checkPrivateDir(filepath.Dir(s.Path)); err != nil {
		return "", fmt.Errorf("inspect machine identity directory: %w", err)
	}
	info, err := os.Lstat(s.Path)
	if err != nil {
		return "", fmt.Errorf("inspect machine identity: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("machine identity %s is not a regular file", s.Path)
	}
	private, err := privateModeMatches(s.Path, info.Mode(), 0o600)
	if err != nil {
		return "", fmt.Errorf("inspect machine identity permissions: %w", err)
	}
	if !private {
		return "", fmt.Errorf("machine identity %s permissions are not private (want 0600 or a protected owner-only policy)", s.Path)
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return "", fmt.Errorf("open machine identity: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var payload document
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode machine identity: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("decode machine identity: multiple JSON values")
		}
		return "", fmt.Errorf("decode machine identity trailing data: %w", err)
	}
	if payload.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("machine identity schema_version %d: want %d", payload.SchemaVersion, SchemaVersion)
	}
	if err := Validate(payload.MachineID); err != nil {
		return "", err
	}
	return payload.MachineID, nil
}

func (s *Store) LoadOrCreate(ctx context.Context) (string, error) {
	if s == nil || s.Path == "" {
		return "", errors.New("create machine identity with nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	directory := filepath.Dir(s.Path)
	if err := ensurePrivateDir(directory); err != nil {
		return "", fmt.Errorf("prepare machine identity directory: %w", err)
	}
	lockDirectory := filepath.Join(directory, ".lock")
	if err := ensurePrivateDir(lockDirectory); err != nil {
		return "", fmt.Errorf("prepare machine identity lock: %w", err)
	}
	var result string
	err := lockx.WithDir(ctx, lockDirectory, "machine identity", func() error {
		loaded, err := s.Load()
		if err == nil {
			result = loaded
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		generate := s.generate
		if generate == nil {
			generate = uuid.NewString
		}
		candidate := generate()
		if err := Validate(candidate); err != nil {
			return fmt.Errorf("generate machine identity: %w", err)
		}
		if err := s.write(candidate); err != nil {
			return err
		}
		result = candidate
		return nil
	})
	return result, err
}

func (s *Store) write(id string) error {
	if err := Validate(id); err != nil {
		return err
	}
	directory := filepath.Dir(s.Path)
	if err := ensurePrivateDir(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("create machine identity temp file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := setPrivateMode(name, 0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document{SchemaVersion: SchemaVersion, MachineID: id}); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode machine identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync machine identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close machine identity: %w", err)
	}
	if err := replaceFile(name, s.Path); err != nil {
		return fmt.Errorf("publish machine identity: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync machine identity directory: %w", err)
	}
	return nil
}

func ensurePrivateDir(path string) error {
	created := false
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		created = true
	} else if err != nil {
		return err
	}
	if created {
		if err := setPrivateMode(path, 0o700); err != nil {
			return err
		}
	}
	return checkPrivateDir(path)
}

func checkPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a private directory", path)
	}
	private, err := privateModeMatches(path, info.Mode(), 0o700)
	if err != nil {
		return fmt.Errorf("inspect machine identity directory permissions: %w", err)
	}
	if !private {
		return fmt.Errorf("machine identity directory %s permissions are not private (want 0700 or a protected owner-only policy)", path)
	}
	return nil
}
