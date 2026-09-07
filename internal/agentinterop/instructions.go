package agentinterop

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func resolveInstructions(ref ArtifactRef, source bool) (ArtifactRef, error) {
	if ref.Path != "" {
		if !strings.HasSuffix(ref.Path, ".md") {
			return ref, errors.New("instruction transfers require Markdown files")
		}
		return ref, nil
	}
	if ref.Agent == "" {
		ref.Agent = "codex"
		if !source {
			ref.Agent = "claude-code"
		}
	}
	name := "AGENTS.md"
	if ref.Agent == "claude-code" {
		name = "CLAUDE.md"
	} else if ref.Agent != "codex" {
		return ref, errors.New("instruction sharing supports AGENTS.md and CLAUDE.md")
	}
	if ref.Scope == "project" {
		ref.Path = name
		return ref, nil
	}
	home, _ := os.UserHomeDir()
	dir := os.Getenv("CODEX_HOME")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(home, ".codex")
	}
	if ref.Agent == "claude-code" {
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, ".claude")
		}
	}
	return absoluteArtifact(ref, filepath.Join(dir, name))
}

func planInstructions(b *builder, owner *record) error {
	req := b.r.Request
	var err error
	req.From, err = resolveInstructions(req.From, true)
	if err != nil {
		return err
	}
	req.To, err = resolveInstructions(req.To, false)
	if err != nil {
		return err
	}
	b.r.Request = req
	original := location(req.From)
	source, err := resolveSkillSource(b, original)
	if err != nil {
		return err
	}
	target := location(req.To)
	if source == target {
		return nil
	}
	img, data, err := b.read(source)
	if err != nil {
		return err
	}
	if img.Kind != "file" || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("instruction source must be a regular UTF-8 Markdown file")
	}
	if secretMaterial(source.Path, data) {
		return ErrCredentials
	}
	v, current, err := b.read(target)
	if err != nil {
		return err
	}
	if v.Kind != "file" && v.Kind != "link" && v.Kind != "absent" {
		return conflict("instruction target")
	}
	if req.Mode == "mirror" {
		rel, err := filepath.Rel(filepath.Dir(filepath.Join(target.Root, target.Path)), filepath.Join(source.Root, source.Path))
		if err != nil {
			return err
		}
		if req.Style == "" || req.Style == "symlink" {
			if v.Kind == "file" && (!bytes.Equal(data, current) || !req.Adopt) {
				return errors.New("existing instruction file needs identical bytes and --adopt, or choose --style import")
			}
			if filepath.Dir(filepath.Join(source.Root, source.Path)) != filepath.Dir(filepath.Join(target.Root, target.Path)) {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "@") {
						return errors.New("relative instruction imports need --style import when source and destination directories differ")
					}
				}
			}
			return b.link(target, rel, req.Adopt)
		}
		if req.Style != "import" || filepath.Base(target.Path) != "CLAUDE.md" {
			return errors.New("import style requires a CLAUDE.md destination")
		}
		if strings.IndexFunc(rel, unicode.IsSpace) >= 0 {
			return errors.New("Claude import paths containing whitespace are unsupported; choose symlink or copy")
		}
		if v.Kind == "link" {
			return conflict("existing instruction link")
		}
		line := "@" + filepath.ToSlash(rel)
		for _, existing := range strings.Split(string(current), "\n") {
			if strings.TrimSpace(existing) == line {
				return nil
			}
		}
		b.r.Notes = append(b.r.Notes, "Import is prepended; existing Claude-specific instructions retain their bytes and order.")
		return b.write(target, append([]byte(line+"\n\n"), current...), 0o644, true)
	}
	if v.Kind == "link" {
		return conflict("existing instruction link")
	}
	if err = b.write(target, data, 0o644, false); err != nil {
		return err
	}
	if req.Mode == "move" {
		if err = checkInstructionConsumers(b, source, original); err != nil {
			return err
		}
		if err = b.remove(original); err != nil {
			return err
		}
	}
	b.r.Notes = append(b.r.Notes, "Instruction text is unchanged; project-specific assumptions and relative paths are not generalized by a scope transfer.")
	return nil
}

func checkInstructionConsumers(b *builder, source, selected Location) error {
	for _, path := range []string{"AGENTS.md", "AGENTS.override.md", "CLAUDE.md", ".claude/CLAUDE.md"} {
		l := Location{source.Root, filepath.FromSlash(path)}
		if l == selected || l == source {
			continue
		}
		v, _, err := b.read(l)
		if err != nil {
			return err
		}
		if v.Kind != "link" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(l.Root, l.Path))
		if err != nil {
			return errors.New("instruction consumer link is dangling or cyclic")
		}
		if resolved == filepath.Join(source.Root, source.Path) {
			return errors.New("instruction source has another native link; preserve or explicitly migrate that consumer before moving it")
		}
	}
	return nil
}
