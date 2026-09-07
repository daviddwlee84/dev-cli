package submodule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type AddRequest struct {
	Parent   string `json:"parent"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Checkout string `json:"checkout"`
	Ref      string `json:"ref,omitempty"`
	Init     string `json:"submodules"`
}

type AddResult struct {
	Operation string `json:"operation"`
	AddRequest
	Phase    string   `json:"phase"`
	GitDir   string   `json:"git_dir,omitempty"`
	HEAD     string   `json:"head,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Staged   bool     `json:"staged"`
	Warnings []string `json:"warnings,omitempty"`
}

// AddPlan holds sealed local authority. Its public report is a copy; callers
// cannot silently change the request between confirmation and application.
type AddPlan struct {
	request    AddRequest
	repository gitx.Repo
	gitdir     string
	files      map[string][]byte
	fileModes  map[string]os.FileMode
	dirs       map[string]os.FileInfo
	head       string
	branch     string
}

func (p *AddPlan) Report() AddResult {
	return AddResult{Operation: "submodule-add", AddRequest: p.request, Phase: "planned", GitDir: p.gitdir}
}

func PlanAdd(ctx context.Context, cfg config.Config, req AddRequest) (*AddPlan, error) {
	if err := gitx.NetworkCloneURL(req.Source); err != nil {
		return nil, err
	}
	if req.Checkout == "" {
		req.Checkout = "pinned"
	}
	if req.Checkout != "pinned" && req.Checkout != "default-branch" {
		return nil, errors.New("--checkout must be pinned or default-branch")
	}
	if req.Checkout == "default-branch" && req.Ref != "" {
		return nil, errors.New("--ref requires --checkout=pinned")
	}
	if strings.HasPrefix(req.Ref, "-") || strings.IndexFunc(req.Ref, unicode.IsControl) >= 0 {
		return nil, errors.New("invalid submodule ref")
	}
	r, err := gitx.Discover(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	if r.Bare || r.Root == "" {
		return nil, errors.New("submodule add requires a non-bare checkout")
	}
	req.Parent = r.Root
	if err := validateAddPath(req.Path); err != nil {
		return nil, err
	}
	settings, err := Settings(cfg, r.Root, req.Init)
	if err != nil {
		return nil, err
	}
	req.Init = settings.Init
	p := &AddPlan{request: req, repository: r, gitdir: filepath.Join(r.GitDir, "modules", filepath.FromSlash(req.Path)), files: map[string][]byte{}, fileModes: map[string]os.FileMode{}, dirs: map[string]os.FileInfo{}}
	if err := p.preflight(ctx); err != nil {
		return nil, err
	}
	for _, path := range []string{filepath.Join(r.GitDir, "index"), filepath.Join(r.GitCommonDir, "config"), filepath.Join(r.GitDir, "config.worktree"), filepath.Join(r.Root, ".gitmodules"), filepath.Join(r.Root, ".dev-cli", "config.toml")} {
		data, err := readAddFile(ctx, path)
		if err != nil {
			return nil, err
		}
		p.files[path] = data
		if data != nil {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			p.fileModes[path] = info.Mode()
		}
	}
	for _, base := range []string{r.Root, r.GitDir, r.GitCommonDir} {
		info, err := os.Stat(base)
		if err != nil {
			return nil, err
		}
		p.dirs[base] = info
	}
	for _, pair := range [][2]string{{r.Root, req.Path}, {r.GitDir, "modules/" + req.Path}} {
		if err := p.checkParents(pair[0], pair[1], true); err != nil {
			return nil, err
		}
	}
	p.head, err = gitx.RunReadOnly(ctx, r.Root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, errors.New("parent must have an existing commit")
	}
	p.branch, err = gitx.RunReadOnly(ctx, r.Root, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return nil, errors.New("parent must be on a branch, not detached HEAD")
	}
	return p, nil
}

func validateAddPath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.ContainsAny(path, "\\:") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return errors.New("submodule path must be a clean relative path inside the parent checkout")
	}
	for _, part := range strings.Split(path, "/") {
		if err := pathx.ValidatePortableComponent(part, safefile.CompiledMaxComponentBytes); err != nil {
			return err
		}
		if part == "." || part == ".." || part == "" || strings.HasPrefix(part, "-") || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".gitmodules") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("submodule path contains a reserved or unsafe component")
		}
	}
	return nil
}

