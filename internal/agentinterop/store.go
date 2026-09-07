package agentinterop

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

type store struct {
	root *os.Root
	key  []byte
}

func (s Service) open(create bool) (*store, error) {
	if create {
		if err := platformTransfers(); err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(s.StateDir) {
		return nil, errors.New("interop state directory must be absolute")
	}
	// Recovery may contain an existing destination's unrelated credentials.
	// Never place that material in a checkout, even when task state was
	// intentionally configured to be Git-backed.
	if err := stateOutsideCheckout(s.StateDir); err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
			return nil, err
		}
	}
	r, info, err := safefile.OpenRoot(s.StateDir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*store, error) { _ = r.Close(); return nil, err }
	if info.Mode().Perm()&0o077 != 0 {
		return fail(errors.New("interop state directory must be owner-private (0700)"))
	}
	ctx := context.Background()
	key, _, err := safefile.ReadStableRegular(ctx, r, "key", nil, 32)
	if errors.Is(err, fs.ErrNotExist) && create {
		f, e := r.Open(".")
		if e != nil {
			return fail(e)
		}
		names, e := f.Readdirnames(-1)
		_ = f.Close()
		if e != nil {
			return fail(e)
		}
		if len(names) > 0 {
			return fail(errors.New("interop signing key is missing from existing state; preserve recovery objects rather than resetting authority"))
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return fail(err)
		}
		_, err = safefile.CreatePrivateNoClobber(ctx, r, "key", key, false)
		if errors.Is(err, fs.ErrExist) {
			key, _, err = safefile.ReadStableRegular(ctx, r, "key", nil, 32)
		}
	}
	if err != nil {
		return fail(err)
	}
	info, err = r.Lstat("key")
	if err != nil || len(key) != 32 || info.Mode().Perm()&0o077 != 0 {
		return fail(errors.New("invalid private interop key"))
	}
	return &store{root: r, key: key}, nil
}

func stateOutsideCheckout(path string) error {
	parent := path
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	for {
		if _, err := os.Lstat(filepath.Join(resolved, ".git")); err == nil {
			return errors.New("private interop state cannot be inside a Git checkout; configure paths.state_dir outside the codebase")
		} else if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(resolved)
		if next == resolved {
			break
		}
		resolved = next
	}
	return nil
}

func (st *store) close() { _ = st.root.Close() }

func (st *store) put(ctx context.Context, data []byte) (string, error) {
	if len(data) > int(maxBytes) {
		return "", errors.New("transfer file limit exceeded")
	}
	id := uuid.NewString() + ".blob"
	_, err := safefile.CreatePrivateNoClobber(ctx, st.root, id, data, false)
	return id, err
}

func (st *store) bytes(ctx context.Context, img image) ([]byte, error) {
	if img.Kind != "file" {
		return nil, nil
	}
	if filepath.Ext(img.Blob) != ".blob" || !validID(img.Blob[:len(img.Blob)-5]) {
		return nil, errors.New("invalid recovery object reference")
	}
	b, info, err := safefile.ReadStableRegular(ctx, st.root, img.Blob, nil, maxBytes)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 || digest(st.key, b) != img.Digest {
		return nil, errors.New("recovery object failed validation")
	}
	return b, nil
}

func validID(id string) bool { u, err := uuid.Parse(id); return err == nil && u.String() == id }

func (st *store) save(ctx context.Context, r *record, create bool) error {
	r.MAC = ""
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	r.MAC = digest(st.key, b)
	b, err = json.Marshal(r)
	if err != nil {
		return err
	}
	name := r.ID + ".json"
	if create {
		_, err = safefile.CreatePrivateNoClobber(ctx, st.root, name, b, false)
		return err
	}
	_, info, err := safefile.ReadStableRegular(ctx, st.root, name, nil, maxBytes)
	if err != nil {
		return err
	}
	_, err = safefile.AtomicReplacePrivate(ctx, st.root, name, info, b, false)
	return err
}

func (st *store) load(ctx context.Context, id string) (record, error) {
	var r record
	if !validID(id) {
		return r, errors.New("invalid transfer ID")
	}
	b, info, err := safefile.ReadStableRegular(ctx, st.root, id+".json", nil, maxBytes)
	if err != nil {
		return r, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return r, errors.New("transfer record is not private")
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, errors.New("invalid transfer record")
	}
	mac := r.MAC
	r.MAC = ""
	b, _ = json.Marshal(r)
	if mac != digest(st.key, b) || r.Schema != SchemaVersion || r.ID != id {
		return r, errors.New("transfer record failed integrity/version check")
	}
	r.MAC = mac
	return r, nil
}

// withLock uses the existing provider lease, so local link/move operations and
// older explicit skills add/update commands serialize on the same host.
func (s Service) withLock(ctx context.Context, fn func() error) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return lockx.WithDir(ctx, filepath.Join(cache, "dev-cli", "skill-mutation"), "agent transfer", fn)
}

func (s Service) Status(ctx context.Context, kind string) ([]ApplyResult, error) {
	st, err := s.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return []ApplyResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer st.close()
	f, err := st.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := []ApplyResult{}
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		r, err := st.load(ctx, name[:len(name)-5])
		if err != nil {
			return nil, err
		}
		if kind != "" && r.Request.Kind != kind {
			continue
		}
		if r.InFlight != nil {
			r.Status = "interrupted-unknown"
		}
		out = append(out, r.result())
	}
	return out, nil
}
