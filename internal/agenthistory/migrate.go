package agenthistory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func frozenRefs(ctx context.Context, root string) (map[string]string, error) {
	data, e := runGit(ctx, root, nil, 8<<20, "for-each-ref", "--format=%(refname)%00%(objectname)")
	if e != nil {
		return nil, e
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		name, oid, ok := strings.Cut(line, "\x00")
		if !ok || !objectID.MatchString(oid) {
			return nil, errors.New("invalid ref observation")
		}
		if strings.HasPrefix(name, "refs/replace/") {
			return nil, errors.New("history migration requires resolving replace refs first")
		}
		refs[name] = oid
	}
	head, e := gitText(ctx, root, "rev-parse", "HEAD")
	if e != nil || !objectID.MatchString(head) {
		return nil, errors.New("migration requires committed history")
	}
	refs["HEAD"] = head
	return refs, nil
}

func outputSlot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("migration output must be a clean absolute path")
	}
	if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
		return "", errors.New("migration output must not exist")
	}
	parent := filepath.Dir(path)
	canonical, e := pathx.Canonical(parent)
	if e != nil || canonical != parent {
		return "", errors.New("use an existing canonical parent for migration output")
	}
	id, e := gitx.DirectoryIdentity(parent)
	if e != nil {
		return "", e
	}
	for p := parent; ; p = filepath.Dir(p) {
		if _, e := os.Lstat(filepath.Join(p, ".git")); e == nil {
			return "", errors.New("migration output must be outside all working repositories")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return hash([]byte(parent + "\x00" + id)), nil
}

func (s *Service) PreviewMigration(ctx context.Context, o MigrationOptions) (Plan, error) {
	p, e := s.newPlan(ctx, "artifact_migrate_"+o.Mode)
	if e != nil {
		return p.View, e
	}
	if o.Mode != "untrack" && o.Mode != "split" {
		return p.View, errors.New("migration mode must be untrack or split")
	}
	paths := o.Paths
	if len(paths) == 0 {
		paths = []string{".specstory/history"}
	}
	if len(paths) > MaxFiles {
		return p.View, errors.New("select at most 256 migration paths")
	}
	for _, path := range paths {
		if !relative(path) {
			return p.View, errors.New("migration paths must be literal source-relative paths")
		}
	}
	sort.Strings(paths)
	p.MigrationPaths = paths
	shallow, e := gitText(ctx, s.Root, "rev-parse", "--is-shallow-repository")
	if e != nil || shallow != "false" {
		return p.View, errors.New("history migration requires complete local Git history")
	}
	partial, _ := gitText(ctx, s.Root, "config", "--get", "extensions.partialclone")
	if partial != "" {
		return p.View, errors.New("hydrate partial clone objects explicitly before migration")
	}
	p.Refs, e = frozenRefs(ctx, s.Root)
	if e != nil {
		return p.View, e
	}
	worktrees, e := gitx.Worktrees(ctx, s.Root)
	if e != nil {
		return p.View, e
	}
	for _, w := range worktrees {
		if w.Head == "" {
			continue
		}
		covered, e := gitText(ctx, s.Root, "for-each-ref", "--contains="+w.Head, "--format=%(refname)")
		if e != nil || covered == "" {
			return p.View, errors.New("a worktree tip has no recovery ref; create a branch for it before migration")
		}
	}
	if o.Output == "" {
		o.Output = filepath.Join(s.planDir(p.View.ID), "migration")
	}
	p.View.Output = o.Output
	p.OutputAnchor, e = outputSlot(o.Output)
	if e != nil {
		return p.View, e
	}
	copy := *s
	copy.Policy = Policy{Paths: paths, Source: "files", Capture: "project"}
	files, e := copy.sources(ctx)
	if e != nil {
		return p.View, e
	}
	if len(files) > MaxFiles {
		return p.View, errors.New("snapshot at most 256 current files per migration; select narrower paths")
	}
	for n, file := range files {
		path := filepath.Join(s.Root, filepath.FromSlash(file))
		token, e := configedit.InspectToken(ctx, path)
		if e != nil {
			return p.View, e
		}
		data, e := safefile.ReadStablePath(ctx, path, MaxFileBytes)
		if e != nil {
			return p.View, errors.New("current artifact snapshot unavailable or over 128 MiB")
		}
		payload := fmt.Sprintf("current-%03d", n)
		if e = configedit.WritePrivate(ctx, filepath.Join(s.planDir(p.View.ID), payload), data, false); e != nil {
			return p.View, e
		}
		fresh, e := configedit.InspectToken(ctx, path)
		if e != nil || fresh != token {
			return p.View, ErrStale
		}
		p.Snapshots = append(p.Snapshots, snapshot{Path: path, Logical: file, Token: token, Digest: hash(data), DesiredDigest: hash(data), Payload: payload})
		p.View.Files = append(p.View.Files, FileSummary{File: file, Bytes: int64(len(data)), Digest: s.opaque(hash(data)), Scan: "not-scanned"})
	}
	if o.Mode == "untrack" {
		if e = s.checkPendingHandoffs(ctx); e != nil {
			return p.View, e
		}
		p.View.RequiresWriterStopped = true
		index, e := runGit(ctx, s.Root, nil, 8<<20, "diff", "--cached", "--name-only", "-z")
		if e != nil {
			return p.View, e
		}
		for _, name := range strings.Split(string(index), "\x00") {
			if selectedPath(name, paths) {
				return p.View, errors.New("selected history has staged changes; review the index before untracking")
			}
		}
		before, e := readOptional(ctx, filepath.Join(s.Root, ".gitignore"))
		if e != nil {
			return p.View, e
		}
		body := string(before)
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "# dev artifact migration " + p.View.ID + "\n"
		for _, path := range paths {
			body += "/" + escapePattern(path) + "\n"
		}
		if e = s.change(ctx, &p, filepath.Join(s.Root, ".gitignore"), []byte(body)); e != nil {
			return p.View, e
		}
	} else {
		binary, e := exec.LookPath("git-filter-repo")
		if e != nil {
			return p.View, errors.New("git-filter-repo is required for split migration")
		}
		binary, e = filepath.EvalSymlinks(binary)
		if e != nil {
			return p.View, e
		}
		data, e := safefile.ReadStablePath(ctx, binary, 16<<20)
		if e != nil {
			return p.View, e
		}
		p.FilterExecutable = binary
		p.FilterDigest = hash(data)
	}
	p.View.Notices = append(p.View.Notices, "Backup covers frozen local refs and selected current artifact files; other uncommitted files, reflogs, unreachable objects, LFS payloads and submodule repositories are outside this backup.", "Raw backup content is not scanned or redacted. No source ref, remote or published tag will be rewritten.")
	if o.Mode == "split" {
		p.View.Notices = append(p.View.Notices, "Output contains original.bundle, original.git, history.git, filtered.git and both commit maps. Signatures on rewritten objects cannot be retained.")
	}
	if e = s.current(ctx, p); e != nil {
		return p.View, e
	}
	if e = s.save(ctx, &p); e != nil {
		return p.View, e
	}
	return p.View, nil
}

func selectedPath(name string, paths []string) bool {
	for _, path := range paths {
		if name == path || strings.HasPrefix(name, path+"/") {
			return true
		}
	}
	return false
}

func (s *Service) ApplyMigration(ctx context.Context, id string, o ApplyOptions) (Plan, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return p.View, e
	}
	if p.View.Kind != "artifact_migrate_untrack" && p.View.Kind != "artifact_migrate_split" {
		return p.View, errors.New("plan is not an artifact migration")
	}
	if p.View.Status == "complete" {
		return p.View, nil
	}
	if p.View.Status != "prepared" {
		return p.View, errors.New("migration is partial; inspect its preserved output before retrying")
	}
	if p.View.RequiresWriterStopped && !o.WriterStopped {
		return p.View, errors.New("untrack migration requires --writer-stopped after the exact recorder exits")
	}
	e = lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		return gitx.WithLifecycleLock(ctx, s.Common, func() error {
			if e := s.validateSnapshots(ctx, p); e != nil {
				return e
			}
			refs, e := frozenRefs(ctx, s.Root)
			if e != nil || !bytes.Equal(encode(refs), encode(p.Refs)) {
				return ErrStale
			}
			anchor, e := outputSlot(p.View.Output)
			if e != nil || anchor != p.OutputAnchor {
				return ErrStale
			}
			if p.FilterExecutable != "" {
				data, e := safefile.ReadStablePath(ctx, p.FilterExecutable, 16<<20)
				if e != nil || hash(data) != p.FilterDigest {
					return ErrStale
				}
			}
			paths := []string{}
			for _, path := range p.MigrationPaths {
				paths = append(paths, filepath.Join(s.Root, filepath.FromSlash(path)))
			}
			if p.View.RequiresWriterStopped && o.Guard != nil {
				if e = o.Guard(ctx, s.Root, paths); e != nil {
					return e
				}
			}
			p.View.Status = "applying"
			if e = s.save(ctx, &p); e != nil {
				return e
			}
			if e = privatefile.MakeDir(p.View.Output); e != nil {
				return e
			}
			original := filepath.Join(p.View.Output, "original.git")
			if _, e = gitText(ctx, s.Root, "clone", "--mirror", "--no-hardlinks", "--", s.Root, original); e != nil {
				return e
			}
			for ref, want := range p.Refs {
				got, e := gitText(ctx, original, "rev-parse", "--verify", ref)
				if e != nil || got != want {
					return errors.New("backup does not contain every frozen ref")
				}
			}
			if _, e = gitText(ctx, original, "fsck", "--full", "--no-reflogs"); e != nil {
				return errors.New("original backup object verification failed")
			}
			bundle := filepath.Join(p.View.Output, "original.bundle")
			if _, e = gitText(ctx, original, "bundle", "create", bundle, "--all", "HEAD"); e != nil {
				return e
			}
			if _, e = gitText(ctx, original, "bundle", "verify", bundle); e != nil {
				return e
			}
			restored := filepath.Join(p.View.Output, "restore-check.git")
			if _, e = gitText(ctx, original, "clone", "--mirror", "--", bundle, restored); e != nil {
				return e
			}
			if _, e = gitText(ctx, restored, "update-ref", "--no-deref", "HEAD", p.Refs["HEAD"]); e != nil {
				return e
			}
			if _, e = gitText(ctx, restored, "fsck", "--full", "--no-reflogs"); e != nil {
				return errors.New("bundle restore verification failed")
			}
			for ref, want := range p.Refs {
				got, e := gitText(ctx, restored, "rev-parse", "--verify", ref)
				if e != nil || got != want {
					return errors.New("restored bundle ref mismatch")
				}
			}
			for _, snapshot := range p.Snapshots {
				data, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(id), snapshot.Payload), MaxFileBytes)
				if e != nil || hash(data) != snapshot.Digest {
					return ErrStale
				}
				target := filepath.Join(p.View.Output, "current", filepath.FromSlash(snapshot.Logical))
				if e = privatefile.EnsureDir(filepath.Dir(target)); e != nil {
					return e
				}
				if e = configedit.WritePrivate(ctx, target, data, false); e != nil {
					return e
				}
			}
			p.View.Recovery = append(p.View.Recovery, bundle)
			p.View.Completed = append(p.View.Completed, "verified-original-backup")
			if e = s.save(ctx, &p); e != nil {
				return e
			}
			if p.View.Kind == "artifact_migrate_untrack" {
				if e = s.applyUntrack(ctx, &p, o); e != nil {
					return e
				}
			} else {
				for _, projection := range []struct {
					name    string
					inverse bool
				}{{"history", false}, {"filtered", true}} {
					candidate := filepath.Join(p.View.Output, projection.name+".git")
					if _, e = gitText(ctx, original, "clone", "--mirror", "--no-hardlinks", "--", original, candidate); e != nil {
						return e
					}
					args := []string{"--force", "--prune-empty", "never", "--prune-degenerate", "never", "--preserve-commit-hashes", "--preserve-commit-encoding", "--replace-refs", "delete-no-add"}
					if projection.inverse {
						args = append(args, "--invert-paths")
					}
					for _, path := range p.MigrationPaths {
						args = append(args, "--path", path, "--path", path+"/")
					}
					r, e := (sshhost.ExecRunner{}).Run(ctx, sshhost.RunRequest{Name: p.FilterExecutable, Args: args, Dir: candidate, UnsetEnv: gitEnvNames(), Env: []string{"GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}, Display: "filter isolated artifact history"})
					if e != nil || r.ExitCode != 0 {
						return errors.New("history filtering failed; preserved original backup is unchanged")
					}
					mapping, e := safefile.ReadStablePath(ctx, filepath.Join(candidate, "filter-repo", "commit-map"), maxRecordBytes)
					if e != nil {
						return e
					}
					if e = verifyProjection(ctx, original, candidate, mapping, p.MigrationPaths, projection.inverse); e != nil {
						return e
					}
					if e = configedit.WritePrivate(ctx, filepath.Join(p.View.Output, projection.name+"-commit-map"), mapping, false); e != nil {
						return e
					}
					p.View.Completed = append(p.View.Completed, "verified-"+projection.name+"-history")
					if e = s.save(ctx, &p); e != nil {
						return e
					}
				}
			}
			manifest := struct {
				Version int               `json:"version"`
				Refs    map[string]string `json:"source_refs"`
				Paths   []string          `json:"paths"`
				Current []FileSummary     `json:"current_files"`
				Hygiene string            `json:"hygiene"`
			}{1, p.Refs, p.MigrationPaths, p.View.Files, "not-scanned"}
			if e = configedit.WritePrivate(ctx, filepath.Join(p.View.Output, "migration.json"), encode(manifest), false); e != nil {
				return e
			}
			p.View.Status = "complete"
			return s.save(ctx, &p)
		})
	})
	if e != nil && p.View.Status == "applying" {
		p.View.Status = "partial"
		_ = s.save(context.Background(), &p)
	}
	return p.View, e
}

