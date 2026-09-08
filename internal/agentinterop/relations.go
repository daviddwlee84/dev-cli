package agentinterop

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Relationship is an historical ownership receipt, not a current filesystem,
// client-loading, connectivity, or authentication observation.
type Relationship struct {
	ID            string      `json:"id"`
	Kind          string      `json:"kind"`
	Mode          string      `json:"mode"`
	RecordedState string      `json:"recorded_state"`
	Name          string      `json:"name,omitempty"`
	Source        ArtifactRef `json:"source"`
	Destination   ArtifactRef `json:"destination"`
}

func (s Service) Relationships(ctx context.Context) ([]Relationship, error) {
	st, err := s.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return []Relationship{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer st.close()
	f, err := st.root.Open(".")
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := []Relationship{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		r, e := st.load(ctx, strings.TrimSuffix(name, ".json"))
		if e != nil {
			return nil, e
		}
		if r.Status != "applied" || r.Request.Mode == "undo" || len(r.Effects) == 0 && len(r.MCPEdits) == 0 {
			continue
		}
		out = append(out, Relationship{ID: r.ID, Kind: r.Request.Kind, Mode: r.Request.Mode, RecordedState: r.Status, Name: r.Request.TargetName, Source: r.Request.From, Destination: r.Request.To})
	}
	return out, nil
}

func (r Relationship) DestinationPath() string {
	return filepath.Join(r.Destination.Root, r.Destination.Path)
}

func updateRelationship(ctx context.Context, st *store, r record) error {
	if r.Parent == "" {
		return nil
	}
	if r.Request.Mode != "undo" && len(r.Effects) == 0 && len(r.MCPEdits) == 0 {
		return nil
	}
	parent, err := st.load(ctx, r.Parent)
	if err != nil {
		return err
	}
	if r.Request.Mode == "undo" {
		parent.Status = "undone"
	} else if r.Request.Mode == "mirror" || r.Request.Mode == "move" {
		parent.Status = "superseded"
	} else {
		return nil
	}
	if err = st.save(ctx, &parent, false); err != nil {
		return err
	}
	// Undoing a refresh restores the preceding relation only when its exact
	// content is present again. Publication changed inode identities, so record
	// those newly observed identities after checking the old content.
	if r.Request.Mode == "undo" && parent.Parent != "" {
		previous, err := st.load(ctx, parent.Parent)
		if err != nil {
			return err
		}
		if previous.Status != "superseded" {
			return nil
		}
		h, err := openRoots(previous)
		if err != nil {
			return err
		}
		defer h.close()
		if previous.Request.Kind == "mcp" && len(previous.MCPEdits) > 0 {
			if err = verifyReceipt(ctx, st, h, previous); err != nil {
				return nil
			}
			previous.Status = "applied"
			return st.save(ctx, &previous, false)
		}
		for i := 0; i < previous.Completed; i++ {
			e := previous.Effects[i]
			if e.Post == nil {
				return nil
			}
			post, _, err := snapshot(ctx, h.roots[e.Root], e.Path, st.key)
			if err != nil || !sameContent(post, *e.Post) {
				return nil
			}
			previous.Effects[i].Post = &post
		}
		previous.Status = "applied"
		return st.save(ctx, &previous, false)
	}
	return nil
}
