package agentinterop

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type heldRoots struct {
	roots map[string]*os.Root
	infos map[string]fs.FileInfo
}

func openRoots(r record) (*heldRoots, error) {
	h := &heldRoots{roots: map[string]*os.Root{}, infos: map[string]fs.FileInfo{}}
	for path, id := range r.Roots {
		root, info, err := safefile.OpenRoot(path)
		if err != nil {
			h.close()
			return nil, ErrStale
		}
		actual, err := rootIdentity(path, info)
		if err != nil || actual != id {
			_ = root.Close()
			h.close()
			return nil, ErrStale
		}
		h.roots[path] = root
		h.infos[path] = info
	}
	return h, nil
}
func (h *heldRoots) close() {
	for _, r := range h.roots {
		_ = r.Close()
	}
}
func (h *heldRoots) verify() error {
	for path, info := range h.infos {
		if safefile.VerifyRoot(path, info) != nil {
			return ErrStale
		}
	}
	return nil
}

func verifyPost(ctx context.Context, st *store, h *heldRoots, r record) error {
	for i := 0; i < r.Completed; i++ {
		e := r.Effects[i]
		if e.Post == nil {
			return errors.New("operation has unconfirmed effects")
		}
		v, _, err := snapshot(ctx, h.roots[e.Root], e.Path, st.key)
		if err != nil || !sameImage(v, *e.Post) {
			return ErrStale
		}
	}
	return nil
}

func verifyAncestors(ctx context.Context, st *store, h *heldRoots, r record, l Location) error {
	for parent := filepath.Dir(l.Path); parent != "."; parent = filepath.Dir(parent) {
		pl := Location{Root: l.Root, Path: parent}
		var expected *image
		for _, g := range r.Guards {
			if g.Location == pl {
				v := g.Image
				expected = &v
				break
			}
		}
		for _, e := range r.Effects[:r.Completed] {
			if e.Location == pl && e.Post != nil {
				expected = e.Post
			}
		}
		if expected == nil || expected.Kind != "dir" {
			return ErrStale
		}
		v, _, err := snapshot(ctx, h.roots[l.Root], parent, st.key)
		if err != nil || v.Kind != "dir" || v.Identity != expected.Identity || v.Mode != expected.Mode {
			return ErrStale
		}
	}
	return nil
}

