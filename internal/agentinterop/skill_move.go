package agentinterop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

type moveProof struct {
	lock  Location
	data  []byte
	entry skillLockEntry
	raw   json.RawMessage
}

func canonicalSkill(ref ArtifactRef, name string) (ArtifactRef, error) {
	if ref.Scope == "project" {
		ref.Path = filepath.Join(".agents", "skills", name)
		return ref, nil
	}
	home, _ := os.UserHomeDir()
	return absoluteArtifact(ref, filepath.Join(home, ".agents", "skills", name))
}

func sourceMoveProof(b *builder, source Location, entries []treeEntry) (*moveProof, error) {
	req := b.r.Request
	lock, err := lockLocation(req.From)
	if err != nil {
		return nil, err
	}
	v, data, err := b.read(lock)
	if err != nil {
		return nil, err
	}
	if v.Kind == "absent" {
		return nil, nil
	}
	doc, err := jsonDocument(data, false)
	if err != nil {
		return nil, err
	}
	if doc.Find(pointer("skills", req.Name)) == nil {
		return nil, nil
	}
	entry, raw, err := readLock(data, req.From.Scope, req.Name)
	if err != nil {
		return nil, err
	}
	if _, err = sourceArgument(entry); err != nil {
		return nil, err
	}
	canonical, err := canonicalSkill(req.From, req.Name)
	if err != nil {
		return nil, err
	}
	if location(canonical) != source {
		return nil, errors.New("provider source ownership is ambiguous outside its canonical tree; use an independent copy")
	}
	hash, tree, err := skillHashes(entries)
	if err != nil {
		return nil, err
	}
	if req.From.Scope == "project" && entry.ComputedHash != hash || req.From.Scope == "user" && entry.SkillFolderHash != tree {
		return nil, errors.New("provider-managed source differs from its lock; source retirement is blocked")
	}
	return &moveProof{lock, data, entry, raw}, nil
}

func rejectTargetMembership(b *builder, ref ArtifactRef, name string) error {
	lock, err := lockLocation(ref)
	if err != nil {
		return err
	}
	v, data, err := b.read(lock)
	if err != nil {
		return err
	}
	if v.Kind == "absent" {
		return nil
	}
	doc, err := jsonDocument(data, false)
	if err != nil {
		return err
	}
	if doc.Find(pointer("skills", name)) != nil {
		return conflict("destination already has upstream lock membership")
	}
	return nil
}

func addMoveMembership(b *builder, proof *moveProof, entries []treeEntry) error {
	req := b.r.Request
	if req.TargetName != req.Name {
		return errors.New("provider moves retain the locked skill name")
	}
	lock, err := lockLocation(req.To)
	if err != nil {
		return err
	}
	v, data, err := b.read(lock)
	if err != nil {
		return err
	}
	version := 1
	if req.To.Scope == "user" {
		version = 3
	}
	if v.Kind == "absent" {
		data = []byte(`{"version":1,"skills":{}}`)
		if version == 3 {
			data = []byte(`{"version":3,"skills":{}}`)
		}
	} else {
		doc, e := jsonDocument(data, false)
		if e != nil {
			return e
		}
		root, e := jsonMap(&doc)
		if e != nil {
			return e
		}
		var actual int
		if json.Unmarshal(root["version"], &actual) != nil || actual != version {
			return errors.New("destination lock schema is unsupported")
		}
		if doc.Find(pointer("skills", req.Name)) != nil {
			return conflict("destination lock membership")
		}
	}
	var entry map[string]any
	if json.Unmarshal(proof.raw, &entry) != nil {
		return errors.New("invalid source lock")
	}
	hash, tree, err := skillHashes(entries)
	if err != nil {
		return err
	}
	if version == 1 {
		delete(entry, "skillFolderHash")
		delete(entry, "installedAt")
		delete(entry, "updatedAt")
		entry["computedHash"] = hash
	} else {
		delete(entry, "computedHash")
		entry["skillFolderHash"] = tree
		entry["installedAt"] = time.Now().UTC().Format(time.RFC3339)
		entry["updatedAt"] = entry["installedAt"]
	}
	patched, err := patchJSON(data, false, []string{"skills", req.Name}, entry, false)
	if err != nil {
		return err
	}
	return b.write(lock, patched, 0o600, true)
}

func retireSkillSource(b *builder, source Location, entries []treeEntry, proof *moveProof) error {
	req := b.r.Request
	if source != location(req.From) {
		return b.remove(location(req.From))
	}
	// A known cross-project mirror is an explicit consumer; do not leave it
	// dangling or rewrite another checkout as a hidden effect of a move.
	f, err := b.st.root.Open(".")
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := b.st.load(b.ctx, strings.TrimSuffix(name, ".json"))
		if err != nil {
			return err
		}
		if r.Status != "applied" || r.Request.Kind != "skill" || r.Request.Mode != "mirror" {
			continue
		}
		if r.Request.From.Root == req.From.Root && r.Request.Name == req.Name && r.Request.To.Root != req.From.Root {
			return errors.New("source has an active cross-project mirror; undo or migrate that relation before moving the canonical skill")
		}
	}
	seen := map[Location]bool{}
	for _, agent := range agentskill.Registry() {
		ref := req.From
		var path string
		if ref.Scope == "project" {
			path = filepath.Join(ref.Root, agent.ProjectSkillsDir, req.Name)
		} else {
			if agent.GlobalSkillsDir == "" {
				continue
			}
			path = filepath.Join(agent.GlobalSkillsDir, req.Name)
		}
		ref, err = absoluteArtifact(ref, path)
		if err != nil {
			return err
		}
		l := location(ref)
		if l == source || seen[l] {
			continue
		}
		seen[l] = true
		v, _, err := b.read(l)
		if err != nil {
			return err
		}
		if v.Kind != "link" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(l.Root, l.Path))
		if err != nil {
			return errors.New("native skill projection is dangling or cyclic")
		}
		if resolved != filepath.Join(source.Root, source.Path) {
			continue
		}
		if proof == nil {
			return errors.New("unowned native skill links still reference the source; migrate them explicitly")
		}
		if err = b.remove(l); err != nil {
			return err
		}
	}
	if proof != nil {
		patched, err := patchJSON(proof.data, false, []string{"skills", req.Name}, nil, true)
		if err != nil {
			return err
		}
		if err = b.write(proof.lock, patched, 0o600, true); err != nil {
			return err
		}
		b.r.Effects[len(b.r.Effects)-1].Retirement = true
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if err = b.remove(Location{source.Root, filepath.Join(source.Path, entries[i].rel)}); err != nil {
			return err
		}
	}
	return nil
}
