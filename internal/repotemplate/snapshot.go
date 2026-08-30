// Package repotemplate prepares and applies safe filesystem snapshots used to
// seed new repositories. A snapshot contains file contents rather than live
// source paths so validation and application are separated by a stable
// boundary.
package repotemplate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

const pathPreviewLimit = 8

// Request identifies a local directory or Git source and the tree within it
// that should become the root of a new repository.
type Request struct {
	Source string
	Ref    string
	Subdir string
}

// File is one immutable regular file in a prepared snapshot.
type File struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// Directory is one directory that should be retained even when it is empty.
type Directory struct {
	Path string
	Mode fs.FileMode
}

// Snapshot is a fully prepared, source-independent template tree. Source is a
// display-safe label and never retains URL userinfo.
type Snapshot struct {
	Source      string
	Ref         string
	Commit      string
	Subdir      string
	Local       bool
	Live        bool
	GitFiltered bool
	Files       []File
	Directories []Directory
}

// Summary is the serializable, content-free part of a Snapshot used by plans
// and results. PathPreview contains only validated relative paths.
type Summary struct {
	Source               string   `json:"source"`
	Ref                  string   `json:"ref,omitempty"`
	Commit               string   `json:"commit,omitempty"`
	Subdir               string   `json:"subdir,omitempty"`
	Local                bool     `json:"local"`
	Live                 bool     `json:"live"`
	GitFiltered          bool     `json:"git_filtered,omitempty"`
	Files                int      `json:"files"`
	Directories          int      `json:"directories"`
	PathPreview          []string `json:"path_preview,omitempty"`
	PathPreviewTruncated bool     `json:"path_preview_truncated,omitempty"`
}

// ApplyResult describes a materialized template snapshot.
type ApplyResult struct {
	Summary
	AppliedFiles       int `json:"applied_files"`
	AppliedDirectories int `json:"applied_directories"`
}

type snapshotSource struct {
	local bool
	live  bool
}

// Prepare validates and reads a template source. A live local Git worktree is
// filtered through Git so tracked files (including tracked ignored files) and
// untracked non-ignored files are retained without copying ignored secrets. A
// ref or non-local source is checked out into an isolated repository first.
func Prepare(ctx context.Context, request Request) (Snapshot, error) {
	request.Source = strings.TrimSpace(request.Source)
	request.Ref = strings.TrimSpace(request.Ref)
	request.Subdir = strings.TrimSpace(request.Subdir)
	if request.Source == "" {
		return Snapshot{}, errors.New("template source is required")
	}
	if strings.HasPrefix(request.Ref, "-") {
		return Snapshot{}, fmt.Errorf("template ref %q must not start with '-'", request.Ref)
	}
	subdir, err := cleanSubdir(request.Subdir)
	if err != nil {
		return Snapshot{}, err
	}

	local := config.Expand(request.Source)
	info, statErr := os.Lstat(local)
	localSource := statErr == nil
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return Snapshot{}, fmt.Errorf("template source %q is a symlink", repo.RedactCloneRef(request.Source))
		}
		if !info.IsDir() {
			return Snapshot{}, fmt.Errorf("template source %q is not a directory", repo.RedactCloneRef(request.Source))
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("inspect template source %q: %v", repo.RedactCloneRef(request.Source),
			repo.RedactCloneError(statErr, request.Source, local))
	}
	if localSource && request.Ref == "" {
		return snapshotDirectory(ctx, repo.RedactCloneRef(request.Source), local, "", "", subdir, snapshotSource{
			local: true,
			live:  true,
		})
	}

	checkout, resolvedRef, cleanup, err := checkoutGitSource(ctx, request.Source, request.Ref)
	if err != nil {
		return Snapshot{}, err
	}
	defer cleanup()
	return snapshotDirectory(ctx, repo.RedactCloneRef(request.Source), checkout, request.Ref, resolvedRef, subdir, snapshotSource{
		local: localSource,
	})
}