// Apply locks, reopens all scope roots, and checks every observation before the
// first target mutation. The journal is synced before and after every effect.
func (s Service) Apply(ctx context.Context, id string) (out ApplyResult, err error) {
	if err = platformTransfers(); err != nil {
		return out, err
	}
	err = s.withLock(ctx, func() error {
		st, e := s.open(false)
		if e != nil {
			return e
		}
		defer st.close()
		r, e := st.load(ctx, id)
		if e != nil {
			return e
		}
		out = r.result()
		h, e := openRoots(r)
		if e != nil {
			return e
		}
		defer h.close()
		if r.Status == "applied" {
			return verifyReceipt(ctx, st, h, r)
		}
		if r.Status != "planned" || r.Completed != 0 || r.InFlight != nil {
			return errors.New("operation is no longer a fresh plan; inspect status and create an undo plan")
		}
		if r.Request.Kind == "mcp" && r.Request.Mode != "undo" {
			if e = validateMCPPolicy(ctx, r.Request); e != nil {
				return e
			}
		}
		if r.Parent != "" {
			parent, e := st.load(ctx, r.Parent)
			if e != nil {
				return e
			}
			if parent.Status != "applied" && !(r.Request.Mode == "undo" && parent.Status == "partial") {
				return ErrStale
			}
		}
		for _, g := range r.Guards {
			if e = validLocation(g.Location); e != nil {
				return e
			}
			root := h.roots[g.Root]
			if root == nil {
				return errors.New("unbound transfer root")
			}
			v, _, e := snapshot(ctx, root, g.Path, st.key)
			if e != nil || !sameImage(v, g.Image) {
				return ErrStale
			}
		}
		for _, eff := range r.Effects {
			if e = validLocation(eff.Location); e != nil {
				return e
			}
			if _, e = st.bytes(ctx, eff.After); e != nil {
				return e
			}
			if eff.Before.Kind == "file" {
				if _, e = st.bytes(ctx, eff.Before); e != nil {
					return e
				}
			}
		}
		r.Status = "applying"
		if e = st.save(ctx, &r, false); e != nil {
			return e
		}
		fail := func(cause error) error {
			r.Status = "partial"
			if r.InFlight != nil {
				r.Status = "interrupted-unknown"
			}
			writeErr := st.save(context.WithoutCancel(ctx), &r, false)
			out = r.result()
			return errors.Join(cause, writeErr)
		}
		for i := range r.Effects {
			eff := r.Effects[i]
			if eff.After.Kind == "absent" || eff.Retirement {
				if e = verifyPost(ctx, st, h, r); e != nil {
					return fail(e)
				}
			}
			if e = h.verify(); e != nil {
				return fail(e)
			}
			if e = verifyAncestors(ctx, st, h, r, eff.Location); e != nil {
				return fail(e)
			}
			v, _, e := snapshot(ctx, h.roots[eff.Root], eff.Path, st.key)
			if e != nil {
				return fail(e)
			}
			matches := sameImage(v, eff.Before)
			if eff.Before.Kind == "dir" {
				matches = v.Kind == "dir" && v.Identity == eff.Before.Identity && v.Mode == eff.Before.Mode
			}
			if !matches {
				return fail(ErrStale)
			}
			data, e := st.bytes(ctx, eff.After)
			if e != nil {
				return fail(e)
			}
			index := i
			r.InFlight = &index
			if e = st.save(ctx, &r, false); e != nil {
				return fail(e)
			}
			if e = publish(ctx, h.roots[eff.Root], eff, data); e != nil {
				return fail(fmt.Errorf("transfer effect %d was not confirmed: %w", i+1, e))
			}
			if s.hooks != nil && s.hooks.afterPublish != nil {
				if e = s.hooks.afterPublish(i); e != nil {
					return fail(e)
				}
			}
			post, _, e := snapshot(ctx, h.roots[eff.Root], eff.Path, st.key)
			if e != nil {
				return fail(e)
			}
			matches = sameContent(post, eff.After)
			if eff.After.Kind == "dir" {
				matches = post.Kind == "dir" && post.Mode == eff.After.Mode
			}
			if eff.After.Kind == "link" {
				matches = post.Kind == "link" && post.Link == eff.After.Link
			}
			if !matches {
				return fail(errors.New("published transfer content could not be confirmed"))
			}
			r.Effects[i].Post = &post
			r.Completed = i + 1
			r.InFlight = nil
			// Directory membership changes as later reviewed children are written.
			for j := 0; j < r.Completed; j++ {
				p := r.Effects[j].Post
				if p != nil && p.Kind == "dir" {
					v, _, e := snapshot(ctx, h.roots[r.Effects[j].Root], r.Effects[j].Path, st.key)
					if e != nil {
						return fail(e)
					}
					r.Effects[j].Post = &v
				}
			}
			if e = st.save(ctx, &r, false); e != nil {
				return fail(e)
			}
			if s.hooks != nil && s.hooks.afterConfirm != nil {
				if e = s.hooks.afterConfirm(i); e != nil {
					return fail(e)
				}
			}
		}
		r.Status = "applied"
		e = st.save(ctx, &r, false)
		out = r.result()
		if e == nil {
			e = updateRelationship(ctx, st, r)
		}
		return e
	})
	if errors.Is(err, ErrStale) && out.Completed == 0 && !out.Uncertain {
		out.Status = "stale"
	}
	return out, err
}

// Undo creates a reverse plan; it never directly restores files. Unconfirmed
// crash effects are retained for manual recovery rather than claimed as owned.
func (s Service) Undo(ctx context.Context, id string) (out Plan, err error) {
	err = s.withLock(ctx, func() error {
		st, e := s.open(false)
		if e != nil {
			return e
		}
		defer st.close()
		r, e := st.load(ctx, id)
		if e != nil {
			return e
		}
		if r.InFlight != nil {
			return errors.New("unconfirmed effect: preserve private recovery objects and inspect the affected path before further changes")
		}
		if r.Status != "applied" && r.Status != "partial" {
			return errors.New("only applied or confirmed partial operations can be undone")
		}
		h, e := openRoots(r)
		if e != nil {
			return e
		}
		defer h.close()
		if e = verifyReceipt(ctx, st, h, r); e != nil {
			return e
		}
		req := r.Request
		req.Mode = "undo"
		b := newBuilder(ctx, st, req)
		defer b.close()
		b.r.Parent = id
		if r.Request.Kind == "mcp" && len(r.MCPEdits) > 0 {
			if e = planMCPUndo(b, r); e != nil {
				return e
			}
			out, e = b.save()
			return e
		}
		for i := r.Completed - 1; i >= 0; i-- {
			eff := r.Effects[i]
			data, e := st.bytes(ctx, eff.Before)
			if e != nil {
				return e
			}
			after := eff.Before
			after.Identity = ""
			after.Blob = ""
			if e = b.change(eff.Location, after, data); e != nil {
				return e
			}
		}
		out, e = b.save()
		return e
	})
	return out, err
}
