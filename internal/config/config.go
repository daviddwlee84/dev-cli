package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the whole of dev's configuration. Zero values are never used
// directly — Default() supplies the baseline and Load() overlays the user's
// config.toml on top of it.
type Config struct {
	Paths    Paths    `toml:"paths"`
	Runtime  Runtime  `toml:"runtime"`
	Worktree Worktree `toml:"worktree"`
	Stats    Stats    `toml:"stats"`

	// Source records where the config was loaded from; "" means defaults only.
	Source string `toml:"-"`
}

// Paths tells dev where repos live and where derived state goes. Every entry
// is a template-or-path expanded through Expand, so "~", "$VAR" and absolute
// paths on another volume (e.g. /mnt/fast/worktrees) all work.
type Paths struct {
	ScanRoots    []string `toml:"scan_roots"`
	TriesRoot    string   `toml:"tries_root"`
	WorktreeRoot string   `toml:"worktree_root"`
	WorktreePath string   `toml:"worktree_path"`
	StateDir     string   `toml:"state_dir"`
	// ProjectRoot is where `dev graduate` promotes a try to, joined with the
	// chosen category.
	ProjectRoot string `toml:"project_root"`
}

// Runtime selects the terminal-multiplexer backend used to open a checkout.
type Runtime struct {
	// Backend is "auto" (herdr, then tmux, then none), or one of those names.
	Backend string `toml:"backend"`
	// MetadataSource is the --source id used for herdr workspace metadata.
	MetadataSource string `toml:"metadata_source"`
}

// Worktree controls where linked worktrees land and how they get provisioned
// into a usable state (deps installed, gitignored env files carried over).
type Worktree struct {
	// Include lists .gitignore-syntax patterns; matching files are copied into
	// a new worktree only when they are actually gitignored in the source
	// checkout. Tracked files are already in the checkout.
	Include []string `toml:"include"`
	// Link names directories symlinked rather than copied (opt-in: sharing
	// node_modules across checkouts breaks native builds often enough that it
	// must never be a default).
	Link []string `toml:"link"`
	// PostCreate is either the string "auto" (detect from lockfiles) or an
	// explicit list of shell commands run in the new worktree.
	PostCreate PostCreate `toml:"post_create"`
	// ProvisionTimeout caps a single post-create command.
	ProvisionTimeout Duration `toml:"provision_timeout"`
}

// Stats configures activity collection for the heatmap.
type Stats struct {
	// Sampler enables recording live agent/runtime activity into stats.db.
	Sampler bool `toml:"sampler"`
	// WakaTime enables the WakaTime importer as an additional source.
	WakaTime bool `toml:"wakatime"`
	// WakaTimeConfig points at the ini file holding the API key.
	WakaTimeConfig string `toml:"wakatime_config"`
}

// Default returns the built-in configuration: what dev does with no config.toml
// at all. It matches the layout documented in the README.
func Default() Config {
	return Config{
		Paths: Paths{
			ScanRoots:    []string{"~/Documents/Program", "~/src/tries"},
			TriesRoot:    "~/src/tries",
			WorktreeRoot: "~/Worktrees",
			WorktreePath: "{{worktree_root}}/{{repo}}/{{branch|slug}}",
			StateDir:     filepath.Join(DataHome(), "dev"),
			ProjectRoot:  "~/Documents/Program",
		},
		Runtime: Runtime{
			Backend:        "auto",
			MetadataSource: "dev",
		},
		Worktree: Worktree{
			Include:          []string{".env", ".env.local"},
			Link:             nil,
			PostCreate:       PostCreate{Auto: true},
			ProvisionTimeout: Duration{10 * time.Minute},
		},
		Stats: Stats{
			Sampler:        true,
			WakaTime:       false,
			WakaTimeConfig: "~/.wakatime.cfg",
		},
	}
}

// Load reads config.toml over the defaults. A missing file is not an error —
// dev must be useful with zero setup. A malformed file is, because silently
// falling back to defaults would send worktrees to the wrong volume.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = ConfigFile()
	}
	md, err := toml.DecodeFile(path, &cfg)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, nil
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		// Unknown keys are usually typos ("worktree_dir" vs "worktree_root").
		// Warn rather than fail so a newer config still works on an older dev.
		fmt.Fprintf(os.Stderr, "dev: warning: unknown key(s) in %s: %v\n", path, undecoded)
	}
	cfg.Source = path
	return cfg, cfg.Validate()
}

// Validate catches the settings that would otherwise fail deep inside a
// worktree create, when a directory has already been made.
func (c Config) Validate() error {
	switch c.Runtime.Backend {
	case "", "auto", "herdr", "tmux", "none":
	default:
		return fmt.Errorf("runtime.backend %q: want auto, herdr, tmux or none", c.Runtime.Backend)
	}
	if c.Paths.WorktreePath == "" {
		return errors.New("paths.worktree_path must not be empty")
	}
	if _, err := Render(c.Paths.WorktreePath, c.probeVars()); err != nil {
		return fmt.Errorf("paths.worktree_path: %w", err)
	}
	return nil
}

// probeVars is a dummy environment used to validate a template without having
// a real repo in hand.
func (c Config) probeVars() Vars {
	return Vars{
		"worktree_root": Expand(c.Paths.WorktreeRoot),
		"repo":          "repo",
		"repo_path":     "/repo",
		"branch":        "branch",
		"category":      "category",
		"host":          "host",
		"date":          "2006-01-02",
	}
}

// StateDir is the expanded directory holding tasks/, stats.db and cache/.
func (c Config) StateDir() string { return Expand(c.Paths.StateDir) }

// TasksDir holds one TOML file per task.
func (c Config) TasksDir() string { return filepath.Join(c.StateDir(), "tasks") }

// ScanRoots returns the expanded repo discovery roots.
func (c Config) ScanRoots() []string {
	out := make([]string, 0, len(c.Paths.ScanRoots))
	for _, r := range c.Paths.ScanRoots {
		if e := Expand(r); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// WorktreePathFor renders the configured template for one repo+branch pair.
func (c Config) WorktreePathFor(repoName, repoPath, branch, category string) (string, error) {
	v := Vars{
		"worktree_root": Expand(c.Paths.WorktreeRoot),
		"repo":          repoName,
		"repo_path":     repoPath,
		"branch":        branch,
		"category":      category,
		"host":          Hostname(),
		"date":          time.Now().Format("2006-01-02"),
	}
	rendered, err := Render(c.Paths.WorktreePath, v)
	if err != nil {
		return "", err
	}
	return Expand(rendered), nil
}

// Hostname is the machine identity recorded as a task's owner.
func Hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}