func checkoutGitSource(ctx context.Context, source, ref string) (string, string, func(), error) {
	parent, err := os.MkdirTemp("", "dev-repo-template-*")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("create template checkout: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	checkout := filepath.Join(parent, "source")
	normalized := repo.NormalizeCloneRef(source)
	safeSource := repo.RedactCloneRef(source)
	redact := func(err error) error { return repo.RedactCloneError(err, source, normalized) }
	if ref == "" {
		if _, err := gitx.Run(ctx, parent, "clone", "--quiet", "--depth", "1", "--", normalized, checkout); err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("clone template source %q: %v", safeSource, redact(err))
		}
		resolved, err := gitx.Run(ctx, checkout, "rev-parse", "HEAD")
		if err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("resolve template HEAD: %v", redact(err))
		}
		return checkout, resolved, cleanup, nil
	}

	if err := os.Mkdir(checkout, 0o755); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("create template checkout: %w", err)
	}
	if _, err := gitx.Run(ctx, checkout, "init", "--quiet"); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("initialize template checkout: %v", redact(err))
	}
	if _, err := gitx.Run(ctx, checkout, "remote", "add", "--", "origin", normalized); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("configure template source %q: %v", safeSource, redact(err))
	}
	checkoutTarget := "FETCH_HEAD"
	if _, err := gitx.Run(ctx, checkout, "fetch", "--quiet", "--depth", "1", "origin", ref); err != nil {
		if _, exactErr := gitx.Run(ctx, checkout, "fetch", "--quiet", "origin", ref); exactErr != nil {
			if _, fullErr := gitx.Run(ctx, checkout, "fetch", "--quiet", "origin"); fullErr != nil {
				cleanup()
				joined := errors.Join(err, exactErr, fullErr)
				return "", "", func() {}, fmt.Errorf("fetch template ref %q: %v", ref, redact(joined))
			}
			checkoutTarget = ref
		}
	}
	if _, err := gitx.Run(ctx, checkout, "checkout", "--quiet", "--detach", checkoutTarget); err != nil && checkoutTarget == ref {
		if _, remoteErr := gitx.Run(ctx, checkout, "checkout", "--quiet", "--detach", "origin/"+ref); remoteErr != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("checkout template ref %q: %v", ref, redact(errors.Join(err, remoteErr)))
		}
	} else if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("checkout template ref %q: %v", ref, redact(err))
	}
	resolved, err := gitx.Run(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("resolve template ref %q: %v", ref, redact(err))
	}
	return checkout, resolved, cleanup, nil
}

func snapshotDirectory(ctx context.Context, sourceLabel, rootPath, ref, commit, subdir string, source snapshotSource) (Snapshot, error) {
	root, rootInfo, err := openNamedRoot(rootPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve template source %q: %w", sourceLabel, err)
	}
	defer root.Close()

	selected := root
	selectedInfo := rootInfo
	selectedPath := rootPath
	if subdir != "" {
		selected, selectedInfo, err = openRelativeRoot(root, subdir)
		if err != nil {
			return Snapshot{}, fmt.Errorf("template subdirectory %q: %w", subdir, err)
		}
		defer selected.Close()
		selectedPath = filepath.Join(rootPath, filepath.FromSlash(subdir))
	}

	snapshot := Snapshot{
		Source: sourceLabel, Ref: ref, Commit: commit, Subdir: subdir,
		Local: source.local, Live: source.live,
	}
	if source.live {
		paths, filtered, err := liveGitPaths(ctx, selectedPath)
		if err != nil {
			return Snapshot{}, fmt.Errorf("list live template %q: %w", sourceLabel, err)
		}
		if err := verifyNamedRoot(selectedPath, selectedInfo); err != nil {
			return Snapshot{}, fmt.Errorf("template source changed while listing: %w", err)
		}
		snapshot.GitFiltered = filtered
		if filtered {
			tree, err := buildSelectionTree(paths)
			if err != nil {
				return Snapshot{}, fmt.Errorf("prepare template %q: %w", sourceLabel, err)
			}
			if _, err := snapshotSelectedRoot(ctx, selected, tree, "", &snapshot); err != nil {
				return Snapshot{}, fmt.Errorf("prepare template %q: %w", sourceLabel, err)
			}
		} else if err := snapshotWholeRoot(ctx, selected, "", &snapshot); err != nil {
			return Snapshot{}, fmt.Errorf("prepare template %q: %w", sourceLabel, err)
		}
	} else if err := snapshotWholeRoot(ctx, selected, "", &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("prepare template %q: %w", sourceLabel, err)
	}
	if err := verifyNamedRoot(selectedPath, selectedInfo); err != nil {
		return Snapshot{}, fmt.Errorf("template source changed during snapshot: %w", err)
	}
	if err := finalizeSnapshot(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("prepare template %q: %w", sourceLabel, err)
	}
	return snapshot, nil
}

