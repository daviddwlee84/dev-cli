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
	if a.noRuntime {
		return runtime.None{}
	}
	backend := a.Cfg.Runtime.Backend
	if a.runtimeOverride != "" {
		backend = a.runtimeOverride
	}
	rt := runtime.Select(backend)
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

// cdDirective emits the marker the shell wrapper installed by
// `dev shell-init` looks for. A child process cannot change its parent's
// working directory, so the only way `dev resume` can leave the user *in* the
// checkout is to ask the shell to do it.
func (a *App) cdDirective(dir string) {
	fmt.Fprintf(a.Out, "cd %s\n", shellQuote(dir))
}

// ctx is the cancellable context commands run under.
func ctxOf() context.Context { return context.Background() }