func (s *Service) applyUntrack(ctx context.Context, p *planRecord, o ApplyOptions) error {
	if e := s.checkPendingHandoffs(ctx); e != nil {
		return e
	}
	if e := s.validateSnapshots(ctx, *p); e != nil {
		return e
	}
	refs, e := frozenRefs(ctx, s.Root)
	if e != nil || !bytes.Equal(encode(refs), encode(p.Refs)) {
		return ErrStale
	}
	indexPath, e := gitText(ctx, s.Root, "rev-parse", "--git-path", "index")
	if e != nil {
		return e
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(s.Root, indexPath)
	}
	index, e := readIndex(ctx, indexPath)
	if e != nil {
		return e
	}
	backup := filepath.Join(p.View.Output, "source-index.before")
	if e = configedit.WritePrivate(ctx, backup, index, false); e != nil {
		return e
	}
	p.View.Recovery = append(p.View.Recovery, backup)
	names, e := runGit(ctx, s.Root, nil, 8<<20, "ls-files", "-z")
	if e != nil {
		return e
	}
	var selected bytes.Buffer
	for _, name := range strings.Split(string(names), "\x00") {
		if selectedPath(name, p.MigrationPaths) {
			selected.WriteString(name)
			selected.WriteByte(0)
		}
	}
	if selected.Len() == 0 {
		return errors.New("no tracked artifact files remain to untrack")
	}
	var editors []configedit.Plan
	for _, c := range p.Changes {
		data, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(p.View.ID), c.Desired), 1<<20)
		if e != nil || hash(data) != c.Digest {
			return ErrStale
		}
		editor, e := configedit.NewTextFile(ctx, c.Path, data)
		if e != nil {
			return e
		}
		token, e := editor.SourceToken(c.Path)
		if e != nil || token != c.Token {
			return ErrStale
		}
		editors = append(editors, editor)
	}
	if o.Guard != nil {
		var paths []string
		for _, path := range p.MigrationPaths {
			paths = append(paths, filepath.Join(s.Root, filepath.FromSlash(path)))
		}
		if e = o.Guard(ctx, s.Root, paths); e != nil {
			return e
		}
	}
	for _, editor := range editors {
		result, e := configedit.Apply(ctx, editor, filepath.Join(s.Dir, "recovery"))
		if result.Receipt != "" {
			p.View.Recovery = append(p.View.Recovery, result.Receipt)
		}
		if e != nil {
			return e
		}
	}
	freshIndex, e := readIndex(ctx, indexPath)
	if e != nil || !bytes.Equal(freshIndex, index) {
		return ErrStale
	}
	if _, e = runGit(ctx, s.Root, selected.Bytes(), 1024, "update-index", "--force-remove", "-z", "--stdin"); e != nil {
		return e
	}
	p.View.Completed = append(p.View.Completed, "untracked-selected-history")
	return s.save(ctx, p)
}