func openNamedRoot(path string) (*os.Root, fs.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("root is a symlink")
	}
	if !before.IsDir() {
		return nil, nil, errors.New("root is not a directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	held, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(before, held) || !os.SameFile(current, held) {
		root.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("root changed while opening")
	}
	return root, held, nil
}

func openRelativeRoot(base *os.Root, relative string) (*os.Root, fs.FileInfo, error) {
	current := base
	owned := false
	for _, component := range strings.Split(relative, "/") {
		next, _, err := openChildRoot(current, component)
		if err != nil {
			if owned {
				current.Close()
			}
			return nil, nil, err
		}
		if owned {
			current.Close()
		}
		current, owned = next, true
	}
	info, err := current.Stat(".")
	if err != nil {
		current.Close()
		return nil, nil, err
	}
	return current, info, nil
}

func openChildRoot(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("component %q is a symlink", name)
	}
	if !before.IsDir() {
		return nil, nil, fmt.Errorf("component %q is not a directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	held, err := child.Stat(".")
	if err != nil {
		child.Close()
		return nil, nil, err
	}
	current, err := parent.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(before, held) || !os.SameFile(current, held) {
		child.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("component %q changed while opening", name)
	}
	return child, held, nil
}

func verifyNamedRoot(path string, expected fs.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(expected, current) {
		return errors.New("root path changed")
	}
	return nil
}

func verifyChildRoot(parent *os.Root, name string, expected fs.FileInfo) error {
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("component %q changed", name)
	}
	return nil
}

func liveGitPaths(ctx context.Context, root string) ([]string, bool, error) {
	if _, err := gitx.Discover(ctx, root); err != nil {
		if errors.Is(err, gitx.ErrNotARepo) {
			return nil, false, nil
		}
		return nil, false, err
	}
	selected := map[string]bool{}
	tracked, err := gitx.Run(ctx, root, "ls-files", "-z", "--stage", "--", ".")
	if err != nil {
		return nil, false, err
	}
	for _, record := range splitNUL(tracked) {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, false, fmt.Errorf("unexpected git ls-files record %q", record)
		}
		header, path := record[:tab], record[tab+1:]
		fields := strings.Fields(header)
		if len(fields) < 1 {
			return nil, false, fmt.Errorf("unexpected git ls-files record %q", record)
		}
		if fields[0] == "160000" {
			continue
		}
		selected[path] = true
	}
	untracked, err := gitx.Run(ctx, root, "ls-files", "-z", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, false, err
	}
	for _, path := range splitNUL(untracked) {
		selected[path] = true
	}
	paths := make([]string, 0, len(selected))
	for path := range selected {
		cleaned, err := validateSnapshotPath(path)
		if err != nil {
			return nil, false, fmt.Errorf("Git path %q: %w", path, err)
		}
		paths = append(paths, cleaned)
	}
	sort.Strings(paths)
	return paths, true, nil
}

func splitNUL(value string) []string {
	value = strings.TrimSuffix(value, "\x00")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\x00")
}

type selectionNode struct {
	leaf     bool
	children map[string]*selectionNode
}

func buildSelectionTree(paths []string) (*selectionNode, error) {
	root := &selectionNode{children: map[string]*selectionNode{}}
	for _, path := range paths {
		current := root
		components := strings.Split(path, "/")
		for index, component := range components {
			if current.leaf {
				return nil, fmt.Errorf("Git path %q is below another selected file", path)
			}
			if current.children[component] == nil {
				current.children[component] = &selectionNode{children: map[string]*selectionNode{}}
			}
			current = current.children[component]
			if index == len(components)-1 {
				if len(current.children) > 0 {
					return nil, fmt.Errorf("Git path %q is the parent of another selected path", path)
				}
				current.leaf = true
			}
		}
	}
	return root, nil
}

