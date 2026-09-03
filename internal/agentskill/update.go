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
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const defaultUpdateWorkers = 4

var (
	errGitSkillPathMissing  = errors.New("skill path is absent from Git tree")
	errProviderHashOrdering = errors.New("provider hash ordering is locale-dependent")
)

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
		if _, safe := safeSkillFolder(metadata.SkillPath); !safe {
			out[index].UpdateStatus = UpdateUnknown
			out[index].UpdateDetail = "invalid skill path in lock entry"
			continue
		}
		normalized := *metadata
		if normalized.RecordedHash == "" || normalized.HashKind == "" {
			normalizeRecordedHash(&normalized)
		}
		if normalized.RecordedHash == "" || normalized.HashKind == "" {
			out[index].UpdateStatus = UpdateUnknown
			out[index].UpdateDetail = "lock entry has no comparable content hash"
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
					cached := map[string]rowUpdate{}
					for _, member := range group.members {
						if err := ctx.Err(); err != nil {
							result.updates = append(result.updates, rowUpdate{member.index, UpdateFailed, "update check canceled: " + err.Error()})
							continue
						}
						metadata := *skillLock(out[member.index])
						if metadata.RecordedHash == "" || metadata.HashKind == "" {
							normalizeRecordedHash(&metadata)
						}
						cacheKey := metadata.SkillPath + "\x00" + metadata.HashKind + "\x00" + metadata.RecordedHash
						if previous, ok := cached[cacheKey]; ok {
							previous.index = member.index
							result.updates = append(result.updates, previous)
							continue
						}
						status, detail := deps.check(ctx, checkout.dir, out[member.index])
						update := rowUpdate{member.index, status, detail}
						cached[cacheKey] = update
						result.updates = append(result.updates, update)
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

func skillLock(row Skill) *LockMetadata { return row.Lock }

func sourceURL(entry LockMetadata) (string, bool) {
	if entry.SourceType == "local" || entry.SourceType == "node_modules" || entry.SourceType == "well-known" {
		return "", false
	}
	if entry.SourceURL != "" {
		return entry.SourceURL, true
	}
	if entry.SourceType == "github" {
		parts := strings.Split(strings.Trim(entry.Source, "/"), "/")
		if len(parts) >= 2 {
			return "https://github.com/" + parts[0] + "/" + parts[1] + ".git", true
		}
	}
	if strings.Contains(entry.Source, "://") || strings.HasSuffix(entry.Source, ".git") {
		return entry.Source, true
	}
	return "", false
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
	if !safeFetchRef(ref) {
		return "", errors.New("skill source ref is not safe to pass to Git")
	}
	parent, err := os.MkdirTemp("", "dev-skill-check-*")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "source")
	if _, err := gitOutput(ctx, "", "clone", "--quiet", "--no-checkout", "--depth", "1", "--filter=blob:none", "--", url, dir); err != nil {
		_ = os.RemoveAll(parent)
		return "", err
	}
	if ref != "" {
		if _, err := gitOutput(ctx, dir, "fetch", "--quiet", "--depth", "1", "--", "origin", ref); err != nil {
			_ = os.RemoveAll(parent)
			return "", err
		}
		if _, err := gitOutput(ctx, dir, "update-ref", "HEAD", "FETCH_HEAD"); err != nil {
			_ = os.RemoveAll(parent)
			return "", err
		}
	}
	return dir, nil
}

func safeFetchRef(ref string) bool {
	if ref == "" {
		return true
	}
	if strings.TrimSpace(ref) != ref || strings.HasPrefix(ref, "-") {
		return false
	}
	return !strings.ContainsFunc(ref, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	})
}

func normalizeRecordedHash(metadata *LockMetadata) {
	metadata.RecordedHash, metadata.HashKind = "", ""
	computed := strings.TrimSpace(metadata.ComputedHash)
	folder := strings.TrimSpace(metadata.SkillFolderHash)
	content := strings.TrimSpace(metadata.ContentHash)

	setFolderHash := func(hash string) bool {
		switch {
		case validHexHash(hash, 40):
			metadata.RecordedHash, metadata.HashKind = hash, "git-tree"
			return true
		case validHexHash(hash, 64):
			metadata.RecordedHash, metadata.HashKind = hash, "sha256-folder"
			return true
		default:
			return false
		}
	}
	if metadata.Scope == ScopeGlobal {
		if setFolderHash(folder) {
			return
		}
		if validHexHash(content, 64) {
			metadata.RecordedHash, metadata.HashKind = content, "sha256-skill-file"
		}
		return
	}
	if validHexHash(computed, 64) {
		metadata.RecordedHash, metadata.HashKind = computed, "sha256-folder"
		return
	}
	// Scope-less package tests and older callers may still supply either global
	// representation; parsed project-v1 locks never reach this branch with them.
	if metadata.Scope == "" && setFolderHash(folder) {
		return
	}
	if metadata.Scope == "" && validHexHash(content, 64) {
		metadata.RecordedHash, metadata.HashKind = content, "sha256-skill-file"
	}
}

