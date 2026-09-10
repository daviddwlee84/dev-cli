package agentskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const defaultUpdateWorkers = 4

type sourceCheckout struct {
	dir     string
	cleanup func()
}

type updateCheckDeps struct {
	clone   func(context.Context, string, string) (sourceCheckout, error)
	check   func(context.Context, string, Skill) (UpdateStatus, string)
	workers int
}

type updateMember struct {
	index int
	url   string
	ref   string
}

type updateGroup struct {
	key     string
	members []updateMember
}

type rowUpdate struct {
	index  int
	status UpdateStatus
	detail string
}

type updateGroupResult struct {
	position int
	updates  []rowUpdate
}

// CheckUpdates compares lock-recorded content with current Git sources. Source
// URL and ref pairs are fetched once, execution is bounded, installed files are
// never written, and output row order is unchanged.
func CheckUpdates(ctx context.Context, rows []Skill) []Skill {
	return checkUpdatesWith(ctx, rows, updateCheckDeps{
		clone: cloneSourceCheckout, check: checkOneResult, workers: defaultUpdateWorkers,
	})
}

func checkUpdatesWith(ctx context.Context, rows []Skill, deps updateCheckDeps) []Skill {
	out := append([]Skill(nil), rows...)
	groupsByKey := map[string][]updateMember{}
	for index := range out {
		metadata := skillLock(out[index])
		if out[index].ManagedBy != ManagedBySkills || metadata == nil {
			continue
		}
		url, ok := sourceURL(*metadata)
		if !ok || metadata.SkillPath == "" {
			out[index].UpdateStatus = UpdateUnknown
			out[index].UpdateDetail = "lock entry has no checkable Git source and skill path"
			continue
		}
		if !safeGitRef(metadata.Ref) {
			out[index].UpdateStatus = UpdateUnknown
			out[index].UpdateDetail = "lock entry has an invalid Git ref"
			continue
		}
		key := url + "\x00" + metadata.Ref
		groupsByKey[key] = append(groupsByKey[key], updateMember{index: index, url: url, ref: metadata.Ref})
	}

	keys := make([]string, 0, len(groupsByKey))
	for key := range groupsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([]updateGroup, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, updateGroup{key: key, members: groupsByKey[key]})
	}
	if len(groups) == 0 {
		return out
	}
	if deps.clone == nil {
		deps.clone = cloneSourceCheckout
	}
	if deps.check == nil {
		deps.check = checkOneResult
	}
	if deps.workers <= 0 {
		deps.workers = defaultUpdateWorkers
	}
	if deps.workers > len(groups) {
		deps.workers = len(groups)
	}

	jobs := make(chan int, len(groups))
	results := make(chan updateGroupResult, len(groups))
	for position := range groups {
		jobs <- position
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(deps.workers)
	for range deps.workers {
		go func() {
			defer workers.Done()
			for position := range jobs {
				group := groups[position]
				result := updateGroupResult{position: position}
				if err := ctx.Err(); err != nil {
					result.updates = failedGroup(group, "update check canceled: "+err.Error())
					results <- result
					continue
				}
				checkout, err := deps.clone(ctx, group.members[0].url, group.members[0].ref)
				if err != nil {
					detail := "could not fetch source"
					if ctxErr := ctx.Err(); ctxErr != nil {
						detail = "update check canceled: " + ctxErr.Error()
					}
					result.updates = failedGroup(group, detail)
					results <- result
					continue
				}
				func() {
					if checkout.cleanup != nil {
						defer checkout.cleanup()
					}
					for _, member := range group.members {
						if err := ctx.Err(); err != nil {
							result.updates = append(result.updates, rowUpdate{member.index, UpdateFailed, "update check canceled: " + err.Error()})
							continue
						}
						status, detail := deps.check(ctx, checkout.dir, out[member.index])
						if err := ctx.Err(); err != nil {
							status, detail = UpdateFailed, "update check canceled: "+err.Error()
						}
						result.updates = append(result.updates, rowUpdate{member.index, status, detail})
					}
				}()
				results <- result
			}
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	orderedResults := make([]updateGroupResult, len(groups))
	for result := range results {
		orderedResults[result.position] = result
	}
	for _, result := range orderedResults {
		for _, update := range result.updates {
			out[update.index].UpdateStatus = update.status
			out[update.index].UpdateDetail = update.detail
		}
	}
	return out
}

func failedGroup(group updateGroup, detail string) []rowUpdate {
	updates := make([]rowUpdate, 0, len(group.members))
	for _, member := range group.members {
		updates = append(updates, rowUpdate{member.index, UpdateFailed, detail})
	}
	return updates
}

func skillLock(row Skill) *LockMetadata {
	if row.Lock != nil {
		return row.Lock
	}
	return row.lock
}

func sourceURL(entry lockEntry) (string, bool) {
	if entry.SourceType == "local" || entry.SourceType == "node_modules" || entry.SourceType == "well-known" {
		return "", false
	}
	if entry.SourceURL != "" {
		return safeGitURL(entry.SourceURL)
	}
	if entry.SourceType == "github" {
		parts := strings.Split(strings.Trim(entry.Source, "/"), "/")
		if len(parts) >= 2 {
			return safeGitURL("https://github.com/" + parts[0] + "/" + parts[1] + ".git")
		}
	}
	if strings.Contains(entry.Source, "://") || strings.HasSuffix(entry.Source, ".git") {
		return safeGitURL(entry.Source)
	}
	return "", false
}

func safeGitURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	return value, true
}

func safeGitRef(value string) bool {
	return !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}

func cloneSourceCheckout(ctx context.Context, url, ref string) (sourceCheckout, error) {
	dir, err := cloneSource(ctx, url, ref)
	if err != nil {
		return sourceCheckout{}, err
	}
	parent := filepath.Dir(dir)
	return sourceCheckout{dir: dir, cleanup: func() { _ = os.RemoveAll(parent) }}, nil
}

func cloneSource(ctx context.Context, url, ref string) (string, error) {
	checkedURL, ok := safeGitURL(url)
	if !ok || !safeGitRef(ref) {
		return "", errors.New("invalid Git source URL or ref")
	}
	url = checkedURL
	parent, err := os.MkdirTemp("", "dev-skill-check-*")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "source")
	if _, err := gitOutput(ctx, "", "clone", "--quiet", "--depth", "1", "--filter=blob:none", "--", url, dir); err != nil {
		_ = os.RemoveAll(parent)
		return "", err
	}
	if ref != "" {
		if _, err := gitOutput(ctx, dir, "fetch", "--quiet", "--depth", "1", "--", "origin", ref); err != nil {
			_ = os.RemoveAll(parent)
			return "", err
		}
		if _, err := gitOutput(ctx, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
			_ = os.RemoveAll(parent)
			return "", err
		}
	}
	return dir, nil
}

