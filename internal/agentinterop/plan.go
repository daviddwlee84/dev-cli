package agentinterop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

func safeText(s string) bool {
	if !utf8.ValidString(s) || len(s) > 4096 {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
			return false
		}
	}
	return true
}

func projectRoots(req TransferRequest) []string {
	var roots []string
	for _, ref := range []ArtifactRef{req.From, req.To} {
		if ref.Scope == "project" {
			roots = append(roots, ref.Root)
		}
	}
	return roots
}

func normalize(req TransferRequest) (TransferRequest, error) {
	for _, s := range []string{req.Name, req.TargetName, req.From.Root, req.To.Root, req.From.Path, req.To.Path, req.EnvFile, req.Launcher} {
		if !safeText(s) {
			return req, errors.New("invalid transfer selector")
		}
	}
	if req.Kind != "skill" && req.Kind != "mcp" && req.Kind != "instructions" {
		return req, ErrUnsupported
	}
	if req.Kind != "instructions" && (req.Name == "" || !safeText(req.Name) || len(req.Name) > 256 || strings.HasPrefix(req.Name, "-") || knownKey.MatchString(req.Name)) {
		return req, errors.New("select exactly one named artifact")
	}
	if req.Name != strings.TrimSpace(req.Name) || req.TargetName != strings.TrimSpace(req.TargetName) || len(req.TargetName) > 256 || knownKey.MatchString(req.TargetName) {
		return req, errors.New("invalid artifact name")
	}
	if req.Mode == "" {
		req.Mode = "copy"
		if req.Kind == "skill" {
			if req.Prepared != "" {
				req.Mode = "install"
			} else if req.From.Root == req.To.Root {
				req.Mode = "mirror"
			} else {
				req.Mode = "install"
			}
		}
		if req.Kind == "instructions" {
			req.Mode = "mirror"
		}
	}
	if req.Mode != "copy" && req.Mode != "move" && req.Mode != "mirror" && !(req.Kind == "skill" && req.Mode == "install") {
		return req, errors.New("mode must be copy, move, mirror, or skill install")
	}
	for _, ref := range []*ArtifactRef{&req.From, &req.To} {
		if ref.Scope == "" {
			ref.Scope = "project"
		}
		if ref.Scope == "global" {
			ref.Scope = "user"
		}
		if ref.Scope != "project" && ref.Scope != "user" {
			return req, errors.New("transfer scope must be project or user/global")
		}
		root, err := filepath.Abs(ref.Root)
		if err != nil {
			return req, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return req, err
		}
		ref.Root = root
		if req.Kind == "skill" {
			if ref.Agent == "" {
				ref.Agent = "universal"
			}
			if ref.Path == "" {
				name := req.Name
				if ref == &req.To && req.TargetName != "" {
					name = req.TargetName
				}
				found := false
				for _, a := range agentskill.Registry() {
					if a.ID != ref.Agent {
						continue
					}
					found = true
					base := a.ProjectSkillsDir
					if ref.Scope == "user" {
						base = a.GlobalSkillsDir
						if a.ProjectSkillsDir == ".agents/skills" {
							home, _ := os.UserHomeDir()
							base = filepath.Join(home, ".agents/skills")
						}
						if base == "" {
							return req, ErrUnsupported
						}
						var err error
						*ref, err = absoluteArtifact(*ref, filepath.Join(base, name))
						if err != nil {
							return req, err
						}
					} else {
						ref.Path = filepath.Join(base, name)
					}
				}
				if !found {
					return req, errors.New("unknown skill agent")
				}
			}
		}
		if ref.Path != "" {
			if err := validLocation(location(*ref)); err != nil {
				return req, err
			}
		}
	}
	if req.TargetName == "" {
		req.TargetName = req.Name
	}
	return req, nil
}

// absoluteArtifact binds a configured user path, including an explicit agent
// home override outside HOME, to its nearest existing directory.
func absoluteArtifact(ref ArtifactRef, path string) (ArtifactRef, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return ref, err
	}
	root := ref.Root
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		root = filepath.Dir(path)
		for {
			info, e := os.Lstat(root)
			if e == nil && info.IsDir() {
				break
			}
			if e == nil {
				return ref, ErrConflict
			}
			if !os.IsNotExist(e) {
				return ref, e
			}
			next := filepath.Dir(root)
			if next == root {
				return ref, ErrUnsupported
			}
			root = next
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return ref, err
		}
		rel, err = filepath.Rel(root, path)
		if err != nil {
			return ref, err
		}
	}
	ref.Root, ref.Path = root, rel
	return ref, nil
}

func (s Service) Plan(ctx context.Context, request TransferRequest) (out Plan, err error) {
	req, err := normalize(request)
	if err != nil {
		return out, err
	}
	err = s.withLock(ctx, func() error {
		st, e := s.open(true)
		if e != nil {
			return e
		}
		defer st.close()
		b := newBuilder(ctx, st, req)
		defer b.close()
		if e = buildTransfer(b, nil); e != nil {
			return e
		}
		out, e = b.save()
		return e
	})
	return out, err
}

func buildTransfer(b *builder, owner *record) error {
	if b.r.Request.From.Scope != b.r.Request.To.Scope {
		b.r.Notes = append(b.r.Notes, "Scope changes alter agent visibility; credentials and client approval are not transferred.")
	}
	switch b.r.Request.Kind {
	case "skill":
		return planSkill(b, owner)
	case "mcp":
		return planMCP(b, owner)
	case "instructions":
		return planInstructions(b, owner)
	default:
		return fmt.Errorf("%s adapter: %w", b.r.Request.Kind, ErrUnsupported)
	}
}
