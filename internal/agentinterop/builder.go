package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

type builder struct {
	ctx      context.Context
	st       *store
	r        record
	roots    map[string]*os.Root
	observed map[Location]image
	data     map[Location][]byte
	planned  map[Location]image
	total    int
}

func newBuilder(ctx context.Context, st *store, req TransferRequest) *builder {
	return &builder{ctx: ctx, st: st, r: record{Schema: SchemaVersion, ID: uuid.NewString(), Request: req, Status: "planned", Roots: map[string]string{}, Guards: []observation{}, Effects: []effect{}, Notes: []string{}}, roots: map[string]*os.Root{}, observed: map[Location]image{}, data: map[Location][]byte{}, planned: map[Location]image{}}
}
func (b *builder) close() {
	for _, r := range b.roots {
		_ = r.Close()
	}
}

func (b *builder) root(path string) (*os.Root, error) {
	if r := b.roots[path]; r != nil {
		return r, nil
	}
	r, info, err := safefile.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	id, err := fileIdentity(info)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	b.roots[path] = r
	b.r.Roots[path] = id
	return r, nil
}

func (b *builder) read(l Location) (image, []byte, error) {
	if err := validLocation(l); err != nil {
		return image{}, nil, err
	}
	if v, ok := b.observed[l]; ok {
		return v, b.data[l], nil
	}
	if parent := filepath.Dir(l.Path); l.Path != "." && parent != "." {
		if _, _, err := b.read(Location{Root: l.Root, Path: parent}); err != nil {
			return image{}, nil, err
		}
	}
	r, err := b.root(l.Root)
	if err != nil {
		return image{}, nil, err
	}
	v, data, err := snapshot(b.ctx, r, l.Path, b.st.key)
	if err != nil {
		return image{}, nil, err
	}
	b.total += len(data)
	if b.total > maxTotalBytes || len(b.observed) > 4*maxFiles {
		return image{}, nil, errors.New("transfer observation limit exceeded")
	}
	b.observed[l] = v
	b.data[l] = data
	b.r.Guards = append(b.r.Guards, observation{Location: l, Image: v})
	return v, data, nil
}

func (b *builder) current(l Location) (image, error) {
	if v, ok := b.planned[l]; ok {
		return v, nil
	}
	v, _, err := b.read(l)
	return v, err
}

func (b *builder) parents(l Location) error {
	parent := filepath.Dir(l.Path)
	if parent == "." {
		return nil
	}
	p := Location{Root: l.Root, Path: parent}
	v, err := b.current(p)
	if err != nil {
		return err
	}
	if v.Kind == "dir" {
		return nil
	}
	if v.Kind != "absent" {
		return ErrConflict
	}
	if err = b.parents(p); err != nil {
		return err
	}
	return b.change(p, image{Kind: "dir", Mode: 0o755}, nil)
}

func (b *builder) change(l Location, after image, data []byte) error {
	if l.Path == "." {
		return errors.New("cannot replace a scope root")
	}
	before, err := b.current(l)
	if err != nil {
		return err
	}
	if _, exists := b.planned[l]; exists {
		return errors.New("overlapping transfer effects")
	}
	if before.Kind == "absent" {
		parent := Location{Root: l.Root, Path: filepath.Dir(l.Path)}
		v, data, e := b.read(parent)
		if e != nil {
			return e
		}
		if v.Kind == "dir" {
			var names []string
			_ = json.Unmarshal(data, &names)
			for _, name := range names {
				if strings.EqualFold(name, filepath.Base(l.Path)) && name != filepath.Base(l.Path) {
					return errors.New("destination has a case-insensitive name collision")
				}
			}
		}
	}
	if after.Kind == "file" {
		after.Digest = digest(b.st.key, data)
	}
	if after.Kind == "link" {
		after.Digest = digest(b.st.key, []byte(after.Link))
	}
	if sameContent(before, after) {
		return nil
	}
	if before.Kind == "file" {
		before.Blob, err = b.st.put(b.ctx, b.data[l])
		if err != nil {
			return err
		}
	}
	if after.Kind == "file" {
		after.Blob, err = b.st.put(b.ctx, data)
		if err != nil {
			return err
		}
	}
	if len(b.r.Effects) >= 4*maxFiles {
		return errors.New("transfer effect limit exceeded")
	}
	b.r.Effects = append(b.r.Effects, effect{Location: l, Before: before, After: after})
	b.planned[l] = after
	return nil
}

func (b *builder) write(l Location, data []byte, mode fs.FileMode, replace bool) error {
	if len(data) > int(maxBytes) {
		return errors.New("transfer file limit exceeded")
	}
	v, err := b.current(l)
	if err != nil {
		return err
	}
	if v.Kind != "absent" && v.Kind != "file" {
		return ErrConflict
	}
	if v.Kind == "file" {
		mode = v.Mode
		if !replace && v.Digest != digest(b.st.key, data) {
			return ErrConflict
		}
	}
	if err = b.parents(l); err != nil {
		return err
	}
	return b.change(l, image{Kind: "file", Mode: mode}, data)
}

func (b *builder) link(l Location, target string, adopt bool) error {
	v, err := b.current(l)
	if err != nil {
		return err
	}
	if v.Kind == "link" && v.Link == target {
		return nil
	}
	if v.Kind != "absent" && !(adopt && v.Kind == "file") {
		return ErrConflict
	}
	if filepath.IsAbs(target) {
		return errors.New("mirror links must be relative")
	}
	if err = b.parents(l); err != nil {
		return err
	}
	return b.change(l, image{Kind: "link", Mode: 0o777, Link: target}, nil)
}

func (b *builder) remove(l Location) error {
	v, err := b.current(l)
	if err != nil {
		return err
	}
	if v.Kind == "absent" {
		return nil
	}
	return b.change(l, image{Kind: "absent"}, nil)
}

func (b *builder) save() (Plan, error) {
	if len(b.r.Effects) == 0 && b.r.Status == "planned" {
		b.r.Notes = append(b.r.Notes, "Already equivalent; no ownership is claimed for existing content.")
	}
	b.r.Notes = append(b.r.Notes, "Recovery payloads are private local state; undo revalidates the resulting files before restoring them.")
	if err := b.st.save(b.ctx, &b.r, true); err != nil {
		return Plan{}, err
	}
	return b.r.public(), nil
}

func location(ref ArtifactRef) Location { return Location{Root: ref.Root, Path: ref.Path} }

func conflict(field string) error { return fmt.Errorf("%s: %w", field, ErrConflict) }