func snapshotWholeRoot(ctx context.Context, root *os.Root, relative string, snapshot *Snapshot) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	before, err := directory.Stat()
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		if strings.EqualFold(name, ".git") {
			continue
		}
		if err := validateSnapshotComponent(name); err != nil {
			return fmt.Errorf("template entry %q: %w", joinRelative(relative, name), err)
		}
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect template entry %q: %w", joinRelative(relative, name), err)
		}
		path := joinRelative(relative, name)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template entry %q is a symlink", path)
		}
		switch {
		case info.IsDir():
			child, held, err := openChildRoot(root, name)
			if err != nil {
				return fmt.Errorf("open template directory %q: %w", path, err)
			}
			snapshot.Directories = append(snapshot.Directories, Directory{Path: path, Mode: held.Mode().Perm()})
			err = snapshotWholeRoot(ctx, child, path, snapshot)
			verifyErr := verifyChildRoot(root, name, held)
			closeErr := child.Close()
			if err != nil {
				return err
			}
			if verifyErr != nil {
				return verifyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case info.Mode().IsRegular():
			data, opened, err := readStableRootFile(ctx, root, name, info)
			if err != nil {
				return fmt.Errorf("read template file %q: %w", path, err)
			}
			snapshot.Files = append(snapshot.Files, File{Path: path, Mode: opened.Mode().Perm(), Data: data})
		default:
			return fmt.Errorf("template entry %q is a special file (%s)", path, info.Mode().Type())
		}
	}
	after, err := directory.Stat()
	if err != nil {
		return err
	}
	if !stableInfo(before, after) {
		return errors.New("source directory changed during snapshot")
	}
	return nil
}

func snapshotSelectedRoot(ctx context.Context, root *os.Root, node *selectionNode, relative string, snapshot *Snapshot) (bool, error) {
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	before, err := root.Stat(".")
	if err != nil {
		return false, err
	}
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	included := false
	for _, name := range names {
		childNode := node.children[name]
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && childNode.leaf && len(childNode.children) == 0 {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect selected template entry %q: %w", joinRelative(relative, name), err)
		}
		path := joinRelative(relative, name)
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("template entry %q is a symlink", path)
		}
		if len(childNode.children) > 0 {
			if childNode.leaf || !info.IsDir() {
				return false, fmt.Errorf("selected Git path %q is not a directory tree", path)
			}
			child, held, err := openChildRoot(root, name)
			if err != nil {
				return false, fmt.Errorf("open selected template directory %q: %w", path, err)
			}
			childIncluded, walkErr := snapshotSelectedRoot(ctx, child, childNode, path, snapshot)
			verifyErr := verifyChildRoot(root, name, held)
			closeErr := child.Close()
			if walkErr != nil {
				return false, walkErr
			}
			if verifyErr != nil {
				return false, verifyErr
			}
			if closeErr != nil {
				return false, closeErr
			}
			if childIncluded {
				snapshot.Directories = append(snapshot.Directories, Directory{Path: path, Mode: held.Mode().Perm()})
				included = true
			}
			continue
		}
		if !childNode.leaf || !info.Mode().IsRegular() {
			return false, fmt.Errorf("selected Git path %q is not a regular file", path)
		}
		data, opened, err := readStableRootFile(ctx, root, name, info)
		if err != nil {
			return false, fmt.Errorf("read selected template file %q: %w", path, err)
		}
		snapshot.Files = append(snapshot.Files, File{Path: path, Mode: opened.Mode().Perm(), Data: data})
		included = true
	}
	after, err := root.Stat(".")
	if err != nil {
		return false, err
	}
	if !stableInfo(before, after) {
		return false, errors.New("source directory changed during snapshot")
	}
	return included, nil
}

func readStableRootFile(ctx context.Context, root *os.Root, name string, expected fs.FileInfo) ([]byte, fs.FileInfo, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !stableInfo(expected, opened) {
		return nil, nil, errors.New("source changed while opening file")
	}
	current, err := root.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !stableInfo(opened, current) {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("source changed while opening file")
	}
	data, err := readAllContext(ctx, file)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	current, err = root.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !stableInfo(opened, after) ||
		!stableInfo(after, current) || after.Size() != int64(len(data)) {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("source changed during snapshot")
	}
	return data, opened, nil
}

