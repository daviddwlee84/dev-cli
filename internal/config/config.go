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
	Paths     Paths     `toml:"paths"`
	Runtime   Runtime   `toml:"runtime"`
	Worktree  Worktree  `toml:"worktree"`
	Stats     Stats     `toml:"stats"`
	TUI       TUI       `toml:"tui"`
	Bootstrap Bootstrap `toml:"bootstrap"`
	Forge     Forge     `toml:"forge"`

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
	// Strategy is how a new worktree gets its installed dependencies:
	// "reinstall" (correct, the default), "copy", "link" or "skip". dev
	// narrows an unsound choice back to reinstall and says why — copying a
	// virtualenv, for instance, cannot work, because it bakes its own
	// absolute path into pyvenv.cfg.
	Strategy string `toml:"strategy"`
	// Strategies overrides Strategy per ecosystem ("python", "node", "go",
	// "rust", "ruby", "elixir").
	Strategies map[string]string `toml:"strategies"`
	// ProvisionTimeout caps a single post-create command.
	ProvisionTimeout Duration `toml:"provision_timeout"`
}

// Forge configures the combined GitHub/GitLab remote inventory.
type Forge struct {
	// RemoteLimit is requested from each provider. GitHub caps it at 100.
	RemoteLimit int `toml:"remote_limit"`
	// CacheTTL makes the REMOTE TUI view instant after its first load; r always
	// refreshes explicitly.
	CacheTTL Duration `toml:"cache_ttl"`
}

// Bootstrap configures recursive discovery and the optional symlink index.
type Bootstrap struct {
	// MaxDepth bounds recursive scans. Zero means unlimited. The default of 8
	// reaches ghq's host/owner/repo and deeply categorised layouts without
	// wandering through a mounted filesystem forever.
	MaxDepth int `toml:"max_depth"`
	// FollowSymlinks follows symlinked container directories with realpath
	// cycle detection. Direct symlinks to repositories are always identified.
	FollowSymlinks bool `toml:"follow_symlinks"`
	// IndexRoot is the optional, non-destructive symlink catalog. Empty means
	// no catalog until --index is passed.
	IndexRoot string `toml:"index_root"`
	// Layout is "flat" (<index>/<repo>) or "preserve" (mirror the path under
	// its scan root).
	Layout string `toml:"layout"`
	// RelativeLinks makes an index portable when it and the repositories move
	// together; absolute links are less surprising across mount points.
	RelativeLinks bool `toml:"relative_links"`
}

// TUI configures the interactive dashboard.
type TUI struct {
	// Repos configures the local repository table.
	Repos RepoTable `toml:"repos"`
	// Tools are the external programs the dashboard can hand the terminal to,
	// each on its own key. When empty, DefaultTools applies.
	//
	// Configurable rather than fixed because which program you reach for is
	// personal — nvim or helix, lazygit or gitui, and whatever aliases and
	// scripts you have built up around your own workflow.
	Tools []Tool `toml:"tools"`
}

// RepoTable configures columns and ordering in the TUI REPOS view.
type RepoTable struct {
	// Columns may contain repo, branch, git, remote, size, live, latest,
	// worktrees, tasks, category, or path, in the exact display order wanted.
	Columns []string `toml:"columns"`
	// Sort is activity, latest, name, git, or tasks.
	Sort string `toml:"sort"`
	// Reverse flips the selected order.
	Reverse bool `toml:"reverse"`
}

// DefaultRepoColumns is the useful full local inventory without paths (detail
// shows the selected path).
func DefaultRepoColumns() []string {
	return []string{"repo", "branch", "git", "size", "live", "latest", "worktrees", "tasks"}
}

// EffectiveRepoColumns returns configured columns or the defaults.
func (c Config) EffectiveRepoColumns() []string {
	if len(c.TUI.Repos.Columns) == 0 {
		return DefaultRepoColumns()
	}
	return c.TUI.Repos.Columns
}

// EffectiveRepoSort returns the configured ordering or activity-first.
func (c Config) EffectiveRepoSort() string {
	if c.TUI.Repos.Sort == "" {
		return "activity"
	}
	return c.TUI.Repos.Sort
}

// Tool is one external program bound to a key in the dashboard.
type Tool struct {
	// Key launches it. A single character, and not one of the dashboard's own
	// bindings — dev reports a clash rather than silently shadowing.
	Key string `toml:"key"`
	// Name is shown in the footer.
	Name string `toml:"name"`
	// Run is a shell command, executed in the selected row's checkout.
	Run string `toml:"run"`
	// Interactive runs through $SHELL -lic, loading the user's rc file so a
	// shell alias or function can be used. Off by default: startup files can
	// print output and alter environment in ways an ordinary command should
	// not inherit accidentally.
	Interactive bool `toml:"interactive"`
}

