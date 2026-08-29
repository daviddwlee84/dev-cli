// Package cli assembles dev's command tree. Commands stay thin: they parse
// flags, call into the domain packages, and render. All policy lives in
// config, catalog, experiment, diskusage, gitx, wt, task and runtime so it can
// be tested without a terminal.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// App is the state shared by every command.
type App struct {
	Cfg      config.Config
	Tasks    *task.Store
	Catalog  *catalog.Store
	Registry *catalog.Registry
	Notes    *note.Service
	Sizes    *diskusage.Manager

	In  io.Reader
	Out io.Writer
	Err io.Writer

	// configPath is the --config override; empty means the XDG default.
	configPath string
	// remotesPath is the --remotes override; empty means dev/remotes.toml.
	remotesPath string
	// runtimeOverride is the --runtime flag; empty means use the config.
	runtimeOverride string
	// noRuntime disables all multiplexer interaction for one invocation.
	noRuntime bool
	// allowSharedCheckout is an explicit escape hatch for coordinated agents
	// whose file ownership is known to be disjoint.
	allowSharedCheckout bool
	// colorMode controls styling of human-readable output.
	colorMode string
	// interactiveCheck is a test seam for commands that prompt only when a real
	// terminal is attached. Production falls back to interactive().
	interactiveCheck func() bool
	// runtimeInstance and runtimesByName are injection seams used by focused
	// command tests.
	runtimeInstance runtime.Runtime
	runtimesByName  map[string]runtime.Runtime
}

// Load reads configuration and prepares the shared stores.
func (a *App) Load() error {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return err
	}
	a.Cfg = cfg
	if a.In == nil {
		a.In = os.Stdin
	}
	if a.Out == nil {
		a.Out = os.Stdout
	}
	if a.Err == nil {
		a.Err = os.Stderr
	}
	a.Tasks = task.NewStore(cfg.TasksDir())
	a.Catalog = catalog.NewStore(cfg.AssetsDir(), catalog.WithDiagnosticSink(func(diagnostic catalog.Diagnostic) {
		fmt.Fprintf(a.Err, "%s %s\n", a.errStyle().warning("dev: warning:"), diagnostic.Error())
	}))
	a.Registry = catalog.NewRegistry(a.Catalog)
	noteStore := note.NewStore(cfg.NotesDir(), note.WithDiagnosticSink(func(path string, err error) {
		fmt.Fprintf(a.Err, "dev: warning: skipping note %s: %v\n", filepath.Base(path), err)
	}))
	a.Notes = note.NewService(noteStore, cfg.NotesIndexFile())
	a.Notes.IndexDiagnostic = func(err error) {
		fmt.Fprintf(a.Err, "dev: warning: note search index: %v\n", err)
	}
	if a.Sizes == nil {
		cache := diskusage.NewCache(filepath.Join(config.CacheHome(), "dev", "sizes-v1.json"), 10*time.Minute)
		a.Sizes = diskusage.NewManager(cache, 2)
	}
	return nil
}

func (a *App) interactive() bool {
	if a.interactiveCheck != nil {
		return a.interactiveCheck()
	}
	return interactive()
}

// Runtime resolves the multiplexer backend for this invocation, honouring
// --runtime and --no-runtime.
func (a *App) Runtime() runtime.Runtime {
	if a.runtimeInstance != nil {
		return a.runtimeInstance
	}
	if a.noRuntime {
		return runtime.None{}
	}
	backend := a.Cfg.Runtime.Backend
	if a.runtimeOverride != "" {
		backend = a.runtimeOverride
	}
	return a.runtimeNamed(backend)
}

// runtimeNamed resolves the backend that owns a persisted opaque handle. It
// deliberately bypasses the current backend override: feeding a Herdr handle to
// tmux after a config change is never valid.
func (a *App) runtimeNamed(name string) runtime.Runtime {
	if rt := a.runtimesByName[name]; rt != nil {
		return rt
	}
	if a.runtimeInstance != nil && (name == "" || a.runtimeInstance.Name() == name) {
		return a.runtimeInstance
	}
	rt := runtime.Select(name)
	if h, ok := rt.(*runtime.Herdr); ok {
		return h.WithMetadataSource(a.Cfg.Runtime.MetadataSource)
	}
	return rt
}

// printf writes to the command's stdout.
func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.Out, format, args...)
}

// warnf writes a non-fatal notice to stderr, so it never pollutes piped output.
func (a *App) warnf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	fmt.Fprintf(a.Err, "%s %s\n", a.errStyle().warning("dev:"), message)
}

// cdDirective asks the shell wrapper to move its parent process. The wrapper
// gives dev a child-only file descriptor so normal output stays connected to
// the terminal and an inherited environment variable can never name a file to
// overwrite. NUL terminates the path because filesystem paths may contain
// newlines. Without the wrapper, retain the printable directive used by older
// integrations.
func (a *App) cdDirective(dir string) error {
	// Windows shells do not inherit an extra descriptor the way the POSIX
	// wrapper's `3>file` redirection does, so the PowerShell wrapper names a
	// temp file instead and reads it back after dev exits.
	if path := os.Getenv("DEV_SHELL_CD_FILE"); path != "" {
		return os.WriteFile(path, append([]byte(dir), 0), 0o600)
	}
	if rawFD := os.Getenv("DEV_SHELL_CD_FD"); rawFD != "" {
		fd, err := strconv.Atoi(rawFD)
		if err != nil || fd < 3 {
			return fmt.Errorf("invalid DEV_SHELL_CD_FD %q", rawFD)
		}
		file := os.NewFile(uintptr(fd), "dev-shell-cd")
		if file == nil {
			return fmt.Errorf("invalid shell directory descriptor %d", fd)
		}
		if _, err := file.Write(append([]byte(dir), 0)); err != nil {
			return fmt.Errorf("write shell directory directive: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close shell directory directive: %w", err)
		}
		return nil
	}
	_, err := fmt.Fprintf(a.Out, "cd %s\n", shellQuote(dir))
	return err
}

// ctx is the cancellable context commands run under.
func ctxOf() context.Context { return context.Background() }
