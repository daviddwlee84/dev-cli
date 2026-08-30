// Package agentskill inventories and manages skills installed through the
// upstream `skills` CLI. Listing delegates to its JSON contract; the small
// amount of policy kept here is dev-specific: merge project/global scopes,
// resolve project scope from the current Git checkout, and never turn a status
// read into an implicit package download or skill update.
package agentskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/skill"
)

// DefaultSource is the catalog opened by `dev skill add` when no package is
// supplied. It remains a shortcut to the upstream interactive wizard: dev
// never silently chooses skills, agents, or an install scope.
const DefaultSource = "daviddwlee84/agent-skills/skills"

type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

type ManagedBy string

const (
	ManagedBySkills   ManagedBy = "skills"
	ManagedByDev      ManagedBy = "dev"
	ManagedByExternal ManagedBy = "external"
)

type UpdateStatus string

const (
	UpdateUnchecked UpdateStatus = "unchecked"
	UpdateCurrent   UpdateStatus = "current"
	UpdateAvailable UpdateStatus = "update_available"
	UpdateMissing   UpdateStatus = "upstream_missing"
	UpdateUnknown   UpdateStatus = "unverifiable"
	UpdateFailed    UpdateStatus = "check_failed"
)

// Skill is one installed skill in one scope. The same name may legitimately
// appear twice, once project-local and once global; callers must not dedupe it.
type Skill struct {
	Name         string
	Scope        Scope
	ScopeRoot    string
	Path         string
	Agents       []string
	Source       string
	SourceURL    string
	SourceType   string
	ManagedBy    ManagedBy
	UpdateStatus UpdateStatus
	UpdateDetail string

	lock *lockEntry
}

// ListOptions selects scopes and the opt-in network freshness check. With no
// scope selected both are returned.
type ListOptions struct {
	Project bool
	Global  bool
	Check   bool
}

type upstreamSkill struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Scope      Scope    `json:"scope"`
	Agents     []string `json:"agents"`
	Source     *string  `json:"source"`
	SourceURL  *string  `json:"sourceUrl"`
	SourceType *string  `json:"sourceType"`
}

type lockFile struct {
	Version int                  `json:"version"`
	Skills  map[string]lockEntry `json:"skills"`
}

type lockEntry struct {
	Source          string `json:"source"`
	SourceURL       string `json:"sourceUrl"`
	SourceType      string `json:"sourceType"`
	Ref             string `json:"ref"`
	SkillPath       string `json:"skillPath"`
	ComputedHash    string `json:"computedHash"`
	SkillFolderHash string `json:"skillFolderHash"`
}

type provider struct {
	bin    string
	prefix []string
	label  string
}

// ProjectRoot uses the current linked-worktree checkout root. Falling back to
// cwd outside Git keeps the upstream project-scope convention useful for
// ordinary folders too.
func ProjectRoot(ctx context.Context, cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	if repo, err := gitx.Discover(ctx, cwd); err == nil && repo.Root != "" {
		return repo.Root
	}
	return cwd
}

// Managed reports whether name has an upstream lock entry in the requested
// scope. Direct-managed skills such as dev-cli and herdr must use their own
// update mechanism rather than being handed to `skills update`.
func Managed(ctx context.Context, cwd, name string, scope Scope) bool {
	root := ProjectRoot(ctx, cwd)
	filename := filepath.Join(root, "skills-lock.json")
	if scope == ScopeGlobal {
		home, _ := os.UserHomeDir()
		filename = filepath.Join(home, ".agents", ".skill-lock.json")
	}
	_, ok := findLockEntry(readLock(filename).Skills, name)
	return ok
}