// reservedKeys are the dashboard's own bindings. A tool cannot take one,
// because losing the ability to quit or move is not a trade anyone wants.
var reservedKeys = map[string]string{
	"q": "quit", "j": "down", "k": "up", "g": "top", "G": "bottom",
	"h": "previous view", "l": "next view", "tab": "next view",
	"/": "filter", "r": "refresh", "o": "open", "p": "park",
	"c": "edit next action", "s": "start a worktree task", "d": "start a direct task",
	"a": "include hidden history", "n": "new Try", " ": "context actions",
	"0": "clear filters", "1": "hot", "2": "warm", "3": "cold",
	"?": "help", "H": "repo activity heatmap", "e": "edit config",
	"O": "cycle repo sort", "R": "reverse repo sort",
}

// ReservedKey reports the dashboard binding a key would collide with.
func ReservedKey(key string) (string, bool) {
	name, ok := reservedKeys[key]
	return name, ok
}

// DefaultTools are the programs bound when nothing is configured. They are
// written out in full by `dev config init` rather than left implicit, so what
// is bound is visible rather than something to discover by pressing keys.
func DefaultTools() []Tool {
	return []Tool{
		{Key: "L", Name: "lazygit", Run: "lazygit"},
		{Key: "Y", Name: "yazi", Run: "yazi"},
		{Key: "E", Name: "editor", Run: "$EDITOR ."},
		{Key: "S", Name: "shell", Run: "$SHELL"},
	}
}

// EffectiveTools returns the configured tools, or the defaults when none are
// configured. A configured list replaces the defaults entirely — a partial
// merge would make it unclear which bindings are in effect.
func (c Config) EffectiveTools() []Tool {
	if len(c.TUI.Tools) == 0 {
		return DefaultTools()
	}
	return c.TUI.Tools
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
			Strategy:         "reinstall",
			ProvisionTimeout: Duration{10 * time.Minute},
		},
		Bootstrap: Bootstrap{
			MaxDepth:       8,
			FollowSymlinks: true,
			Layout:         "flat",
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
	switch c.Bootstrap.Layout {
	case "", "flat", "preserve":
	default:
		return fmt.Errorf("bootstrap.layout %q: want flat or preserve", c.Bootstrap.Layout)
	}
	if c.Bootstrap.MaxDepth < 0 {
		return fmt.Errorf("bootstrap.max_depth must be zero (unlimited) or positive")
	}
	if c.Forge.RemoteLimit < 0 {
		return fmt.Errorf("forge.remote_limit must be zero (use default) or positive")
	}
	validStrategy := func(v string) bool {
		switch v {
		case "", "reinstall", "copy", "link", "skip":
			return true
		}
		return false
	}
	if !validStrategy(c.Worktree.Strategy) {
		return fmt.Errorf("worktree.strategy %q: want reinstall, copy, link or skip", c.Worktree.Strategy)
	}
	for ecosystem, strategy := range c.Worktree.Strategies {
		if !validStrategy(strategy) {
			return fmt.Errorf("worktree.strategies.%s %q: want reinstall, copy, link or skip",
				ecosystem, strategy)
		}
	}
	validColumns := map[string]bool{
		"repo": true, "branch": true, "git": true, "remote": true, "size": true, "live": true,
		"latest": true, "worktrees": true, "tasks": true,
		"category": true, "path": true,
	}
	for i, column := range c.TUI.Repos.Columns {
		if !validColumns[column] {
			return fmt.Errorf("tui.repos.columns[%d] %q: unknown column", i, column)
		}
	}
	switch c.EffectiveRepoSort() {
	case "activity", "latest", "name", "git", "size", "tasks":
	default:
		return fmt.Errorf("tui.repos.sort %q: want activity, latest, name, git, size or tasks", c.TUI.Repos.Sort)
	}
	seen := map[string]string{}
	for i, t := range c.TUI.Tools {
		switch {
		case t.Key == "":
			return fmt.Errorf("tui.tools[%d]: key is required", i)
		case t.Run == "":
			return fmt.Errorf("tui.tools[%d] (%s): run is required", i, t.Key)
		}
		if binding, ok := ReservedKey(t.Key); ok {
			return fmt.Errorf("tui.tools[%d]: key %q is the dashboard's %q binding; pick another",
				i, t.Key, binding)
		}
		if prev, ok := seen[t.Key]; ok {
			return fmt.Errorf("tui.tools[%d]: key %q is already bound to %q", i, t.Key, prev)
		}
		seen[t.Key] = t.Name
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

// StateDir is the expanded directory holding tasks/, assets/ and stats.db.
func (c Config) StateDir() string { return Expand(c.Paths.StateDir) }

// TasksDir holds one TOML file per task.
func (c Config) TasksDir() string { return filepath.Join(c.StateDir(), "tasks") }

// AssetsDir holds one TOML file per catalog asset.
func (c Config) AssetsDir() string { return filepath.Join(c.StateDir(), "assets") }

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
