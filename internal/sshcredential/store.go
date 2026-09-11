package sshcredential

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/pelletier/go-toml/v2"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

type Store struct{ Path string }
type storeSource struct {
	info       fs.FileInfo
	digest     [32]byte
	anchor     string
	anchorInfo fs.FileInfo
}
type Plan struct {
	Before Snapshot `json:"before"`
	After  Snapshot `json:"after"`
	state  *storePlanState
}
type storePlanState struct {
	store         *Store
	source        storeSource
	before, after Snapshot
}

func DefaultPath() string { return filepath.Join(config.ConfigHome(), "dev", "ssh-credentials.toml") }
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	if absolute, e := filepath.Abs(path); e == nil {
		path = absolute
	}
	return &Store{filepath.Clean(path)}
}
func emptySnapshot() Snapshot { return Snapshot{SchemaVersion: 1, Records: []Record{}} }
func cloneSnapshot(s Snapshot) Snapshot {
	s.Records = append([]Record{}, s.Records...)
	for i := range s.Records {
		if s.Records[i].Reference != nil {
			r := *s.Records[i].Reference
			s.Records[i].Reference = &r
		}
	}
	return s
}
func (s *Store) read(ctx context.Context) (Snapshot, storeSource, error) {
	result := emptySnapshot()
	source := storeSource{}
	if s == nil || !filepath.IsAbs(s.Path) {
		return result, source, ErrUnsafe
	}
	anchor, e := safeCredentialParents(filepath.Dir(s.Path), false)
	if e != nil {
		return result, source, e
	}
	source.anchor = anchor
	source.anchorInfo, e = os.Lstat(anchor)
	if e != nil {
		return result, source, e
	}
	if anchor != filepath.Dir(s.Path) {
		return result, source, nil
	}
	root, held, e := safefile.OpenRoot(anchor)
	if e != nil {
		return result, source, e
	}
	defer root.Close()
	info, e := root.Lstat(filepath.Base(s.Path))
	if errors.Is(e, fs.ErrNotExist) {
		return result, source, nil
	}
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return result, source, ErrUnsafe
	}
	if e = credentialPrivate(s.Path, info, 0o600); e != nil {
		return result, source, e
	}
	body, info, e := safefile.ReadStableRegular(ctx, root, filepath.Base(s.Path), info, 1<<20)
	if e != nil {
		return result, source, e
	}
	if safefile.VerifyRoot(anchor, held) != nil {
		return result, source, ErrStale
	}
	decoder := toml.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&result); e != nil {
		return emptySnapshot(), source, ErrUnsafe
	}
	if e = validateSnapshot(result); e != nil {
		return result, source, e
	}
	source.info = info
	source.digest = sha256.Sum256(body)
	return result, source, nil
}
func (s *Store) Read(ctx context.Context) (Snapshot, error) { r, _, e := s.read(ctx); return r, e }
func validateSnapshot(s Snapshot) error {
	if s.SchemaVersion != 1 || len(s.Records) > 4096 {
		return ErrUnsafe
	}
	seen := map[string]bool{}
	for _, r := range s.Records {
		if r.Context.Validate() != nil || r.ID != r.Context.ID() || seen[r.ID] || r.Policy != PolicyAsk && r.Policy != PolicyNever || r.State != "ready" && r.State != "pending" && r.State != "unknown" || r.Reference != nil && !validReference(*r.Reference) || r.PendingProvider != "" && r.PendingProvider != "system" && r.PendingProvider != "bitwarden" {
			return ErrUnsafe
		}
		seen[r.ID] = true
	}
	return nil
}
func (s *Store) Lookup(ctx context.Context, c Context) (Record, bool, error) {
	if c.Validate() != nil {
		return Record{}, false, ErrUnsafe
	}
	snapshot, e := s.Read(ctx)
	if e != nil {
		return Record{}, false, e
	}
	for _, r := range snapshot.Records {
		if r.ID == c.ID() {
			return r, true, nil
		}
	}
	return Record{ID: c.ID(), Context: c, Policy: PolicyAsk, State: "ready"}, false, nil
}
func (s *Store) Plan(ctx context.Context, record Record) (Plan, error) {
	before, source, e := s.read(ctx)
	if e != nil {
		return Plan{}, e
	}
	after := cloneSnapshot(before)
	found := false
	for i, r := range after.Records {
		if r.ID == record.ID {
			after.Records[i] = record
			found = true
			break
		}
	}
	if !found {
		after.Records = append(after.Records, record)
	}
	sort.Slice(after.Records, func(i, j int) bool { return after.Records[i].ID < after.Records[j].ID })
	after.Revision++
	if e = validateSnapshot(after); e != nil {
		return Plan{}, e
	}
	return Plan{Before: cloneSnapshot(before), After: cloneSnapshot(after), state: &storePlanState{s, source, cloneSnapshot(before), cloneSnapshot(after)}}, nil
}
func sameStoreSource(a, b storeSource) bool {
	if (a.info == nil) != (b.info == nil) || a.digest != b.digest {
		return false
	}
	if a.info != nil && !os.SameFile(a.info, b.info) {
		return false
	}
	return true
}
func (s *Store) Apply(ctx context.Context, p Plan) (Snapshot, error) {
	if p.state == nil || p.state.store != s || !reflect.DeepEqual(p.Before, p.state.before) || !reflect.DeepEqual(p.After, p.state.after) {
		return Snapshot{}, ErrStale
	}
	if e := ctx.Err(); e != nil {
		return Snapshot{}, e
	}
	if safefile.VerifyRoot(p.state.source.anchor, p.state.source.anchorInfo) != nil {
		return Snapshot{}, ErrStale
	}
	if _, e := safeCredentialParents(filepath.Dir(s.Path), true); e != nil {
		return Snapshot{}, e
	}
	root, held, e := safefile.OpenRoot(filepath.Dir(s.Path))
	if e != nil {
		return Snapshot{}, e
	}
	defer root.Close()
	lock := filepath.Join(filepath.Dir(s.Path), ".ssh-credentials.lock")
	if info, e := os.Lstat(lock); e == nil {
		if !info.Mode().IsRegular() || credentialPrivate(lock, info, 0o600) != nil {
			return Snapshot{}, ErrUnsafe
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return Snapshot{}, e
	} else {
		file, e := root.OpenFile(filepath.Base(lock), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if e != nil {
			return Snapshot{}, e
		}
		e = credentialSetPrivate(lock, 0o600)
		e = errors.Join(e, file.Close())
		if e != nil {
			return Snapshot{}, e
		}
	}
	e = lockx.WithFile(ctx, lock, "SSH credential policy", func() error {
		if safefile.VerifyRoot(filepath.Dir(s.Path), held) != nil {
			return ErrStale
		}
		current, source, e := s.read(ctx)
		if e != nil {
			return e
		}
		if !sameStoreSource(p.state.source, source) || !reflect.DeepEqual(current, p.state.before) {
			return ErrStale
		}
		body, e := toml.Marshal(p.state.after)
		if e != nil || len(body) > 1<<20 {
			return ErrUnsafe
		}
		var nonce [16]byte
		if _, e = rand.Read(nonce[:]); e != nil {
			return e
		}
		name := ".ssh-credentials-" + hex.EncodeToString(nonce[:]) + ".tmp"
		file, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if e != nil {
			return e
		}
		defer root.Remove(name)
		tmpPath := filepath.Join(filepath.Dir(s.Path), name)
		if e = credentialSetPrivate(tmpPath, 0o600); e == nil {
			_, e = file.Write(body)
		}
		if e == nil {
			e = file.Sync()
		}
		e = errors.Join(e, file.Close())
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, source, e = s.read(ctx)
		if e != nil || !sameStoreSource(p.state.source, source) || !reflect.DeepEqual(current, p.state.before) || safefile.VerifyRoot(filepath.Dir(s.Path), held) != nil {
			return ErrStale
		}
		if e = root.Rename(name, filepath.Base(s.Path)); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return Snapshot{}, e
	}
	return s.Read(ctx)
}
func (s *Store) SavePreference(ctx context.Context, c Context, policy Policy) (Record, error) {
	r, _, e := s.Lookup(ctx, c)
	if e != nil {
		return r, e
	}
	r.Policy = policy
	p, e := s.Plan(ctx, r)
	if e != nil {
		return r, e
	}
	_, e = s.Apply(ctx, p)
	return r, e
}
func safeCredentialParents(path string, create bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrUnsafe
	}
	var missing []string
	current := path
	for {
		info, e := os.Lstat(current)
		if e == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || credentialAncestor(current, info) != nil {
				return "", ErrUnsafe
			}
			if current != filepath.Dir(current) {
				if _, e = safeCredentialParents(filepath.Dir(current), false); e != nil {
					return "", e
				}
			}
			break
		}
		if !errors.Is(e, fs.ErrNotExist) {
			return "", e
		}
		missing = append(missing, current)
		next := filepath.Dir(current)
		if next == current {
			return "", ErrUnsafe
		}
		current = next
	}
	anchor := current
	if !create {
		return anchor, nil
	}
	for i := len(missing) - 1; i >= 0; i-- {
		e := os.Mkdir(missing[i], 0o700)
		if e != nil && !errors.Is(e, fs.ErrExist) {
			return "", e
		}
		if e == nil {
			if e = credentialSetPrivate(missing[i], 0o700); e != nil {
				return "", e
			}
		}
		info, e := os.Lstat(missing[i])
		if e != nil || credentialPrivate(missing[i], info, 0o700) != nil {
			return "", ErrUnsafe
		}
	}
	return path, nil
}