func validHexHash(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
	expected, kind := entry.RecordedHash, entry.HashKind
	if expected == "" {
		copy := *entry
		normalizeRecordedHash(&copy)
		expected, kind = copy.RecordedHash, copy.HashKind
	}
	var actual string
	var hashErr error
	switch kind {
	case "git-tree":
		spec := "HEAD^{tree}"
		if folder != "" {
			spec = "HEAD:" + folder
		}
		actual, hashErr = gitOutput(ctx, repoDir, "rev-parse", "--verify", spec)
	case "sha256-folder":
		actual, hashErr = gitFolderHashContext(ctx, repoDir, folder)
	case "sha256-skill-file":
		actual, hashErr = gitSkillFileHashContext(ctx, repoDir, folder)
	}
	if hashErr != nil {
		switch {
		case ctx.Err() != nil:
			return UpdateFailed, "update check canceled"
		case errors.Is(hashErr, errGitSkillPathMissing):
			return UpdateMissing, "skill path no longer exists upstream"
		case errors.Is(hashErr, errProviderHashOrdering):
			return UpdateUnknown, "lock hash ordering is unverifiable for non-ASCII paths"
		default:
			return UpdateFailed, "could not compute upstream content hash"
		}
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

type gitTreeFile struct {
	name string
	oid  string
}

func gitFolderHashContext(ctx context.Context, repoDir, folder string) (string, error) {
	files, err := gitTreeFiles(ctx, repoDir, folder)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		if !isASCII(file.name) {
			return "", errProviderHashOrdering
		}
	}
	collator := collate.New(language.English)
	sort.SliceStable(files, func(i, j int) bool {
		if order := collator.CompareString(files[i].name, files[j].name); order != 0 {
			return order < 0
		}
		return files[i].name < files[j].name
	})
	hash := sha256.New()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, file.name)
		if err := writeGitBlob(ctx, repoDir, file.oid, hash); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func gitSkillFileHashContext(ctx context.Context, repoDir, folder string) (string, error) {
	files, err := gitTreeFiles(ctx, repoDir, folder)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		if file.name != "SKILL.md" {
			continue
		}
		hash := sha256.New()
		if err := writeGitBlob(ctx, repoDir, file.oid, hash); err != nil {
			return "", err
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	return "", errGitSkillPathMissing
}

func gitTreeFiles(ctx context.Context, repoDir, folder string) ([]gitTreeFile, error) {
	args := []string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree", "HEAD", "--"}
	if folder != "" {
		args = append(args, folder)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repoDir
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	prefix := ""
	if folder != "" {
		prefix = strings.TrimSuffix(folder, "/") + "/"
	}
	var files []gitTreeFile
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, errors.New("unexpected git tree record")
		}
		fields := bytes.Fields(header)
		if len(fields) != 3 || string(fields[1]) != "blob" || !strings.HasPrefix(string(fields[0]), "100") {
			continue
		}
		fullPath := filepath.ToSlash(string(rawPath))
		name := fullPath
		if prefix != "" {
			if !strings.HasPrefix(fullPath, prefix) {
				continue
			}
			name = strings.TrimPrefix(fullPath, prefix)
		}
		if name == "" || ignoredHashPath(name) {
			continue
		}
		files = append(files, gitTreeFile{name: name, oid: string(fields[2])})
	}
	if len(files) == 0 {
		return nil, errGitSkillPathMissing
	}
	return files, nil
}

func writeGitBlob(ctx context.Context, repoDir, oid string, destination io.Writer) error {
	command := exec.CommandContext(ctx, "git", "cat-file", "blob", oid)
	command.Dir = repoDir
	command.Stdout = destination
	return command.Run()
}

func ignoredHashPath(name string) bool {
	for _, component := range strings.Split(filepath.ToSlash(name), "/") {
		if component == ".git" || component == "node_modules" {
			return true
		}
	}
	return false
}

func isASCII(value string) bool {
	for index := range len(value) {
		if value[index] >= 0x80 {
			return false
		}
	}
	return true
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
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
	collator := collate.New(language.English)
	sort.SliceStable(files, func(i, j int) bool {
		if order := collator.CompareString(files[i].name, files[j].name); order != 0 {
			return order < 0
		}
		return files[i].name < files[j].name
	})
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, file.name)
		input, openedInfo, err := safefile.OpenRegular(file.path)
		if err != nil {
			return "", err
		}
		currentInfo, err := os.Lstat(file.path)
		if err != nil || !os.SameFile(openedInfo, currentInfo) {
			_ = input.Close()
			return "", errors.New("skill hash target changed while opening")
		}
		for {
			if err := ctx.Err(); err != nil {
				_ = input.Close()
				return "", err
			}
			read, readErr := input.Read(buffer)
			if read > 0 {
				_, _ = hash.Write(buffer[:read])
			}
			if readErr != nil {
				_ = input.Close()
				if errors.Is(readErr, io.EOF) {
					break
				}
				return "", readErr
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileHashContext(ctx context.Context, filename string) (string, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("skill hash target is not a regular file")
	}
	input, openedInfo, err := safefile.OpenRegular(filename)
	if err != nil {
		return "", err
	}
	defer input.Close()
	currentInfo, err := os.Lstat(filename)
	if err != nil || !os.SameFile(openedInfo, currentInfo) {
		return "", errors.New("skill hash target changed while opening")
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, readErr := input.Read(buffer)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
