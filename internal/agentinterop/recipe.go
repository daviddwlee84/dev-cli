package agentinterop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type recipeRef struct {
	Scope string `toml:"scope"`
	Agent string `toml:"agent,omitempty"`
	Path  string `toml:"path,omitempty"`
}
type recipeTransfer struct {
	Kind       string      `toml:"kind"`
	Mode       string      `toml:"mode"`
	Name       string      `toml:"name,omitempty"`
	TargetName string      `toml:"target_name,omitempty"`
	From       recipeRef   `toml:"from"`
	To         recipeRef   `toml:"to"`
	Style      string      `toml:"style,omitempty"`
	Transport  string      `toml:"transport,omitempty"`
	Bindings   []SecretRef `toml:"bindings,omitempty"`
}
type recipeDocument struct {
	Version         int                       `toml:"version"`
	ProviderProfile string                    `toml:"provider_profile,omitempty"`
	Transfers       map[string]recipeTransfer `toml:"transfers"`
}

// ExportRecipe returns an optional, reviewable native-source recipe. The caller
// chooses where to save it; exporting does not overwrite a repository file.
func (s Service) ExportRecipe(ctx context.Context, id, entry string) ([]byte, error) {
	st, err := s.open(false)
	if err != nil {
		return nil, err
	}
	defer st.close()
	r, err := st.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != "applied" || (r.Request.Mode != "copy" && r.Request.Mode != "mirror") {
		return nil, errors.New("portable recipes describe applied copy/mirror relations; upstream installs already have native locks")
	}
	req := r.Request
	if req.From.Scope == "project" && req.To.Scope == "project" && req.From.Root != req.To.Root {
		return nil, errors.New("cross-project absolute dependencies are not portable; retain native copied content or upstream skills locks")
	}
	if r.Bridge != nil || req.EnvFile != "" || req.Launcher != "" {
		return nil, errors.New("host-local credential/launcher bindings are not exported; reconstruct those explicitly on each host")
	}
	if entry == "" {
		entry = req.Kind
		if req.Name != "" {
			entry += "-" + req.Name
		}
	}
	if !safeText(entry) || entry == "" {
		return nil, errors.New("invalid recipe entry name")
	}
	portable := func(ref ArtifactRef) (recipeRef, error) {
		out := recipeRef{Scope: ref.Scope, Agent: ref.Agent, Path: filepath.ToSlash(ref.Path)}
		if ref.Scope == "user" {
			out.Path = ""
			if ref.Agent == "" {
				return out, errors.New("user recipes need a native agent identity")
			}
		}
		return out, nil
	}
	from, err := portable(req.From)
	if err != nil {
		return nil, err
	}
	to, err := portable(req.To)
	if err != nil {
		return nil, err
	}
	doc := recipeDocument{Version: 1, Transfers: map[string]recipeTransfer{entry: {Kind: req.Kind, Mode: req.Mode, Name: req.Name, TargetName: req.TargetName, From: from, To: to, Style: req.Style, Transport: req.Transport, Bindings: req.Bindings}}}
	if req.Kind == "skill" {
		doc.ProviderProfile = "skills@" + SkillsProviderVersion
	}
	var buf bytes.Buffer
	buf.WriteString("# Optional reconstruction intent. Native agent files remain the runtime configuration.\n# No credentials, checkout paths, ownership receipts, or launcher bindings are stored here.\n")
	if err = toml.NewEncoder(&buf).Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PlanRecipe selects exactly one entry. Re-plan each subsequent entry after
// applying the previous one; do not apply a batch of stale directory snapshots.
func (s Service) PlanRecipe(ctx context.Context, root, file, entry, kind string) (out Plan, err error) {
	root, err = filepath.Abs(root)
	if err != nil {
		return out, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return out, err
	}
	if filepath.IsAbs(file) {
		file, err = filepath.Rel(root, file)
		if err != nil {
			return out, err
		}
	}
	l := Location{root, file}
	if err = validLocation(l); err != nil {
		return out, err
	}
	err = s.withLock(ctx, func() error {
		st, e := s.open(true)
		if e != nil {
			return e
		}
		defer st.close()
		b := newBuilder(ctx, st, TransferRequest{})
		defer b.close()
		img, data, e := b.read(l)
		if e != nil {
			return e
		}
		if img.Kind != "file" {
			return errors.New("recipe must be a regular file in the selected checkout")
		}
		var doc recipeDocument
		meta, e := toml.Decode(string(data), &doc)
		if e != nil || doc.Version != 1 || len(meta.Undecoded()) != 0 || len(doc.Transfers) == 0 || len(doc.Transfers) > 64 {
			return errors.New("unsupported or malformed recipe schema")
		}
		if doc.ProviderProfile != "" && doc.ProviderProfile != "skills@"+SkillsProviderVersion {
			return errors.New("unsupported recipe provider profile")
		}
		if entry == "" {
			if len(doc.Transfers) != 1 {
				return errors.New("recipe has multiple transfers; select one --entry")
			}
			for name := range doc.Transfers {
				entry = name
			}
		}
		item, ok := doc.Transfers[entry]
		if !ok {
			return errors.New("recipe entry does not exist")
		}
		if item.Kind != kind {
			return errors.New("recipe entry belongs to another artifact family")
		}
		if item.Mode != "copy" && item.Mode != "mirror" {
			return errors.New("recipes support copy/mirror; moves and upstream preparation require explicit commands")
		}
		ref := func(r recipeRef) (ArtifactRef, error) {
			base := root
			if r.Scope == "user" || r.Scope == "global" {
				var e error
				base, e = os.UserHomeDir()
				if e != nil {
					return ArtifactRef{}, e
				}
				if r.Path != "" {
					return ArtifactRef{}, errors.New("user recipes resolve native agent locations, not arbitrary personal paths")
				}
			}
			return ArtifactRef{ScopeRef: ScopeRef{Scope: r.Scope, Root: base}, Agent: r.Agent, Path: filepath.FromSlash(r.Path)}, nil
		}
		from, e := ref(item.From)
		if e != nil {
			return e
		}
		to, e := ref(item.To)
		if e != nil {
			return e
		}
		req, e := normalize(TransferRequest{Kind: item.Kind, Mode: item.Mode, Name: item.Name, TargetName: item.TargetName, From: from, To: to, Style: item.Style, Transport: item.Transport, Bindings: item.Bindings})
		if e != nil {
			return e
		}
		b.r.Request = req
		if e = buildTransfer(b, nil); e != nil {
			return e
		}
		b.r.Notes = append(b.r.Notes, "Recipe entry was selected explicitly; its file identity is also bound to this plan.")
		out, e = b.save()
		return e
	})
	return out, err
}
