// Package sshactivity retains private, host-local observations of explicit SSH
// use and tests. It does not discover hosts or authorize a connection or cleanup.
package sshactivity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const maxRecordBytes = 64 << 10

type ProfileRecord struct {
	SchemaVersion int    `json:"schema_version"`
	ProfileID     string `json:"profile_id"`
	// Fingerprint identifies the last-used profile revision. Tests never change it
	// once there is a usage observation, so testing cannot resurrect old usage.
	Fingerprint            string      `json:"fingerprint"`
	LastUsed               time.Time   `json:"last_used,omitempty"`
	LastTest               *TestRecord `json:"last_test,omitempty"`
	LastSuccess            time.Time   `json:"last_success,omitempty"`
	LastSuccessFingerprint string      `json:"last_success_fingerprint,omitempty"`
}

type TestRecord struct {
	ObservedAt  time.Time                 `json:"observed_at"`
	Fingerprint string                    `json:"fingerprint"`
	Mode        string                    `json:"mode"`
	Status      string                    `json:"status"`
	Stages      []sshhost.DiagnosticStage `json:"stages"`
	Routes      []sshhost.DiagnosticRoute `json:"routes,omitempty"`
	Endpoint    string                    `json:"endpoint,omitempty"`
}

// Store uses one bounded, atomically replaced record per profile. Source-bound
// private writes and a per-profile process lock preserve concurrent use/tests.
// Reads never create directories or locks; records are outside discovery cache.
type Store struct{ Dir string }

func NewStore(dir string) *Store { return &Store{Dir: filepath.Clean(dir)} }

func validText(value string, limit int) bool {
	return len(value) <= limit && !strings.ContainsFunc(value, unicode.IsControl)
}

func (s *Store) path(id string) (string, error) {
	if s == nil || !filepath.IsAbs(s.Dir) || filepath.Clean(s.Dir) != s.Dir || id == "" || !validText(id, 512) {
		return "", errors.New("invalid SSH activity identity or path")
	}
	h := sha256.Sum256([]byte(id))
	return filepath.Join(s.Dir, hex.EncodeToString(h[:])+".json"), nil
}

func (s *Store) Read(ctx context.Context, id string) (ProfileRecord, error) {
	empty := ProfileRecord{SchemaVersion: 1, ProfileID: id}
	path, err := s.path(id)
	if err != nil {
		return empty, err
	}
	// configedit first checks the entire ancestor chain, even for missing files.
	data, err := configedit.Read(ctx, path)
	if err != nil {
		return empty, err
	}
	if len(data) == 0 {
		if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
			return empty, nil
		}
		return empty, errors.New("empty SSH activity record")
	}
	if len(data) > maxRecordBytes {
		return empty, errors.New("SSH activity record exceeds limit")
	}
	for _, item := range []struct {
		path string
		dir  bool
	}{{s.Dir, true}, {path, false}} {
		info, e := os.Lstat(item.path)
		if e != nil {
			return empty, e
		}
		if e = privatefile.Check(item.path, info, item.dir); e != nil {
			return empty, e
		}
	}
	var record ProfileRecord
	if json.Unmarshal(data, &record) != nil || record.SchemaVersion != 1 || record.ProfileID != id || !validText(record.Fingerprint, 256) {
		return empty, errors.New("invalid SSH activity record")
	}
	if record.LastTest != nil {
		if err := validateTest(*record.LastTest); err != nil {
			return empty, err
		}
	}
	return record, nil
}

