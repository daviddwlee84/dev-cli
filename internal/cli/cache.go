package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

func newCacheCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clear regenerable dev caches",
		Long: `Manage files under $XDG_CACHE_HOME/dev.

These are disposable accelerators: the remote forge inventory, logical disk-size
measurements and fetched GitHub gitignore templates. Activity statistics are deliberately not here —
$XDG_DATA_HOME/dev/stats.db contains observations that may not be
reconstructible; use "dev stats path/clear" for it.`,
	}
	cmd.AddCommand(newCacheListCmd(app), newCachePathCmd(app), newCacheClearCmd(app))
	return cmd
}

func cacheRoot() string { return filepath.Join(config.CacheHome(), "dev") }

func newCachePathCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print dev's XDG cache directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(app.Out, cacheRoot())
			return nil
		},
	}
}

func newCacheListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cache paths, sizes, and ages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t := NewTable("CACHE", "SIZE", "AGE", "PATH")
			for _, item := range cacheItems() {
				size, modified, exists := cacheInfo(item.path)
				if !exists {
					t.Add(item.name, "—", "—", config.Contract(item.path)+" (absent)")
					continue
				}
				t.Add(item.name, humanBytes(size), humanAge(time.Since(modified)), config.Contract(item.path))
			}
			t.Render(app.Out)
			fmt.Fprintf(app.Out, "\nStats data: %s (not cache; use `dev stats clear`)\n",
				config.Contract(filepath.Join(app.Cfg.StateDir(), "stats.db")))
			return nil
		},
	}
}

type cacheItem struct{ name, path string }

func cacheItems() []cacheItem {
	return []cacheItem{
		{"remote", filepath.Join(cacheRoot(), "remotes.json")},
		{"size", filepath.Join(cacheRoot(), "sizes-v1.json")},
		{"gitignore", filepath.Join(cacheRoot(), "gitignore")},
	}
}

func newCacheClearCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:       "clear <remote|size|gitignore|all>",
		Short:     "Remove a regenerable cache",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"remote", "size", "gitignore", "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "remotes" {
				name = "remote"
			}
			if name == "sizes" {
				name = "size"
			}
			var targets []cacheItem
			for _, item := range cacheItems() {
				if name == "all" || item.name == name {
					targets = append(targets, item)
				}
			}
			if len(targets) == 0 {
				return fmt.Errorf("unknown cache %q: want remote, size, gitignore or all", args[0])
			}
			removed := 0
			for _, item := range targets {
				if _, err := os.Lstat(item.path); os.IsNotExist(err) {
					continue
				}
				if err := os.RemoveAll(item.path); err != nil {
					return fmt.Errorf("clear %s: %w", item.name, err)
				}
				removed++
			}
			fmt.Fprintf(app.Out, "cleared %d cache(s) under %s\n", removed, config.Contract(cacheRoot()))
			return nil
		},
	}
}

func cacheInfo(path string) (size int64, modified time.Time, exists bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}, false
	}
	exists, modified = true, info.ModTime()
	if !info.IsDir() {
		return info.Size(), modified, true
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if i, err := d.Info(); err == nil {
			size += i.Size()
			if i.ModTime().After(modified) {
				modified = i.ModTime()
			}
		}
		return nil
	})
	return size, modified, true
}

func humanBytes(n int64) string {
	const unit = 1024
	switch {
	case n < unit:
		return fmt.Sprintf("%dB", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1fK", float64(n)/unit)
	case n < unit*unit*unit:
		return fmt.Sprintf("%.1fM", float64(n)/(unit*unit))
	default:
		return fmt.Sprintf("%.1fG", float64(n)/(unit*unit*unit))
	}
}