func (p *AddPlan) preflight(ctx context.Context) error {
	r := p.repository
	if operation, active, err := gitx.InProgress(r.Root); err != nil {
		return err
	} else if active {
		return fmt.Errorf("Git operation %s is in progress", operation)
	}
	conflicts, err := gitx.RunReadOnly(ctx, r.Root, "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	if conflicts != "" {
		return errors.New("parent has unresolved index conflicts")
	}
	// Refuse custom conversion before any status/hash command can invoke it.
	attrs, err := gitx.RunReadOnly(ctx, r.Root, "check-attr", "-z", "filter", "working-tree-encoding", "--", ".gitmodules")
	if err != nil {
		return err
	}
	attrFields := strings.Split(strings.TrimSuffix(attrs, "\x00"), "\x00")
	for i := 2; i < len(attrFields); i += 3 {
		if attrFields[i] != "unspecified" && attrFields[i] != "unset" {
			return errors.New(".gitmodules filters or working-tree encoding are unsupported for guarded addition")
		}
	}
	changed, err := gitx.RunReadOnly(ctx, r.Root, "--literal-pathspecs", "diff", "--cached", "--name-only", "-z", "--", ".gitmodules")
	if err != nil {
		return err
	}
	if changed != "" {
		return errors.New(".gitmodules must be absent or unchanged from HEAD; preserve and finish its existing edits first")
	}
	worktreeChanges, err := gitx.RunReadOnly(ctx, r.Root, "--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--", ".gitmodules")
	if err != nil {
		return err
	}
	if worktreeChanges != "" {
		return errors.New(".gitmodules has unstaged content or mode changes")
	}
	flags, err := gitx.RunReadOnly(ctx, r.Root, "--literal-pathspecs", "ls-files", "-v", "--", ".gitmodules")
	if err != nil {
		return err
	}
	if flags != "" && !strings.HasPrefix(flags, "H ") {
		return errors.New(".gitmodules has hidden index flags; clear them explicitly before adding a submodule")
	}
	// Verify the content independently of status, honoring Git's ordinary
	// line-ending rules (e.g. a clean Windows CRLF checkout of an LF blob).
	if data, err := readAddFile(ctx, filepath.Join(r.Root, ".gitmodules")); err != nil {
		return err
	} else if data == nil && flags != "" {
		return errors.New("tracked .gitmodules is missing from the working tree")
	} else if data != nil {
		actual, err := gitx.RunReadOnly(ctx, r.Root, "hash-object", "--path=.gitmodules", "--", ".gitmodules")
		if err != nil {
			return err
		}
		expected, err := gitx.RunReadOnly(ctx, r.Root, "rev-parse", "--verify", "HEAD:.gitmodules")
		if err != nil || actual != expected {
			return errors.New(".gitmodules working bytes differ from HEAD")
		}
	}
	if _, err := gitx.SubmodulesOf(ctx, r.Root); err != nil {
		return err
	}
	tracked, err := gitx.RunReadOnly(ctx, r.Root, "--literal-pathspecs", "ls-files", "-z", "--", p.request.Path)
	if err != nil {
		return err
	}
	if tracked != "" {
		return errors.New("submodule target overlaps tracked content")
	}
	for _, path := range []string{filepath.Join(r.Root, filepath.FromSlash(p.request.Path)), p.gitdir} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("target already exists: %s; existing clones and stores are never adopted or overwritten", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	sources, err := gitx.FetchCloneSources(ctx, r.Root)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if catalog.NormalizeRemoteIdentity(s.URL) == catalog.NormalizeRemoteIdentity(p.request.Source) {
			return errors.New("cannot add the parent repository as its own submodule")
		}
	}
	// A declared-but-uninitialized entry or a custom name may not have a
	// working directory. Reject name collisions independently of filesystem state.
	modules, err := readAddFile(ctx, filepath.Join(r.Root, ".gitmodules"))
	if err != nil {
		return err
	}
	if modules != nil {
		out, err := gitx.RunReadOnly(ctx, r.Root, "config", "--file", ".gitmodules", "--name-only", "--get-regexp", "^submodule\\.")
		if err != nil && strings.TrimSpace(string(modules)) != "" {
			// --list validates syntax even when no matching submodule keys exist.
			if _, validationErr := gitx.RunReadOnly(ctx, r.Root, "config", "--file", ".gitmodules", "--list"); validationErr != nil {
				return validationErr
			}
		}
		for _, key := range strings.Split(out, "\n") {
			if strings.HasPrefix(key, "submodule."+p.request.Path+".") {
				return errors.New("submodule name already declared")
			}
		}
	}
	return nil
}