func gitEnvNames() []string {
	var names []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			names = append(names, key)
		}
	}
	return names
}

// The exact Git index is an authorized metadata read. Open its held directory
// directly rather than weakening ordinary content traversal's .git rejection.
func readIndex(ctx context.Context, path string) ([]byte, error) {
	root, id, e := safefile.OpenRoot(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer root.Close()
	data, _, e := safefile.ReadStableRegular(ctx, root, filepath.Base(path), nil, MaxFileBytes)
	if e != nil {
		return nil, e
	}
	if e = safefile.VerifyRoot(filepath.Dir(path), id); e != nil {
		return nil, e
	}
	return data, nil
}

func verifyProjection(ctx context.Context, original, candidate string, mapping []byte, paths []string, inverse bool) error {
	if _, e := gitText(ctx, candidate, "fsck", "--full", "--no-reflogs"); e != nil {
		return errors.New("filtered object verification failed")
	}
	seen := map[string]bool{}
	for n, line := range strings.Split(strings.TrimSpace(string(mapping)), "\n") {
		if n == 0 {
			if line != "old                                      new" && strings.Join(strings.Fields(line), " ") != "old new" {
				return errors.New("invalid filter commit map")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !objectID.MatchString(fields[0]) || !objectID.MatchString(fields[1]) || strings.Trim(fields[1], "0") == "" {
			return errors.New("filter unexpectedly removed or invalidated a commit")
		}
		old, new := fields[0], fields[1]
		seen[old] = true
		before, e := runGit(ctx, original, nil, 8<<20, "ls-tree", "-r", "-z", old)
		if e != nil {
			return e
		}
		var expected bytes.Buffer
		for _, entry := range bytes.Split(before, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			_, name, ok := bytes.Cut(entry, []byte{'\t'})
			if !ok {
				return errors.New("invalid source tree")
			}
			selected := selectedPath(string(name), paths)
			if selected != inverse {
				expected.Write(entry)
				expected.WriteByte(0)
			}
		}
		after, e := runGit(ctx, candidate, nil, 8<<20, "ls-tree", "-r", "-z", new)
		if e != nil || !bytes.Equal(after, expected.Bytes()) {
			return errors.New("filtered tree differs from the exact selected-path projection")
		}
	}
	commits, e := gitText(ctx, original, "rev-list", "--all")
	if e != nil {
		return e
	}
	for _, id := range strings.Fields(commits) {
		if !seen[id] {
			return errors.New("commit map does not cover original refs")
		}
	}
	return nil
}