func readAllContext(ctx context.Context, reader io.Reader) ([]byte, error) {
	var data []byte
	buffer := make([]byte, 64*1024)
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		data = append(data, buffer[:read]...)
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func stableInfo(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

// Summary returns a content-free description suitable for user-facing plans.
func (snapshot Snapshot) Summary() Summary {
	preview, truncated := snapshotPathPreview(snapshot)
	return Summary{
		Source: snapshot.Source, Ref: snapshot.Ref, Commit: snapshot.Commit, Subdir: snapshot.Subdir,
		Local: snapshot.Local, Live: snapshot.Live, GitFiltered: snapshot.GitFiltered,
		Files: len(snapshot.Files), Directories: len(snapshot.Directories),
		PathPreview: preview, PathPreviewTruncated: truncated,
	}
}

func snapshotPathPreview(snapshot Snapshot) ([]string, bool) {
	paths := make([]string, 0, len(snapshot.Files)+len(snapshot.Directories))
	for _, file := range snapshot.Files {
		paths = append(paths, file.Path)
	}
	for _, directory := range snapshot.Directories {
		paths = append(paths, directory.Path+"/")
	}
	sort.Strings(paths)
	truncated := len(paths) > pathPreviewLimit
	if truncated {
		paths = paths[:pathPreviewLimit]
	}
	return append([]string(nil), paths...), truncated
}

// Apply writes a prepared snapshot into a newly initialized, otherwise empty
// repository. All traversal and creation stays relative to a held os.Root, and
// file permissions are applied through the opened file descriptor.
func (snapshot Snapshot) Apply(destination string) (ApplyResult, error) {
	result := ApplyResult{Summary: snapshot.Summary()}
	if err := validateSnapshot(snapshot); err != nil {
		return result, err
	}
	root, rootInfo, err := openNamedRoot(destination)
	if err != nil {
		return result, fmt.Errorf("resolve template destination: %w", err)
	}
	defer root.Close()
	entries, err := readRootEntries(root)
	if err != nil {
		return result, fmt.Errorf("inspect template destination: %w", err)
	}
	gitMetadata := false
	for _, entry := range entries {
		if entry.Name() == ".git" {
			info, err := root.Lstat(entry.Name())
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return result, errors.New("template destination has unsafe Git metadata")
			}
			gitMetadata = true
			continue
		}
		return result, fmt.Errorf("template destination is not a new repository: %q already exists", entry.Name())
	}
	if !gitMetadata {
		return result, errors.New("template destination is not an initialized Git repository")
	}

	tree, err := buildApplyTree(snapshot)
	if err != nil {
		return result, err
	}
	if err := applyTree(root, tree, &result); err != nil {
		return result, err
	}
	if err := verifyNamedRoot(destination, rootInfo); err != nil {
		return result, fmt.Errorf("template destination changed during apply: %w", err)
	}
	return result, nil
}

func readRootEntries(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

type applyNode struct {
	directory *Directory
	file      *File
	children  map[string]*applyNode
}

func buildApplyTree(snapshot Snapshot) (*applyNode, error) {
	root := &applyNode{children: map[string]*applyNode{}}
	add := func(path string) *applyNode {
		current := root
		for _, component := range strings.Split(path, "/") {
			if current.children[component] == nil {
				current.children[component] = &applyNode{children: map[string]*applyNode{}}
			}
			current = current.children[component]
		}
		return current
	}
	for index := range snapshot.Directories {
		directory := &snapshot.Directories[index]
		add(directory.Path).directory = directory
	}
	for index := range snapshot.Files {
		file := &snapshot.Files[index]
		add(file.Path).file = file
	}
	return root, nil
}

func applyTree(root *os.Root, node *applyNode, result *ApplyResult) error {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		childNode := node.children[name]
		if childNode.file != nil {
			if err := writeRootFile(root, name, *childNode.file); err != nil {
				return fmt.Errorf("create template file %q: %w", childNode.file.Path, err)
			}
			result.AppliedFiles++
			continue
		}
		if err := root.Mkdir(name, 0o700); err != nil {
			return fmt.Errorf("create template directory %q: %w", name, err)
		}
		child, held, err := openChildRoot(root, name)
		if err != nil {
			return fmt.Errorf("open created template directory %q: %w", name, err)
		}
		walkErr := applyTree(child, childNode, result)
		mode := fs.FileMode(0o755)
		if childNode.directory != nil {
			mode = childNode.directory.Mode.Perm()
			result.AppliedDirectories++
		}
		chmodErr := chmodRootDirectory(child, mode)
		verifyErr := verifyChildRoot(root, name, held)
		closeErr := child.Close()
		if walkErr != nil {
			return walkErr
		}
		if chmodErr != nil {
			return fmt.Errorf("set template directory mode %q: %w", name, chmodErr)
		}
		if verifyErr != nil {
			return verifyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func writeRootFile(root *os.Root, name string, file File) error {
	writer, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keepOpen := true
	defer func() {
		if keepOpen {
			_ = writer.Close()
		}
	}()
	for remaining := file.Data; len(remaining) > 0; {
		written, err := writer.Write(remaining)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if err := writer.Chmod(file.Mode.Perm()); err != nil {
		return err
	}
	if err := writer.Sync(); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	keepOpen = false
	return nil
}

func chmodRootDirectory(root *os.Root, mode fs.FileMode) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Chmod(mode.Perm())
}

func finalizeSnapshot(snapshot *Snapshot) error {
	sort.SliceStable(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	sort.SliceStable(snapshot.Directories, func(i, j int) bool { return snapshot.Directories[i].Path < snapshot.Directories[j].Path })
	return validateSnapshot(*snapshot)
}

func validateSnapshot(snapshot Snapshot) error {
	types := make(map[string]string, len(snapshot.Directories)+len(snapshot.Files))
	caseFolded := make(map[string]string, len(snapshot.Directories)+len(snapshot.Files))
	validate := func(relative, kind string, mode fs.FileMode) error {
		cleaned, err := validateSnapshotPath(relative)
		if err != nil || cleaned != relative {
			if err == nil {
				err = errors.New("path is not a clean relative path")
			}
			return fmt.Errorf("template %s %q: %w", kind, relative, err)
		}
		if mode&^fs.ModePerm != 0 {
			return fmt.Errorf("template %s %q has unsupported mode %s", kind, relative, mode)
		}
		if previous, exists := types[relative]; exists {
			return fmt.Errorf("template path %q is declared as both %s and %s", relative, previous, kind)
		}
		folded := strings.ToLower(relative)
		if previous, exists := caseFolded[folded]; exists && previous != relative {
			return fmt.Errorf("template paths %q and %q collide on case-insensitive filesystems", previous, relative)
		}
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if types[parent] == "file" {
				return fmt.Errorf("template %s %q is below file %q", kind, relative, parent)
			}
		}
		types[relative] = kind
		caseFolded[folded] = relative
		return nil
	}
	for _, directory := range snapshot.Directories {
		if err := validate(directory.Path, "directory", directory.Mode); err != nil {
			return err
		}
	}
	for _, file := range snapshot.Files {
		if err := validate(file.Path, "file", file.Mode); err != nil {
			return err
		}
		prefix := file.Path + "/"
		for existing := range types {
			if strings.HasPrefix(existing, prefix) {
				return fmt.Errorf("template file %q is the parent of %q", file.Path, existing)
			}
		}
	}
	return nil
}

func cleanSubdir(value string) (string, error) {
	if value == "" || value == "." {
		return "", nil
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return "", fmt.Errorf("template subdirectory %q must be relative", value)
	}
	components := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' })
	if len(components) == 0 {
		return "", fmt.Errorf("template subdirectory %q is invalid", value)
	}
	for _, component := range components {
		if component == ".." {
			return "", fmt.Errorf("template subdirectory %q: %w", value, pathx.ErrTraversal)
		}
		if err := validateSnapshotComponent(component); err != nil {
			return "", fmt.Errorf("template subdirectory %q: %w", value, err)
		}
	}
	return strings.Join(components, "/"), nil
}

func validateSnapshotPath(value string) (string, error) {
	if value == "" || filepath.ToSlash(value) != value || strings.HasPrefix(value, "/") {
		return "", errors.New("path is not a clean relative path")
	}
	components := strings.Split(value, "/")
	for _, component := range components {
		if err := validateSnapshotComponent(component); err != nil {
			return "", err
		}
	}
	cleaned := strings.Join(components, "/")
	if cleaned != value {
		return "", errors.New("path is not a clean relative path")
	}
	return cleaned, nil
}

func validateSnapshotComponent(component string) error {
	if err := pathx.ValidateComponent(component); err != nil {
		return err
	}
	windowsName := strings.TrimRight(component, ". ")
	if strings.EqualFold(windowsName, ".git") {
		return fmt.Errorf("template path contains reserved Git metadata")
	}
	if windowsName != component || strings.ContainsRune(component, ':') {
		return fmt.Errorf("template path component %q is not portable to Windows", component)
	}
	if strings.IndexFunc(component, unicode.IsControl) >= 0 {
		return fmt.Errorf("template path component contains control characters")
	}
	return nil
}

func joinRelative(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}