func normalizeRecordedHash(metadata *LockMetadata) {
	// computedHash uniquely identifies the project-v1 folder hash and is also
	// understood when older package-local callers omitted Scope.
	if hash := strings.TrimSpace(metadata.ComputedHash); hash != "" {
		metadata.RecordedHash = hash
		metadata.HashKind = "sha256-folder"
		return
	}
	if hash := strings.TrimSpace(metadata.SkillFolderHash); hash != "" {
		metadata.RecordedHash = hash
		if len(hash) == 40 {
			metadata.HashKind = "git-tree"
		} else {
			metadata.HashKind = "sha256-folder"
		}
		return
	}
	if hash := strings.TrimSpace(metadata.ContentHash); hash != "" {
		metadata.RecordedHash = hash
		metadata.HashKind = "sha256-skill-file"
	}
}

func checkOne(ctx context.Context, repoDir string, row *Skill) {
	status, detail := checkOneResult(ctx, repoDir, *row)
	row.UpdateStatus, row.UpdateDetail = status, detail
}

func checkOneResult(ctx context.Context, repoDir string, row Skill) (UpdateStatus, string) {
	entry := skillLock(row)
	if entry == nil {
		return UpdateUnknown, "skill has no lock metadata"
	}
	folder, ok := safeSkillFolder(entry.SkillPath)
	if !ok {
		return UpdateUnknown, "invalid skill path in lock entry"
	}
	abs := filepath.Join(repoDir, filepath.FromSlash(folder))
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		if os.IsNotExist(err) {
			return UpdateMissing, "skill path no longer exists upstream"
		}
		return UpdateFailed, "could not inspect upstream skill path"
	}
	if !insidePath(repoDir, abs) {
		return UpdateUnknown, "skill path escapes the fetched source"
	}

	expected, kind := entry.RecordedHash, entry.HashKind
	if expected == "" {
		copy := *entry
		normalizeRecordedHash(&copy)
		expected, kind = copy.RecordedHash, copy.HashKind
	}
	var (
		actual    string
		actualErr error
	)
	switch kind {
	case "git-tree":
		spec := "HEAD^{tree}"
		if folder != "" {
			spec = "HEAD:" + folder
		}
		actual, actualErr = gitOutput(ctx, repoDir, "rev-parse", spec)
	case "sha256-folder":
		actual, actualErr = folderHashContext(ctx, abs)
	case "sha256-skill-file":
		actual, actualErr = fileHashContext(ctx, filepath.Join(abs, "SKILL.md"))
	}
	if err := ctx.Err(); err != nil {
		return UpdateFailed, "update check canceled: " + err.Error()
	}
	if actualErr != nil {
		return UpdateUnknown, "could not compare upstream content hash"
	}
	if expected == "" || actual == "" {
		return UpdateUnknown, "lock entry has no comparable content hash"
	}
	if strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected)) {
		return UpdateCurrent, "matches the recorded upstream content"
	}
	return UpdateAvailable, "upstream content changed"
}

