// Package dotfile observes chezmoi configuration without executing chezmoi or
// evaluating user templates. Native operations remain chezmoi's responsibility.
package dotfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tailscale/hujson"
	"go.yaml.in/yaml/v3"
)

const SchemaVersion = 1
const MaxStatusBytes int64 = 16 << 10
const maxConfigBytes int64 = 1 << 20

type Options struct {
	ConfigPath string
}

// Status deliberately separates configuration, source checkout and deployed
// files. A known Git revision never proves that a revision has been applied.
type Status struct {
	SchemaVersion   int             `json:"schema_version"`
	Platform        string          `json:"platform"`
	Installed       bool            `json:"installed"`
	ConfigState     string          `json:"config_state"`
	ConfigPath      string          `json:"config_path,omitempty"`
	SourceState     string          `json:"source_state"`
	SourceDir       string          `json:"source_dir,omitempty"`
	SourceStateDir  string          `json:"source_state_dir,omitempty"`
	WorkingTree     string          `json:"working_tree,omitempty"`
	GitState        string          `json:"git_state"`
	GitRevision     string          `json:"git_revision,omitempty"`
	GitBranch       string          `json:"git_branch,omitempty"`
	DeploymentDrift string          `json:"deployment_drift"`
	Reason          string          `json:"reason,omitempty"`
	Recommendation  *Recommendation `json:"recommendation,omitempty"`
}

type nativeConfig struct {
	SourceDir   string `toml:"sourceDir" json:"sourceDir" yaml:"sourceDir"`
	WorkingTree string `toml:"workingTree" json:"workingTree" yaml:"workingTree"`
}

// Observe performs bounded local reads only. It does not run chezmoi, inspect
// deployment content, contact Git remotes, refresh the index, or invoke hooks.
func Observe(ctx context.Context, options Options) Status {
	platform := DetectPlatform()
	s := Status{SchemaVersion: SchemaVersion, Platform: platform, ConfigState: "absent", SourceState: "unknown", GitState: "unknown", DeploymentDrift: "unknown", Recommendation: Recommended(platform)}
	_, err := exec.LookPath("chezmoi")
	s.Installed = err == nil
	configPath, reason := findConfig(options.ConfigPath)
	if reason != "" {
		s.ConfigState, s.Reason = "unknown", reason
		return s
	}
	var saved nativeConfig
	if configPath != "" {
		s.ConfigPath = configPath
		data, err := readBounded(configPath, maxConfigBytes)
		if err != nil {
			s.ConfigState, s.Reason = "unknown", "config-unreadable"
			return s
		}
		if err := decodeConfig(configPath, data, &saved); err != nil {
			s.ConfigState, s.Reason = "unknown", "config-invalid"
			return s
		}
		s.ConfigState = "present"
	}
	base := saved.SourceDir
	if base == "" {
		s.SourceDir, err = findDefaultSource()
	} else {
		s.SourceDir, err = staticPath(base)
	}
	if err != nil {
		s.Reason = "source-unresolved"
		return s
	}
	s.SourceStateDir = s.SourceDir
	s.WorkingTree = s.SourceDir
	if saved.WorkingTree != "" {
		s.WorkingTree, err = staticPath(saved.WorkingTree)
		if err != nil {
			s.Reason = "working-tree-unresolved"
			return s
		}
	}
	info, err := os.Stat(s.SourceDir)
	if errors.Is(err, os.ErrNotExist) {
		// A dangling source symlink is existing setup evidence, not permission
		// to initialize a repository through that link.
		if _, linkErr := os.Lstat(s.SourceDir); !errors.Is(linkErr, os.ErrNotExist) {
			s.Reason = "source-unreadable"
			return s
		}
		s.SourceState = "absent"
		s.GitState = "absent"
		return s
	}
	if err != nil || !info.IsDir() {
		s.Reason = "source-unreadable"
		return s
	}
	root, err := readBounded(filepath.Join(s.SourceDir, ".chezmoiroot"), 4096)
	if err == nil {
		rel := strings.TrimSpace(string(root))
		if rel == "" || strings.ContainsAny(rel, "\x00\r\n$") || strings.Contains(rel, "{{") || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(filepath.Clean(rel), ".."+string(filepath.Separator)) {
			s.SourceStateDir, s.Reason = "", "source-root-unresolved"
			return s
		}
		s.SourceStateDir = filepath.Join(s.SourceDir, rel)
		resolvedBase, baseErr := filepath.EvalSymlinks(s.SourceDir)
		resolvedState, stateErr := filepath.EvalSymlinks(s.SourceStateDir)
		if baseErr != nil || stateErr != nil || !within(resolvedBase, resolvedState) {
			s.Reason = "source-root-unresolved"
			return s
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.Reason = "source-root-unreadable"
		return s
	}
	if info, err := os.Stat(s.SourceStateDir); err != nil || !info.IsDir() {
		s.Reason = "source-root-unreadable"
		return s
	}
	s.SourceState = "present"
	observeGit(ctx, &s)
	return s
}

func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func findConfig(explicit string) (string, string) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) && !strings.HasPrefix(explicit, "~") {
			var err error
			explicit, err = filepath.Abs(explicit)
			if err != nil {
				return "", "config-path-unresolved"
			}
		}
		path, err := staticPath(explicit)
		if err != nil {
			return "", "config-path-unresolved"
		}
		if _, err := os.Stat(path); err != nil {
			return "", "config-unreadable"
		}
		return path, ""
	}
	directories, err := xdgSearchDirectories("XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", ".config", []string{filepath.Join("/", "etc", "xdg")})
	if err != nil {
		return "", "config-path-unresolved"
	}
	for _, directory := range directories {
		var found []string
		for _, extension := range []string{"toml", "yaml", "json", "jsonc"} {
			path := filepath.Join(directory, "chezmoi", "chezmoi."+extension)
			// Native discovery matches directory entry names, including broken
			// links. The subsequent bounded read reports unreadable config.
			if _, err := os.Lstat(path); err == nil {
				found = append(found, path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", "config-unreadable"
			}
		}
		if len(found) > 1 {
			return "", "config-ambiguous"
		}
		if len(found) == 1 {
			return found[0], ""
		}
	}
	return "", ""
}

