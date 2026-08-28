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
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// App is the state shared by every command.
type App struct {
	Cfg      config.Config
	Tasks    *task.Store
	Catalog  *catalog.Store
	Registry *catalog.Registry
	Sizes    *diskusage.Manager

	Out io.Writer
	Err io.Writer

	// configPath is the --config override; empty means the XDG default.
	configPath string
	// runtimeOverride is the --runtime flag; empty means use the config.
	runtimeOverride string
	// noRuntime disables all multiplexer interaction for one invocation.
	noRuntime bool
	// allowSharedCheckout is an explicit escape hatch for coordinated agents
	// whose file ownership is known to be disjoint.
	allowSharedCheckout bool
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
	if a.Out == nil {
		a.Out = os.Stdout
	}
	if a.Err == nil {
		a.Err = os.Stderr
	}
	a.Tasks = task.NewStore(cfg.TasksDir())
	a.Catalog = catalog.NewStore(cfg.AssetsDir(), catalog.WithDiagnosticSink(func(diagnostic catalog.Diagnostic) {
		fmt.Fprintf(a.Err, "dev: warning: %s\n", diagnostic.Error())
	}))
	a.Registry = catalog.NewRegistry(a.Catalog)
	if a.Sizes == nil {
		cache := diskusage.NewCache(filepath.Join(config.CacheHome(), "dev", "sizes-v1.json"), 10*time.Minute)
		a.Sizes = diskusage.NewManager(cache, 2)
	}
	return nil
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
	fmt.Fprintf(a.Err, "dev: "+format+"\n", args...)
}

// cdDirective asks the shell wrapper to move its parent process. The wrapper
// gives dev a child-only file descriptor so normal output stays connected to
// the terminal and an inherited environment variable can never name a file to
// overwrite. NUL terminates the path because filesystem paths may contain
// newlines. Without the wrapper, retain the printable directive used by older
// integrations.
func (a *App) cdDirective(dir string) error {
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