// List returns a merged, stable inventory. It never downloads the provider;
// a missing uncached CLI is an actionable error rather than a network side
// effect hidden inside a read.
func List(ctx context.Context, cwd string, options ListOptions) ([]Skill, error) {
	root := ProjectRoot(ctx, cwd)
	p, err := readOnlyProvider()
	if err != nil {
		return nil, err
	}

	project, global := options.Project, options.Global
	if !project && !global {
		project, global = true, true
	}

	projectLock := readLock(filepath.Join(root, "skills-lock.json"))
	home, _ := os.UserHomeDir()
	globalLock := readLock(filepath.Join(home, ".agents", ".skill-lock.json"))

	var rows []Skill
	if project {
		got, err := listScope(ctx, p, root, ScopeProject, root, projectLock)
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}
	if global {
		got, err := listScope(ctx, p, root, ScopeGlobal, home, globalLock)
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope == ScopeProject
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	if options.Check {
		rows = CheckUpdates(ctx, rows)
	}
	return rows, nil
}

func listScope(ctx context.Context, p provider, cwd string, scope Scope, scopeRoot string, lock lockFile) ([]Skill, error) {
	args := []string{"list", "--json"}
	if scope == ScopeGlobal {
		args = append(args, "--global")
	}
	out, errOut, err := p.output(ctx, cwd, args...)
	if err != nil {
		detail := strings.TrimSpace(errOut)
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("list %s agent skills with %s: %s", scope, p.label, detail)
	}
	var upstream []upstreamSkill
	if err := json.Unmarshal(out, &upstream); err != nil {
		return nil, fmt.Errorf("decode %s output from %s: %w", scope, p.label, err)
	}

	rows := make([]Skill, 0, len(upstream))
	for _, raw := range upstream {
		row := Skill{
			Name: raw.Name, Scope: scope, ScopeRoot: scopeRoot,
			Path: raw.Path, Agents: append([]string(nil), raw.Agents...),
			Source: value(raw.Source), SourceURL: value(raw.SourceURL),
			SourceType: value(raw.SourceType), UpdateStatus: UpdateUnknown,
			ManagedBy: ManagedByExternal,
		}
		if !filepath.IsAbs(row.Path) {
			row.Path = filepath.Join(scopeRoot, row.Path)
		}
		if entry, ok := findLockEntry(lock.Skills, row.Name); ok {
			copy := entry
			row.lock = &copy
			row.ManagedBy = ManagedBySkills
			row.UpdateStatus = UpdateUnchecked
			if row.Source == "" {
				row.Source = entry.Source
			}
			if row.SourceURL == "" {
				row.SourceURL = entry.SourceURL
			}
			if row.SourceType == "" {
				row.SourceType = entry.SourceType
			}
		} else if row.Name == skill.Name {
			row.ManagedBy = ManagedByDev
			row.Source = "dev binary"
			row.UpdateStatus, row.UpdateDetail = bundledStatus(row.Path)
		} else {
			row.UpdateDetail = "not tracked by the skills CLI"
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func findLockEntry(entries map[string]lockEntry, name string) (lockEntry, bool) {
	if entry, ok := entries[name]; ok {
		return entry, true
	}
	want := normalizedName(name)
	for key, entry := range entries {
		if normalizedName(key) == want {
			return entry, true
		}
	}
	return lockEntry{}, false
}

func normalizedName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func readLock(filename string) lockFile {
	b, err := os.ReadFile(filename)
	if err != nil {
		return lockFile{Skills: map[string]lockEntry{}}
	}
	var lock lockFile
	if json.Unmarshal(b, &lock) != nil || lock.Skills == nil {
		return lockFile{Skills: map[string]lockEntry{}}
	}
	return lock
}

func bundledStatus(dir string) (UpdateStatus, string) {
	files, err := skill.Files()
	if err != nil {
		return UpdateFailed, err.Error()
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || !bytes.Equal(got, want) {
			return UpdateAvailable, "bundled skill differs; run `dev skill install`"
		}
	}
	return UpdateCurrent, "matches this dev binary"
}

// CheckUpdates compares lock-recorded content with the current Git source.
// It never invokes `skills update` because that command writes immediately.
func CheckUpdates(ctx context.Context, rows []Skill) []Skill {
	out := append([]Skill(nil), rows...)
	type member struct {
		index int
		url   string
		ref   string
	}
	groups := map[string][]member{}
	for i := range out {
		row := &out[i]
		if row.ManagedBy != ManagedBySkills || row.lock == nil {
			continue
		}
		url, ok := sourceURL(*row.lock)
		if !ok || row.lock.SkillPath == "" {
			row.UpdateStatus = UpdateUnknown
			row.UpdateDetail = "lock entry has no checkable Git source and skill path"
			continue
		}
		key := url + "\x00" + row.lock.Ref
		groups[key] = append(groups[key], member{index: i, url: url, ref: row.lock.Ref})
	}

	for _, members := range groups {
		dir, err := cloneSource(ctx, members[0].url, members[0].ref)
		if err != nil {
			for _, member := range members {
				out[member.index].UpdateStatus = UpdateFailed
				out[member.index].UpdateDetail = "could not fetch source"
			}
			continue
		}
		for _, member := range members {
			checkOne(ctx, dir, &out[member.index])
		}
		os.RemoveAll(filepath.Dir(dir))
	}
	return out
}

func sourceURL(entry lockEntry) (string, bool) {
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

func cloneSource(ctx context.Context, url, ref string) (string, error) {
	parent, err := os.MkdirTemp("", "dev-skill-check-*")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "source")
	if _, err := gitOutput(ctx, "", "clone", "--quiet", "--depth", "1", "--filter=blob:none", url, dir); err != nil {
		os.RemoveAll(parent)
		return "", err
	}
	if ref != "" {
		if _, err := gitOutput(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", ref); err != nil {
			os.RemoveAll(parent)
			return "", err
		}
		if _, err := gitOutput(ctx, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
			os.RemoveAll(parent)
			return "", err
		}
	}
	return dir, nil
}

func checkOne(ctx context.Context, repoDir string, row *Skill) {
	entry := row.lock
	folder, ok := safeSkillFolder(entry.SkillPath)
	if !ok {
		row.UpdateStatus = UpdateUnknown
		row.UpdateDetail = "invalid skill path in lock entry"
		return
	}
	abs := filepath.Join(repoDir, filepath.FromSlash(folder))
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		if os.IsNotExist(err) {
			row.UpdateStatus = UpdateMissing
			row.UpdateDetail = "skill path no longer exists upstream"
		} else {
			row.UpdateStatus = UpdateFailed
			row.UpdateDetail = "could not inspect upstream skill path"
		}
		return
	}

	expected := entry.ComputedHash
	actual := ""
	if row.Scope == ScopeGlobal {
		expected = entry.SkillFolderHash
		if len(expected) == 40 {
			spec := "HEAD^{tree}"
			if folder != "" {
				spec = "HEAD:" + folder
			}
			actual, _ = gitOutput(ctx, repoDir, "rev-parse", spec)
		} else if len(expected) == 64 {
			actual, _ = folderHash(abs)
		}
	} else if len(expected) == 64 {
		actual, _ = folderHash(abs)
	}
	if expected == "" || actual == "" {
		row.UpdateStatus = UpdateUnknown
		row.UpdateDetail = "lock entry has no comparable content hash"
		return
	}
	if strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected)) {
		row.UpdateStatus = UpdateCurrent
		row.UpdateDetail = "matches the recorded upstream content"
	} else {
		row.UpdateStatus = UpdateAvailable
		row.UpdateDetail = "upstream content changed"
	}
}

func safeSkillFolder(skillPath string) (string, bool) {
	p := strings.ReplaceAll(strings.TrimSpace(skillPath), "\\", "/")
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
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func folderHash(dir string) (string, error) {
	type file struct {
		name string
		body []byte
	}
	var files []file
	err := filepath.WalkDir(dir, func(filename string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filename != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			body, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, filename)
			if err != nil {
				return err
			}
			files = append(files, file{name: filepath.ToSlash(rel), body: body})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	h := sha256.New()
	for _, f := range files {
		_, _ = io.WriteString(h, f.name)
		_, _ = h.Write(f.body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readOnlyProvider() (provider, error) {
	if bin, err := exec.LookPath("skills"); err == nil {
		return provider{bin: bin, label: "skills"}, nil
	}
	if bin, err := exec.LookPath("npx"); err == nil {
		return provider{bin: bin, prefix: []string{"--no-install", "skills"}, label: "npx --no-install skills"}, nil
	}
	return provider{}, errors.New("skills provider unavailable — run `dev skill add` once or install the `skills` npm package")
}

func interactiveProvider() (provider, error) {
	if bin, err := exec.LookPath("skills"); err == nil {
		return provider{bin: bin, label: "skills"}, nil
	}
	if bin, err := exec.LookPath("npx"); err == nil {
		return provider{bin: bin, prefix: []string{"skills"}, label: "npx skills"}, nil
	}
	return provider{}, errors.New("neither `skills` nor `npx` is available")
}

func (p provider) command(ctx context.Context, cwd string, args ...string) *exec.Cmd {
	all := append(append([]string(nil), p.prefix...), args...)
	cmd := exec.CommandContext(ctx, p.bin, all...)
	cmd.Dir = cwd
	return cmd
}

func (p provider) output(ctx context.Context, cwd string, args ...string) ([]byte, string, error) {
	cmd := p.command(ctx, cwd, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

// AddCommand opens the upstream interactive installer in projectRoot.
func AddCommand(ctx context.Context, projectRoot, source string) (*exec.Cmd, error) {
	if source == "" {
		source = DefaultSource
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	return p.command(ctx, projectRoot, "add", source), nil
}

// InstallCommand installs an explicit set of skills for explicit agents in
// project scope. The caller owns the surrounding confirmation UI; --yes only
// suppresses the provider's duplicate prompt after that decision was made.
func InstallCommand(ctx context.Context, projectRoot, source string, names, agents []string) (*exec.Cmd, error) {
	if source == "" {
		source = DefaultSource
	}
	if len(names) == 0 {
		return nil, errors.New("at least one skill is required")
	}
	if len(agents) == 0 {
		return nil, errors.New("at least one agent is required")
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	args := []string{"add", source, "--skill"}
	args = append(args, names...)
	args = append(args, "--agent")
	args = append(args, agents...)
	args = append(args, "--yes")
	return p.command(ctx, ProjectRoot(ctx, projectRoot), args...), nil
}

// FindProject returns one installed project-scoped skill by name.
func FindProject(ctx context.Context, projectRoot, name string) (Skill, error) {
	rows, err := List(ctx, projectRoot, ListOptions{Project: true})
	if err != nil {
		return Skill{}, err
	}
	for _, row := range rows {
		if row.Scope == ScopeProject && row.Name == name {
			return row, nil
		}
	}
	return Skill{}, fmt.Errorf("project skill %q is not installed", name)
}

// UpdateCommand updates exactly one lock-managed skill in exactly one scope.
func UpdateCommand(ctx context.Context, projectRoot, name string, scope Scope) (*exec.Cmd, error) {
	if name == "" {
		return nil, errors.New("skill name is required")
	}
	if scope != ScopeProject && scope != ScopeGlobal {
		return nil, errors.New("skill update requires project or global scope")
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	args := []string{"update", name, "--yes"}
	if scope == ScopeGlobal {
		args = append(args, "--global")
	} else {
		args = append(args, "--project")
	}
	return p.command(ctx, projectRoot, args...), nil
}

// ProviderVersion checks the same no-download provider used for listing.
func ProviderVersion(ctx context.Context, cwd string) (string, error) {
	p, err := readOnlyProvider()
	if err != nil {
		return "", err
	}
	out, errOut, err := p.output(ctx, cwd, "--version")
	if err != nil {
		return "", fmt.Errorf("%s: %s", p.label, strings.TrimSpace(errOut))
	}
	return p.label + " " + strings.TrimSpace(string(out)), nil
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
