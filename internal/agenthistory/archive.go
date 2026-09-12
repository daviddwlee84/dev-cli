package agenthistory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

var preamble = regexp.MustCompile(`(?m)^<!-- ([^<\r\n]+?) Session ([0-9a-fA-F-]+) \(`)

func sessionIdentity(data []byte) (string, string, error) {
	if len(data) > 16<<10 {
		data = data[:16<<10]
	}
	m := preamble.FindSubmatch(data)
	if len(m) != 3 {
		return "", "", errors.New("SpecStory session preamble not found")
	}
	provider := strings.ToLower(strings.Fields(string(m[1]))[0])
	id := strings.ToLower(string(m[2]))
	if !component.MatchString(provider) || !validID(id) {
		return "", "", errors.New("invalid SpecStory session identity")
	}
	return provider, id, nil
}
func parseSession(s string) (string, string, error) {
	p, id, ok := strings.Cut(s, ":")
	if !ok || !component.MatchString(p) || !validID(id) {
		return "", "", errors.New("session must be provider:uuid")
	}
	return p, id, nil
}

func (s *Service) capturePath(logical string) string {
	if s.Policy.Capture == "external" && strings.HasPrefix(logical, ".specstory/history/") {
		return filepath.Join(s.Binding.CaptureDir, filepath.FromSlash(strings.TrimPrefix(logical, ".specstory/history/")))
	}
	return filepath.Join(s.Root, filepath.FromSlash(logical))
}
func (s *Service) sources(ctx context.Context) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	for _, configured := range s.Policy.Paths {
		root := filepath.Join(s.Root, filepath.FromSlash(configured))
		if s.Policy.Capture == "external" {
			if configured != ".specstory/history" || s.Binding.CaptureDir == "" {
				return nil, errors.New("external capture binding is unavailable")
			}
			root = s.Binding.CaptureDir
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			if err != nil {
				return errors.New("artifact source directory unavailable")
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("artifact source contains a symlink; select regular files")
			}
			if entry.IsDir() {
				if strings.Count(strings.TrimPrefix(path, root), string(filepath.Separator)) > 32 {
					return errors.New("artifact directory depth exceeds limit")
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("artifact source contains a special file")
			}
			if s.Policy.Source == "specstory" && !strings.HasSuffix(entry.Name(), ".md") {
				return nil
			}
			rel, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			logical := configured
			if rel != "." {
				logical = filepath.ToSlash(filepath.Join(configured, rel))
			}
			if !relative(logical) {
				return errors.New("unsafe artifact source name")
			}
			if !seen[logical] {
				files = append(files, logical)
				seen[logical] = true
			}
			if len(files) > 10000 {
				return errors.New("artifact inventory exceeds 10000 files; select narrower policy paths")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// Prefix reads only select candidates. Full source identity and bytes are
// checked independently before a candidate can become an archive plan.
func prefixIdentity(path string) (string, string, error) {
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return "", "", errors.New("noncanonical transcript")
	}
	f, _, e := safefile.OpenRegular(path)
	if e != nil {
		return "", "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 16<<10))
	if e != nil {
		return "", "", e
	}
	return sessionIdentity(b)
}

func (s *Service) PreviewArchive(ctx context.Context, o ArchiveOptions) (Plan, error) {
	p, err := s.newPlan(ctx, "artifact_archive")
	if err != nil {
		return p.View, err
	}
	if !s.Configured || s.Policy.Mode != "archive" {
		return p.View, errors.New("configure archive mode before preserving snapshots")
	}
	if err = s.validateCapture(ctx); err != nil {
		return p.View, err
	}
	archive, err := archiveCheckout(ctx, s.Root, s.Binding.Archive)
	if err != nil {
		return p.View, err
	}
	p.View.Archive = archive
	p.View.RequiresWriterStopped = true
	p.ArchiveToken, p.ArchiveHead, p.ArchiveIndex, err = checkoutToken(ctx, archive)
	if err != nil {
		return p.View, err
	}
	if err = cleanArchive(ctx, archive); err != nil {
		return p.View, err
	}
	files, err := s.sources(ctx)
	if err != nil {
		return p.View, err
	}
	selected := map[string]bool{}
	for _, f := range o.Files {
		if !relative(f) {
			return p.View, errors.New("select source-relative artifact files")
		}
		selected[f] = true
	}
	if len(selected) == 0 && o.Session == "" {
		return p.View, errors.New("select --session or exact --file paths")
	}
	wantProvider, wantID := "", ""
	if o.Session != "" {
		wantProvider, wantID, err = parseSession(o.Session)
		if err != nil {
			return p.View, err
		}
	}
	var chosen []string
	for _, logical := range files {
		if len(selected) > 0 && !selected[logical] {
			continue
		}
		if o.Session != "" {
			provider, id, e := prefixIdentity(s.capturePath(logical))
			if e != nil || provider != wantProvider || id != wantID {
				continue
			}
		}
		chosen = append(chosen, logical)
		delete(selected, logical)
	}
	if len(selected) > 0 {
		return p.View, errors.New("a selected file is outside the configured artifact scope")
	}
	if len(chosen) > MaxFiles {
		return p.View, errors.New("archive at most 256 files per plan")
	}
	if len(chosen) == 0 || o.Session != "" && len(chosen) != 1 {
		return p.View, errors.New("session/file selection must identify an unambiguous existing source")
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	for n, logical := range chosen {
		path := s.capturePath(logical)
		token, e := configedit.InspectToken(ctx, path)
		if e != nil {
			return p.View, errors.New("artifact source is unavailable, changing, unsafe or over 128 MiB")
		}
		data, e := safefile.ReadStablePath(ctx, path, MaxFileBytes)
		if e != nil {
			return p.View, errors.New("cannot take stable artifact snapshot")
		}
		provider, id := "files", uuid.NewSHA1(uuid.NameSpaceURL, []byte(s.Policy.ProjectID+"\x00"+logical)).String()
		if s.Policy.Source == "specstory" {
			provider, id, e = sessionIdentity(data)
			if e != nil {
				return p.View, e
			}
			if o.Session != "" && (provider != wantProvider || id != wantID) {
				return p.View, ErrStale
			}
		}
		item := snapshot{Path: path, Logical: logical, Token: token, Digest: hash(data), Provider: provider, SessionID: id, Payload: fmt.Sprintf("snapshot-%03d", n)}
		if e = configedit.WritePrivate(ctx, filepath.Join(s.planDir(p.View.ID), item.Payload+".original"), data, false); e != nil {
			return p.View, e
		}
		summary := FileSummary{File: logical, Bytes: int64(len(data)), Digest: s.opaque(item.Digest), Scan: "not-scanned"}
		if s.Binding.Protection != "off" {
			h, e := s.protector(ctx)
			if e != nil {
				return p.View, e
			}
			r, e := h.InspectSnapshot(ctx, logical, data, s.Binding.Protection == "redact")
			if r.Report.ID != "" {
				p.View.Reports = append(p.View.Reports, r.Report)
			}
			if e != nil {
				p.View.Status = "blocked"
				return p.View, e
			}
			data = r.Data
			item.ScanInputs = r.InputDigest
			summary.Scan = r.Report.Status
			summary.Replacements = r.Replacements
			summary.Cached = r.Cached
		}
		item.DesiredDigest = hash(data)
		if e = configedit.WritePrivate(ctx, filepath.Join(s.planDir(p.View.ID), item.Payload), data, false); e != nil {
			return p.View, e
		}
		fresh, e := configedit.InspectToken(ctx, path)
		if e != nil || fresh != token {
			return p.View, ErrStale
		}
		p.Snapshots = append(p.Snapshots, item)
		p.View.Files = append(p.View.Files, summary)
	}
	if err = s.current(ctx, p); err != nil {
		return p.View, err
	}
	p.AttributesToken, err = checkArchiveAttributes(ctx, archive, p, false)
	if err != nil {
		return p.View, err
	}
	p.View.ReviewDir = s.planDir(p.View.ID)
	p.View.Notices = append(p.View.Notices, "Apply creates an archive commit. Original files and the source index stay unchanged; no push occurs.", "Review payloads privately; .original files contain the unmodified input and never enter the archive automatically.")
	p.View.Notices = append(p.View.Notices, "The project archive receives .gitattributes disabling text/ident conversion for owned snapshots and records. Custom content filters are refused, never bypassed.")
	if err = s.save(ctx, &p); err != nil {
		return p.View, err
	}
	return p.View, nil
}

func cleanArchive(ctx context.Context, root string) error {
	status, e := gitText(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil || status != "" {
		return errors.New("archive checkout and index must be clean")
	}
	return nil
}

func (s *Service) validateSnapshots(ctx context.Context, p planRecord) error {
	if err := s.current(ctx, p); err != nil {
		return err
	}
	for _, snapshot := range p.Snapshots {
		token, err := configedit.InspectToken(ctx, snapshot.Path)
		if err != nil || token != snapshot.Token {
			return ErrStale
		}
		data, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(p.View.ID), snapshot.Payload), MaxFileBytes)
		if e != nil || hash(data) != snapshot.DesiredDigest {
			return errors.New("archive proposal content changed")
		}
		if snapshot.ScanInputs != "" {
			h, e := s.protector(ctx)
			if e != nil {
				return e
			}
			inputs, e := h.SnapshotInputs(ctx)
			if e != nil || inputs != snapshot.ScanInputs {
				return ErrStale
			}
		}
	}
	return nil
}

func (s *Service) ApplyArchive(ctx context.Context, id string, o ApplyOptions) (Plan, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return p.View, err
	}
	if p.View.Kind != "artifact_archive" {
		return p.View, errors.New("plan is not an artifact archive")
	}
	if p.View.Status == "complete" {
		return p.View, s.verifyReceipt(ctx, p)
	}
	if p.View.Status != "prepared" {
		return p.View, errors.New("archive operation is partial; inspect its receipt before preparing a new plan")
	}
	if !o.WriterStopped {
		return p.View, errors.New("archive finalization requires --writer-stopped after the exact recorder exits")
	}
	err = lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		return gitx.WithLifecycleLock(ctx, s.Common, func() error {
			archive, e := archiveCheckout(ctx, s.Root, p.Binding.Archive)
			if e != nil {
				return e
			}
			repo, e := gitx.Discover(ctx, archive)
			if e != nil {
				return e
			}
			return gitx.WithLifecycleLock(ctx, repo.GitCommonDir, func() error {
				if e := s.validateSnapshots(ctx, p); e != nil {
					return e
				}
				paths := []string{}
				for _, item := range p.Snapshots {
					paths = append(paths, item.Path)
				}
				if o.Guard != nil {
					if e := o.Guard(ctx, s.Root, paths); e != nil {
						return e
					}
				}
				token, _, _, e := checkoutToken(ctx, archive)
				if e != nil || token != p.ArchiveToken {
					return ErrStale
				}
				if e = cleanArchive(ctx, archive); e != nil {
					return e
				}
				attributes, e := checkArchiveAttributes(ctx, archive, p, false)
				if e != nil || attributes != p.AttributesToken {
					return ErrStale
				}
				p.View.Status = "applying"
				if e = s.save(ctx, &p); e != nil {
					return e
				}
				_, sourceHead, _, e := checkoutToken(ctx, s.Root)
				if e != nil {
					return e
				}
				if sourceHead == "unborn" {
					sourceHead = ""
				}
				var index bytes.Buffer
				expected := map[string]string{}
				attributePath := "projects/" + p.Policy.ProjectID + "/.gitattributes"
				attributeTarget := filepath.Join(archive, filepath.FromSlash(attributePath))
				if e = safeWriteParent(ctx, filepath.Dir(attributeTarget)); e != nil {
					return e
				}
				if e = privatefile.EnsureDir(filepath.Dir(attributeTarget)); e != nil {
					return e
				}
				if existing, e := safefile.ReadStablePath(ctx, attributeTarget, 1<<20); e == nil {
					if string(existing) != archiveAttributes {
						return errors.New("archive project attributes already contain custom rules")
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					return e
				} else if e = configedit.WritePrivate(ctx, attributeTarget, []byte(archiveAttributes), false); e != nil {
					return e
				}
				if _, e = checkArchiveAttributes(ctx, archive, p, true); e != nil {
					return e
				}
				attributeOID, e := runGit(ctx, archive, []byte(archiveAttributes), 1024, "hash-object", "-w", "--stdin")
				if e != nil {
					return e
				}
				expected[attributePath] = strings.TrimSpace(string(attributeOID))
				fmt.Fprintf(&index, "100644 %s\t%s%c", expected[attributePath], attributePath, 0)
				for n, item := range p.Snapshots {
					data, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(id), item.Payload), MaxFileBytes)
					if e != nil || hash(data) != item.DesiredDigest {
						return ErrStale
					}
					content := filepath.ToSlash(filepath.Join("projects", p.Policy.ProjectID, "sessions", item.Provider, item.SessionID, item.DesiredDigest+".md"))
					record := Record{Version: 1, ProjectID: p.Policy.ProjectID, Source: p.Policy.Source, Provider: item.Provider, SessionID: item.SessionID, SourcePath: item.Logical, SourceCommit: sourceHead, Content: content, Digest: item.DesiredDigest, OriginalDigest: item.Digest, Protection: p.Binding.Protection, Scan: p.View.Files[n].Scan, Created: time.Now().UTC()}
					metadata := filepath.ToSlash(filepath.Join("projects", p.Policy.ProjectID, "records", id, fmt.Sprintf("%03d.json", n)))
					for _, file := range []struct {
						path string
						data []byte
					}{{content, data}, {metadata, encode(record)}} {
						target := filepath.Join(archive, filepath.FromSlash(file.path))
						if e = safeWriteParent(ctx, filepath.Dir(target)); e != nil {
							return e
						}
						if e = privatefile.EnsureDir(filepath.Dir(target)); e != nil {
							return e
						}
						if existing, e := safefile.ReadStablePath(ctx, target, MaxFileBytes); e == nil {
							if !bytes.Equal(existing, file.data) {
								return errors.New("archive destination already contains different data")
							}
						} else if !errors.Is(e, os.ErrNotExist) {
							return e
						} else if e = configedit.WritePrivate(ctx, target, file.data, false); e != nil {
							return e
						}
						oid, e := runGit(ctx, archive, file.data, 1024, "hash-object", "-w", "--stdin")
						if e != nil {
							return e
						}
						object := strings.TrimSpace(string(oid))
						fmt.Fprintf(&index, "100644 %s\t%s%c", object, file.path, 0)
						expected[file.path] = object
						p.View.Completed = append(p.View.Completed, file.path)
					}
					if e = s.save(ctx, &p); e != nil {
						return e
					}
				}
				if e = s.validateSnapshots(ctx, p); e != nil {
					return e
				}
				if o.Guard != nil {
					if e = o.Guard(ctx, s.Root, paths); e != nil {
						return e
					}
				}
				fresh, _, _, e := checkoutToken(ctx, archive)
				if e != nil || fresh != p.ArchiveToken {
					return ErrStale
				}
				if _, e = runGit(ctx, archive, index.Bytes(), 1024, "update-index", "-z", "--index-info"); e != nil {
					return e
				}
				message := "chore: preserve agent history\n\nDev-Artifact-Archive: " + id
				if _, e = gitText(ctx, archive, "commit", "-m", message); e != nil {
					return errors.New("archive commit failed; source files are intact; inspect archive index and receipt")
				}
				commit, e := gitText(ctx, archive, "rev-parse", "HEAD")
				if e != nil {
					return e
				}
				p.View.ArchiveCommit = commit
				if e = verifyCommitPaths(ctx, archive, p.ArchiveHead, commit, expected); e != nil {
					return e
				}
				if e = s.validateSnapshots(ctx, p); e != nil {
					return e
				}
				p.View.Status = "complete"
				return s.save(ctx, &p)
			})
		})
	})
	if err != nil && p.View.Status == "applying" {
		p.View.Status = "partial"
		_ = s.save(context.Background(), &p)
	}
	return p.View, err
}