func safeSkillFolder(skillPath string) (string, bool) {
	p := strings.ReplaceAll(strings.TrimSpace(skillPath), "\\", "/")
	if p == "" || strings.ContainsRune(p, '\x00') {
		return "", false
	}
	// filepath.VolumeName follows the host OS, so reject a Windows drive prefix
	// explicitly even when dev is checking a lock file on Unix.
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) {
		return "", false
	}
	if strings.EqualFold(path.Base(p), "SKILL.md") {
		p = path.Dir(p)
	}
	if p == "." {
		p = ""
	}
	clean := path.Clean(p)
	if clean == "." {
		clean = ""
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || filepath.VolumeName(filepath.FromSlash(clean)) != "" {
		return "", false
	}
	return clean, true
}

func folderHash(dir string) (string, error) {
	return folderHashContext(context.Background(), dir)
}

func folderHashContext(ctx context.Context, dir string) (string, error) {
	type file struct {
		name string
		path string
	}
	var files []file
	err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && filename != dir && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(dir, filename)
			if err != nil {
				return err
			}
			files = append(files, file{name: filepath.ToSlash(relative), path: filename})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	digest := sha256.New()
	buffer := make([]byte, 32<<10)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(digest, file.name)
		if err := streamFileHash(ctx, digest, file.path, buffer); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func fileHash(filename string) (string, error) {
	return fileHashContext(context.Background(), filename)
}

func fileHashContext(ctx context.Context, filename string) (string, error) {
	digest := sha256.New()
	if err := streamFileHash(ctx, digest, filename, make([]byte, 32<<10)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func streamFileHash(ctx context.Context, destination io.Writer, filename string, buffer []byte) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := destination.Write(buffer[:read]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func insidePath(root, candidate string) bool {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootResolved, candidateResolved)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