// chezmoi searches the user home first and then every XDG search directory,
// taking the first source or directory containing a supported config format.
// See chezmoi internal/cmd/config.go defaultConfigFile/defaultSourceDir and
// twpayne/go-xdg's BaseDirectorySpecification. CHEZMOI_* variables are native
// command exports, not configuration/source overrides.
func xdgSearchDirectories(homeVariable, directoriesVariable, fallbackHome string, fallbackDirectories []string) ([]string, error) {
	first := os.Getenv(homeVariable)
	if first == "" {
		first = filepath.Join(home(), filepath.FromSlash(fallbackHome))
	}
	remaining := os.Getenv(directoriesVariable)
	if remaining == "" {
		remaining = strings.Join(fallbackDirectories, string(os.PathListSeparator))
	}
	values := append([]string{first}, filepath.SplitList(remaining)...)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid XDG path")
		}
		// Native NewAbsPathFromExtPath cleans, expands ~, then makes relative
		// paths absolute. This also resolves Windows' default rooted paths.
		value = filepath.Clean(value)
		if value == "~" {
			value = home()
		} else if strings.HasPrefix(value, "~/") || runtime.GOOS == "windows" && strings.HasPrefix(value, `~\`) {
			value = filepath.Join(home(), value[2:])
		}
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, err
		}
		result = append(result, absolute)
	}
	return result, nil
}

func findDefaultSource() (string, error) {
	directories, err := xdgSearchDirectories("XDG_DATA_HOME", "XDG_DATA_DIRS", ".local/share", []string{filepath.Join("/", "usr", "local", "share"), filepath.Join("/", "usr", "share")})
	if err != nil {
		return "", err
	}
	for _, directory := range directories {
		candidate := filepath.Join(directory, "chezmoi")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return filepath.Join(directories[0], "chezmoi"), nil
}

func decodeConfig(path string, data []byte, target *nativeConfig) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		_, err := toml.Decode(string(data), target)
		return err
	case ".yaml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(target); err != nil {
			return err
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("multiple YAML documents")
		}
		return nil
	case ".jsonc":
		standard, err := hujson.Standardize(data)
		if err != nil {
			return err
		}
		return json.Unmarshal(standard, target)
	case ".json":
		return json.Unmarshal(data, target)
	default:
		return errors.New("unsupported configuration format")
	}
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return data, err
}

func staticPath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n$") || strings.Contains(value, "{{") {
		return "", errors.New("path requires evaluation")
	}
	if value == "~" {
		value = home()
	} else if strings.HasPrefix(value, "~/") || runtime.GOOS == "windows" && strings.HasPrefix(value, `~\`) {
		value = filepath.Join(home(), value[2:])
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("path is not absolute")
	}
	return filepath.Clean(value), nil
}

func home() string {
	value, _ := os.UserHomeDir()
	return value
}

// Only revision queries are used here: git status can execute fsmonitor.
func observeGit(ctx context.Context, s *Status) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		full := append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", s.WorkingTree}, args...)
		cmd := exec.CommandContext(ctx, "git", full...)
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(key, "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
		var out limitedBuffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil || out.exceeded {
			return "", errors.New("Git metadata unavailable")
		}
		return strings.TrimSpace(out.String()), nil
	}
	root, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		s.GitState = "unavailable"
		return
	}
	s.WorkingTree = root
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		s.GitState = "unborn"
		return
	}
	s.GitState, s.GitRevision = "present", head
	if branch, err := run("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		s.GitBranch = branch
	}
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := 4096 - b.Len()
	if len(data) > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}
