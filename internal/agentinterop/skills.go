package agentinterop

import (
	"bytes"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"go.yaml.in/yaml/v3"
)

var knownKey = regexp.MustCompile(`(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{25,}|github_pat_[A-Za-z0-9_]{25,}|AKIA[0-9A-Z]{16})`)
var privatePEM = regexp.MustCompile(`(?m)-----BEGIN (?:RSA |DSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----\r?\n(?:[A-Za-z0-9+/=]{16,}\r?\n)+-----END (?:RSA |DSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)

func secretMaterial(path string, data []byte) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, name := range []string{".credentials.json", "application_default_credentials.json", ".auth.json", "token.json", "tokens.json", "oauth-tokens.json", "id_dsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519_sk"} {
		if base == name {
			return true
		}
	}
	for _, extension := range []string{".ppk", ".p12", ".pfx"} {
		if strings.HasSuffix(base, extension) {
			return true
		}
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") && base != ".env.example" && base != ".env.sample" || base == "id_rsa" || base == "id_ed25519" || base == "auth.json" || base == "credentials.json" {
		return true
	}
	return knownKey.Match(data) || privatePEM.Match([]byte(strings.ReplaceAll(string(data), `\n`, "\n")))
}

type treeEntry struct {
	rel  string
	img  image
	data []byte
}

func (b *builder) tree(l Location) ([]treeEntry, error) {
	out := []treeEntry{}
	var paths []string
	var walk func(Location, string) error
	walk = func(at Location, rel string) error {
		img, data, err := b.read(at)
		if err != nil {
			return err
		}
		if img.Kind != "file" && img.Kind != "dir" {
			return errors.New("skill trees must contain only regular files and directories")
		}
		if img.Kind == "file" && secretMaterial(rel, data) {
			return ErrCredentials
		}
		if len(out) >= maxFiles {
			return errors.New("skill file limit exceeded")
		}
		out = append(out, treeEntry{rel, img, data})
		if rel != "." {
			paths = append(paths, filepath.ToSlash(rel))
		}
		if img.Kind == "dir" {
			r, err := b.root(at.Root)
			if err != nil {
				return err
			}
			parent, leaf, done, err := openParent(r, at.Path)
			if err != nil {
				return err
			}
			defer done()
			child, err := parent.OpenRoot(leaf)
			if err != nil {
				return err
			}
			defer child.Close()
			f, err := child.Open(".")
			if err != nil {
				return err
			}
			defer f.Close()
			names, err := f.Readdirnames(-1)
			if err != nil {
				return err
			}
			sort.Strings(names)
			for _, name := range names {
				if err = walk(Location{at.Root, filepath.Join(at.Path, name)}, filepath.Join(rel, name)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(l, "."); err != nil {
		return nil, err
	}
	if err := pathx.ValidatePortablePathSet(paths, pathx.PortablePathLimits{MaxPathBytes: 4096, MaxComponentBytes: 255, MaxDepth: 64}); err != nil {
		return nil, err
	}
	return out, nil
}

func validateSkill(entries []treeEntry) error {
	for _, e := range entries {
		if e.rel != "SKILL.md" {
			continue
		}
		data := strings.ReplaceAll(string(e.data), "\r\n", "\n")
		if !strings.HasPrefix(data, "---\n") {
			return errors.New("SKILL.md needs YAML frontmatter")
		}
		end := strings.Index(data[4:], "\n---")
		if end < 0 || end > 64<<10 {
			return errors.New("invalid skill frontmatter")
		}
		var fm struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if yaml.Unmarshal([]byte(data[4:4+end]), &fm) != nil || strings.TrimSpace(fm.Name) == "" || strings.TrimSpace(fm.Description) == "" || !safeText(fm.Name) {
			return errors.New("skill name and description are required")
		}
		return nil
	}
	return errors.New("selected source has no SKILL.md")
}

func resolveSkillSource(b *builder, l Location) (Location, error) {
	v, _, err := b.read(l)
	if err != nil {
		return l, err
	}
	if v.Kind != "link" {
		return l, nil
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(l.Root, l.Path))
	if err != nil {
		return l, errors.New("skill source link is dangling or cyclic")
	}
	rel, err := filepath.Rel(l.Root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return l, errors.New("skill source leaves selected scope; select its actual source explicitly")
	}
	return Location{l.Root, rel}, nil
}

func planSkill(b *builder, owner *record) error {
	req := b.r.Request
	if req.Name == "dev-cli" {
		return errors.New("dev-cli is managed by the bundled skill installer")
	}
	if req.Mode == "install" {
		return planPreparedSkill(b)
	}
	source, err := resolveSkillSource(b, location(req.From))
	if err != nil {
		return err
	}
	target := location(req.To)
	if source == target {
		return nil
	}
	srcAbs, dstAbs := filepath.Join(source.Root, source.Path), filepath.Join(target.Root, target.Path)
	if pathInside(srcAbs, dstAbs) || pathInside(dstAbs, srcAbs) {
		return errors.New("skill source and destination overlap")
	}
	entries, err := b.tree(source)
	if err != nil {
		return err
	}
	if err = validateSkill(entries); err != nil {
		return err
	}
	var proof *moveProof
	if req.Mode == "move" {
		proof, err = sourceMoveProof(b, source, entries)
		if err != nil {
			return err
		}
		if proof != nil {
			canonical, e := canonicalSkill(req.To, req.TargetName)
			if e != nil {
				return e
			}
			target = location(canonical)
			if target == source {
				return errors.New("universal canonical skills cannot be hidden from one agent by moving them; use mirror")
			}
		}
	}
	if req.From.Root != req.To.Root || req.From.Scope != req.To.Scope {
		if err = rejectTargetMembership(b, req.To, req.TargetName); err != nil {
			return err
		}
	}
	if req.Mode != "mirror" {
		v, _, e := b.read(target)
		if e != nil {
			return e
		}
		if v.Kind != "absent" {
			if v.Kind != "dir" || req.Mode == "move" {
				return conflict("skill target")
			}
			other, e := b.tree(target)
			if e != nil {
				return e
			}
			if len(other) != len(entries) {
				return conflict("skill target tree")
			}
			for i := range entries {
				if entries[i].rel != other[i].rel || entries[i].img.Kind != other[i].img.Kind || entries[i].img.Mode != other[i].img.Mode || !bytes.Equal(entries[i].data, other[i].data) {
					return conflict("skill target tree")
				}
			}
			return nil
		}
	}
	if req.Mode == "mirror" {
		rel, err := filepath.Rel(filepath.Dir(dstAbs), srcAbs)
		if err != nil {
			return err
		}
		b.r.Notes = append(b.r.Notes, "Symlink changes are visible to every consumer; this does not verify client discovery.")
		return b.link(target, rel, false)
	}
	for _, entry := range entries {
		at := Location{target.Root, filepath.Join(target.Path, entry.rel)}
		if entry.img.Kind == "dir" {
			v, err := b.current(at)
			if err != nil {
				return err
			}
			if v.Kind == "dir" {
				continue
			}
			if v.Kind != "absent" {
				return conflict("skill directory")
			}
			if err = b.parents(at); err != nil {
				return err
			}
			if err = b.change(at, image{Kind: "dir", Mode: entry.img.Mode}, nil); err != nil {
				return err
			}
		} else if err = b.write(at, entry.data, entry.img.Mode, false); err != nil {
			return err
		}
	}
	if req.Mode == "move" {
		if proof != nil {
			if err = addMoveMembership(b, proof, entries); err != nil {
				return err
			}
			if target != location(req.To) {
				rel, e := filepath.Rel(filepath.Dir(filepath.Join(req.To.Root, req.To.Path)), filepath.Join(target.Root, target.Path))
				if e != nil {
					return e
				}
				if err = b.link(location(req.To), rel, false); err != nil {
					return err
				}
			}
		}
		if err = retireSkillSource(b, source, entries, proof); err != nil {
			return err
		}
	}
	b.r.Notes = append(b.r.Notes, "Independent copies retain their bytes; no upstream lock or update ownership is invented.")
	return nil
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