func (s *Store) ReadProfiles(ctx context.Context, ids []string) (map[string]ProfileRecord, error) {
	result := make(map[string]ProfileRecord)
	var failures []error
	for _, id := range ids {
		if _, found := result[id]; found {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		record, err := s.Read(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		result[id] = record
	}
	return result, errors.Join(failures...)
}

func (s *Store) RecordUse(ctx context.Context, id, fingerprint string, at time.Time) (ProfileRecord, error) {
	if fingerprint == "" || !validText(fingerprint, 256) || at.IsZero() {
		return ProfileRecord{}, errors.New("invalid SSH use observation")
	}
	return s.update(ctx, id, func(record *ProfileRecord) {
		if at.After(record.LastUsed) {
			record.LastUsed, record.Fingerprint = at.UTC(), fingerprint
		}
	})
}

func (s *Store) RecordTest(ctx context.Context, id string, test TestRecord) (ProfileRecord, error) {
	if err := validateTest(test); err != nil {
		return ProfileRecord{}, err
	}
	return s.update(ctx, id, func(record *ProfileRecord) {
		if record.LastTest == nil || !test.ObservedAt.Before(record.LastTest.ObservedAt) {
			test.ObservedAt = test.ObservedAt.UTC()
			record.LastTest = &test
			if record.LastUsed.IsZero() {
				record.Fingerprint = test.Fingerprint
			}
		}
		if test.Mode == "full" && test.Status == "ready" && test.ObservedAt.After(record.LastSuccess) {
			record.LastSuccess, record.LastSuccessFingerprint = test.ObservedAt.UTC(), test.Fingerprint
		}
	})
}

func validateTest(test TestRecord) error {
	if test.ObservedAt.IsZero() || test.Fingerprint == "" || !validText(test.Fingerprint, 256) || test.Mode != "network" && test.Mode != "full" || !validText(test.Endpoint, 512) || len(test.Routes) > 4 || len(test.Stages) > 32 {
		return errors.New("invalid SSH test observation")
	}
	switch test.Status {
	case "ready", "network_ready", "not_ready", "incomplete", "canceled", "stale", "error":
	default:
		return errors.New("invalid SSH test status")
	}
	for _, stage := range test.Stages {
		if stage.RoundTripMS != nil && (math.IsNaN(*stage.RoundTripMS) || math.IsInf(*stage.RoundTripMS, 0) || *stage.RoundTripMS < 0 || *stage.RoundTripMS > 2000) {
			return errors.New("invalid SSH ping observation")
		}
		if !validText(stage.Name, 64) || !validText(stage.Code, 128) || !validText(stage.State, 32) || stage.ElapsedMS < 0 || stage.ElapsedMS > 60000 {
			return errors.New("invalid SSH test stage")
		}
	}
	for _, route := range test.Routes {
		for _, value := range []string{route.Address, route.Family, route.Interface, route.Source, route.Gateway, route.Destination, route.Code} {
			if !validText(value, 512) {
				return errors.New("invalid SSH test route")
			}
		}
	}
	return nil
}

func (s *Store) update(ctx context.Context, id string, change func(*ProfileRecord)) (ProfileRecord, error) {
	path, err := s.path(id)
	if err != nil {
		return ProfileRecord{}, err
	}
	// Inspect the directory chain through an unwritten guard path. The record
	// itself is read only after locking: a concurrent atomic replacement before
	// the lock is an expected update, not authority to abort this observation.
	if _, err = configedit.Read(ctx, filepath.Join(s.Dir, ".directory-check")); err != nil {
		return ProfileRecord{}, err
	}
	if err = privatefile.EnsureDir(s.Dir); err != nil {
		return ProfileRecord{}, err
	}
	lockPath := path + ".lock"
	if err = configedit.WritePrivate(ctx, lockPath, []byte("ssh-activity\n"), false); err != nil && !errors.Is(err, fs.ErrExist) {
		return ProfileRecord{}, err
	}
	lockInfo, err := os.Lstat(lockPath)
	if err != nil {
		return ProfileRecord{}, err
	}
	if err = privatefile.Check(lockPath, lockInfo, false); err != nil {
		return ProfileRecord{}, err
	}
	var record ProfileRecord
	err = lockx.WithFile(ctx, lockPath, "SSH activity", func() error {
		currentLock, e := os.Lstat(lockPath)
		if e != nil || !os.SameFile(lockInfo, currentLock) {
			return errors.New("SSH activity lock changed")
		}
		if e = privatefile.Check(lockPath, currentLock, false); e != nil {
			return e
		}
		record, e = s.Read(ctx, id)
		if e != nil {
			return e
		}
		change(&record)
		data, e := json.Marshal(record)
		if e != nil {
			return e
		}
		if len(data) > maxRecordBytes {
			return errors.New("SSH activity record exceeds limit")
		}
		return configedit.WritePrivate(ctx, path, data, true)
	})
	if err != nil {
		return record, fmt.Errorf("save SSH activity: %w", err)
	}
	return record, nil
}

func FromDiagnosis(fingerprint, mode string, at time.Time, diagnosis sshhost.Diagnosis) TestRecord {
	stages := append([]sshhost.DiagnosticStage{}, diagnosis.Stages...)
	if len(diagnosis.Attempts) > 0 {
		stages = append(stages, diagnosis.Attempts[0].Stages...)
	}
	return TestRecord{ObservedAt: at.UTC(), Fingerprint: fingerprint, Mode: mode, Status: diagnosis.Status, Stages: stages, Routes: append([]sshhost.DiagnosticRoute{}, diagnosis.Routes...), Endpoint: diagnosis.SocketRemote}
}