func readAddFile(ctx context.Context, path string) ([]byte, error) {
	data, err := safefile.ReadRegular(ctx, path, 64*1024*1024)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect addition authority %s: %w", path, err)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func (p *AddPlan) checkParents(base, rel string, capture bool) error {
	path := base
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target crosses a non-directory or symlink: %s", path)
		}
		if base == p.request.Parent {
			for _, gitdir := range []string{p.repository.GitDir, p.repository.GitCommonDir} {
				if expected := p.dirs[gitdir]; expected != nil && os.SameFile(expected, info) {
					return errors.New("target crosses Git administrative storage")
				}
			}
		}
		if capture {
			p.dirs[path] = info
		}
		if path != base {
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				return fmt.Errorf("target crosses another repository: %s; use it as --parent explicitly", path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (p *AddPlan) revalidate(ctx context.Context) error {
	current, err := gitx.Discover(ctx, p.request.Parent)
	if err != nil || current != p.repository {
		return errors.New("stale submodule-add plan: checkout identity changed")
	}
	for path, expected := range p.dirs {
		if err := safefile.VerifyRoot(path, expected); err != nil {
			return fmt.Errorf("stale submodule-add plan: directory changed: %s", path)
		}
	}
	for path, expected := range p.files {
		data, err := readAddFile(ctx, path)
		if err != nil {
			return err
		}
		if (data == nil) != (expected == nil) || sha256.Sum256(data) != sha256.Sum256(expected) {
			return fmt.Errorf("stale submodule-add plan: file changed: %s", path)
		}
		if expected != nil {
			info, err := os.Lstat(path)
			if err != nil || info.Mode() != p.fileModes[path] {
				return fmt.Errorf("stale submodule-add plan: file mode changed: %s", path)
			}
		}
	}
	head, err := gitx.RunReadOnly(ctx, p.request.Parent, "rev-parse", "HEAD")
	if err != nil || head != p.head {
		return errors.New("stale submodule-add plan: HEAD changed")
	}
	branch, err := gitx.RunReadOnly(ctx, p.request.Parent, "symbolic-ref", "-q", "HEAD")
	if err != nil || branch != p.branch {
		return errors.New("stale submodule-add plan: branch changed")
	}
	if op, active, err := gitx.InProgress(p.request.Parent); err != nil {
		return err
	} else if active {
		return fmt.Errorf("Git operation %s started after plan", op)
	}
	if err := p.checkParents(p.request.Parent, p.request.Path, false); err != nil {
		return err
	}
	return p.checkParents(p.repository.GitDir, "modules/"+p.request.Path, false)
}

// Apply preserves partial clones on every failure. It never removes user data
// or resets the parent's index to a saved snapshot.
func (p *AddPlan) Apply(ctx context.Context, guard func(context.Context, string) error) (AddResult, error) {
	result := p.Report()
	result.Phase = "not-added"
	err := gitx.WithLifecycleLock(ctx, p.repository.GitCommonDir, func() error {
		if err := p.revalidate(ctx); err != nil {
			return err
		}
		if err := p.preflight(ctx); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(ctx, p.request.Parent); err != nil {
				return err
			}
		}
		target := filepath.Join(p.request.Parent, filepath.FromSlash(p.request.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p.gitdir), 0755); err != nil {
			return err
		}
		if err := p.revalidate(ctx); err != nil {
			return err
		}
		result.Phase = "clone-incomplete"
		for _, path := range []string{target, p.gitdir} {
			if _, err := os.Lstat(path); err == nil {
				result.Phase = "not-added"
				return fmt.Errorf("target appeared before cloning: %s", path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err := gitx.CloneSubmodule(ctx, p.request.Parent, p.request.Source, target, p.gitdir); err != nil {
			return err
		}
		result.Phase = "cloned"
		childIdentity, err := os.Stat(target)
		if err != nil {
			return err
		}
		storeIdentity, err := os.Stat(p.gitdir)
		if err != nil {
			return err
		}
		head, branch, err := gitx.CheckoutAddedSubmodule(ctx, target, p.request.Checkout, p.request.Ref)
		if err != nil {
			return err
		}
		result.HEAD, result.Branch = head, branch
		if err := p.revalidate(ctx); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(ctx, p.request.Parent); err != nil {
				return err
			}
		}
		if err := safefile.VerifyRoot(target, childIdentity); err != nil {
			return errors.New("new child checkout identity changed before publication")
		}
		if err := safefile.VerifyRoot(p.gitdir, storeIdentity); err != nil {
			return errors.New("new child Git store changed before publication")
		}
		childRepo, err := gitx.Discover(ctx, target)
		if err != nil || childRepo.Root != target || childRepo.GitDir != p.gitdir || childRepo.GitCommonDir != p.gitdir {
			return errors.New("new child repository identity changed before publication")
		}
		childHEAD, err := gitx.RunReadOnly(ctx, target, "rev-parse", "HEAD")
		if err != nil || childHEAD != head {
			return errors.New("new child HEAD changed before publication")
		}
		modulePath := filepath.Join(p.request.Parent, ".gitmodules")
		data, err := p.moduleContents(ctx)
		if err != nil {
			return err
		}
		if err := p.revalidate(ctx); err != nil {
			return err
		}
		if err := replaceAddModules(ctx, modulePath, p.files[modulePath], data); err != nil {
			return err
		}
		result.Phase = "added-unstaged"
		if err := gitx.StageAddedSubmodule(ctx, p.request.Parent, p.request.Path); err != nil {
			return err
		}
		result.Staged = true
		result.Phase = "added"
		if p.request.Init == "recursive" {
			// Initialize only the newly added subtree; preserve existing siblings.
			if _, err := gitx.InitSubmodules(ctx, target); err != nil {
				result.Phase = "initialization-incomplete"
				return errors.New("submodule added and staged, but recursive initialization failed; inspect child status and run dev submodule init there")
			}
		}
		result.Phase = "complete"
		return nil
	})
	if err != nil && result.Phase != "not-added" {
		result.Warnings = append(result.Warnings, "Partial data retained; inspect the reported checkout and Git directory. Do not rerun add over existing paths or force-delete them. Finish metadata/staging manually, then use dev submodule init for missing descendants.")
	}
	return result, err
}

func (p *AddPlan) moduleContents(ctx context.Context) ([]byte, error) {
	f, err := os.CreateTemp(p.repository.GitDir, "dev-submodule-add-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	_, writeErr := f.Write(p.files[filepath.Join(p.request.Parent, ".gitmodules")])
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return nil, err
	}
	for _, field := range [][2]string{{"path", p.request.Path}, {"url", p.request.Source}} {
		if _, err := gitx.Run(ctx, p.request.Parent, "config", "--file", path, "submodule."+p.request.Path+"."+field[0], field[1]); err != nil {
			return nil, err
		}
	}
	return readAddFile(ctx, path)
}

func replaceAddModules(ctx context.Context, path string, expected, data []byte) error {
	root, _, err := safefile.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	if expected == nil {
		_, err = safefile.CreatePrivateNoClobber(ctx, root, name, data, false)
		return err
	}
	observed, err := root.Lstat(name)
	if err != nil {
		return err
	}
	current, observed, err := safefile.ReadStableRegular(ctx, root, name, observed, 64*1024*1024)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return errors.New(".gitmodules changed before publication")
	}
	_, err = safefile.AtomicReplace(ctx, root, name, observed, data, observed.Mode().Perm())
	return err
}
