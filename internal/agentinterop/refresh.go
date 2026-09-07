package agentinterop

import (
	"context"
	"errors"
)

func (s Service) Inspect(ctx context.Context, id string) (ApplyResult, error) {
	st, err := s.open(false)
	if err != nil {
		return ApplyResult{}, err
	}
	defer st.close()
	r, err := st.load(ctx, id)
	return r.result(), err
}

func (s Service) Refresh(ctx context.Context, id string) (out Plan, err error) {
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
		if r.Request.Mode != "mirror" || r.Status != "applied" {
			return errors.New("refresh requires an applied mirror relation")
		}
		owner := r
		for n := 0; len(owner.Effects) == 0 && owner.Parent != ""; n++ {
			if n >= 32 {
				return errors.New("mirror history limit exceeded")
			}
			owner, e = st.load(ctx, owner.Parent)
			if e != nil {
				return e
			}
		}
		h, e := openRoots(owner)
		if e != nil {
			return e
		}
		defer h.close()
		if e = verifyPost(ctx, st, h, owner); e != nil {
			return e
		}
		b := newBuilder(ctx, st, r.Request)
		defer b.close()
		b.r.Parent = id
		if e = buildTransfer(b, &owner); e != nil {
			return e
		}
		out, e = b.save()
		return e
	})
	return out, err
}
