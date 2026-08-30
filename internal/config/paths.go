package config

import (
	"os"
	"path/filepath"
	"strings"
)

// XDG base directories, resolved the way the spec prescribes: honor the
// environment variable when it holds an absolute path, otherwise fall back to
// the documented default under $HOME.

func xdgDir(envVar, fallback string) string {
	if v := os.Getenv(envVar); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(home(), fallback)
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// ConfigHome is $XDG_CONFIG_HOME (default ~/.config).
func ConfigHome() string { return xdgDir("XDG_CONFIG_HOME", ".config") }

// DataHome is $XDG_DATA_HOME (default ~/.local/share).
func DataHome() string { return xdgDir("XDG_DATA_HOME", ".local/share") }

// CacheHome is $XDG_CACHE_HOME (default ~/.cache).
func CacheHome() string { return xdgDir("XDG_CACHE_HOME", ".cache") }

// ConfigFile is the path dev loads its TOML configuration from.
func ConfigFile() string { return filepath.Join(ConfigHome(), "dev", "config.toml") }

// ScaffoldsFile is the optional repository-bootstrap preset catalog.
func ScaffoldsFile() string { return filepath.Join(ConfigHome(), "dev", "scaffolds.toml") }

// Expand resolves a leading ~ and any environment variables in p, then makes
// the result absolute. Paths in config.toml are written by humans, so both
// "~/Worktrees" and "$WORK/trees" have to work.
func Expand(p string) string {
	if p == "" {
		return ""
	}
	p = os.ExpandEnv(p)
	switch {
	case p == "~":
		p = home()
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home(), p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// Contract renders p with $HOME collapsed back to ~, for display only.
func Contract(p string) string {
	h := home()
	if h != "" && p == h {
		return "~"
	}
	if h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}