func verifyCommitPaths(ctx context.Context, root, parent, commit string, expected map[string]string) error {
	args := []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z"}
	if parent != "unborn" {
		args = append(args, parent)
	}
	args = append(args, commit)
	changed, e := runGit(ctx, root, nil, 8<<20, args...)
	if e != nil {
		return e
	}
	for _, path := range strings.Split(string(changed), "\x00") {
		if path != "" && expected[path] == "" {
			return errors.New("archive hook changed an unrelated path; inspect the partial receipt")
		}
	}
	for path, want := range expected {
		got, e := gitText(ctx, root, "rev-parse", commit+":"+path)
		if e != nil || got != want {
			return errors.New("archive commit does not contain the reviewed bytes")
		}
	}
	if parent != "unborn" {
		parents, e := gitText(ctx, root, "rev-list", "--parents", "-n", "1", commit)
		if e != nil || parents != commit+" "+parent {
			return errors.New("archive commit parent changed")
		}
	}
	return nil
}

func (s *Service) verifyReceipt(ctx context.Context, p planRecord) error {
	if p.View.Status != "complete" || p.View.ArchiveCommit == "" {
		return errors.New("archive receipt is incomplete")
	}
	root, e := archiveCheckout(ctx, s.Root, p.Binding.Archive)
	if e != nil {
		return e
	}
	if _, e = gitText(ctx, root, "merge-base", "--is-ancestor", p.View.ArchiveCommit, "HEAD"); e != nil {
		return errors.New("archive commit is no longer reachable")
	}
	for _, item := range p.Snapshots {
		if item.Digest != item.DesiredDigest {
			original, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(p.View.ID), item.Payload+".original"), MaxFileBytes)
			if e != nil || hash(original) != item.Digest {
				return errors.New("redacted archive original recovery is missing or changed")
			}
		}
		path := filepath.ToSlash(filepath.Join("projects", p.Policy.ProjectID, "sessions", item.Provider, item.SessionID, item.DesiredDigest+".md"))
		data, e := runGit(ctx, root, nil, MaxFileBytes, "cat-file", "blob", p.View.ArchiveCommit+":"+path)
		if e != nil || hash(data) != item.DesiredDigest {
			return errors.New("archive snapshot is missing or modified")
		}
	}
	return nil
}
