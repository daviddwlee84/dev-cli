package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repobrowse"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func newTUIGHDashActions(current func() *App) tui.GHDashActions {
	return tui.GHDashActions{
		Probe: ghDashInstalled,
		Prepare: func(ctx context.Context, request tui.GHDashRequest) (tui.Workflow, error) {
			return &ghDashWorkflow{ctx: ctx, app: *current(), request: request}, nil
		},
	}
}

// Native extension inventory is local. Never run dash, auth, or an extension
// installer merely to decide whether to display its action.
func ghDashInstalled(ctx context.Context) bool {
	executable, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "extension", "list")
	command.WaitDelay = 500 * time.Millisecond
	command.Env = ghDashEnvironment(os.Environ(), map[string]string{
		"GH_NO_UPDATE_NOTIFIER": "1", "GH_NO_EXTENSION_UPDATE_NOTIFIER": "1", "GH_PROMPT_DISABLED": "1", "NO_COLOR": "1", "CLICOLOR": "0",
	}, "GH_FORCE_TTY")
	output := &ghDashBoundedBuffer{}
	command.Stdout, command.Stderr = output, io.Discard
	if command.Run() != nil || output.overflow {
		return false
	}
	return ghDashExtensionListed(output.String())
}

type ghDashBoundedBuffer struct {
	data     bytes.Buffer
	overflow bool
}

func (b *ghDashBoundedBuffer) Len() int       { return b.data.Len() }
func (b *ghDashBoundedBuffer) String() string { return b.data.String() }

func (b *ghDashBoundedBuffer) Write(data []byte) (int, error) {
	const maxBytes = 64 * 1024
	size := len(data)
	available := maxBytes - b.Len()
	if len(data) > available {
		data = data[:available]
		b.overflow = true
	}
	_, _ = b.data.Write(data)
	return size, nil
}

func ghDashExtensionListed(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "gh" && fields[1] == "dash" {
			return true
		}
	}
	return false
}

func ghDashEnvironment(base []string, values map[string]string, remove ...string) []string {
	discard := map[string]bool{}
	for key := range values {
		discard[strings.ToUpper(key)] = true
	}
	for _, key := range remove {
		discard[strings.ToUpper(key)] = true
	}
	result := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !discard[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	// Stable output makes environment tests and diagnostics reproducible; the
	// values themselves are runtime references and are never logged.
	for _, key := range []string{"GH_HOST", "GH_REPO", "GH_NO_UPDATE_NOTIFIER", "GH_NO_EXTENSION_UPDATE_NOTIFIER", "GH_PROMPT_DISABLED", "NO_COLOR", "CLICOLOR"} {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

type ghDashWorkflow struct {
	ctx     context.Context
	app     App
	request tui.GHDashRequest
}

func (w *ghDashWorkflow) SetStdin(input io.Reader)   { w.app.In = input }
func (w *ghDashWorkflow) SetStdout(output io.Writer) { w.app.Out = output }
func (w *ghDashWorkflow) SetStderr(output io.Writer) { w.app.Err = output }
func (w *ghDashWorkflow) Result() tui.WorkflowResult {
	return tui.WorkflowResult{Status: "returned from gh-dash"}
}

func (w *ghDashWorkflow) Run() error {
	repository, err := w.resolveTarget()
	if err != nil {
		return err
	}
	web, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: &repository})
	if !ok || web.Provider != forge.GitHub {
		return fmt.Errorf("gh-dash requires an exact GitHub repository")
	}
	if !ghDashInstalled(w.ctx) {
		return fmt.Errorf("gh-dash is unavailable; install it explicitly with `gh extension install dlvhdr/gh-dash`")
	}
	parsed, err := url.Parse(web.URL)
	if err != nil {
		return err
	}
	directory := w.request.Path
	if directory != "" {
		if _, err := gitx.Discover(w.ctx, directory); err != nil {
			return fmt.Errorf("selected checkout changed: %w", err)
		}
		if !w.request.ResolveLocal {
			topology, err := gitx.RecoveryTopologyOf(w.ctx, directory)
			if err != nil {
				return err
			}
			matched := false
			for _, remote := range topology.Remotes {
				for _, raw := range remote.FetchURLs {
					current, valid := forge.DeriveWebURL(forge.WebURLRequest{Remote: raw})
					if valid && current.Provider == forge.GitHub && current.URL == web.URL {
						matched = true
					}
				}
			}
			if !matched {
				return fmt.Errorf("selected checkout no longer matches %s", repository.FullName)
			}
		}
	} else {
		// A neutral private directory avoids inheriting an unrelated repo's
		// .gh-dash.yml. This does not create a checkout or alter native config.
		directory, err = os.MkdirTemp("", "dev-gh-dash-")
		if err != nil {
			return err
		}
		defer func() {
			// Native user keybindings may create files here. Remove only our
			// empty directory; retained user work must not become temp cleanup.
			if cleanupErr := os.Remove(directory); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				fmt.Fprintf(w.app.Err, "gh-dash directory retained at %s: %v\n", directory, cleanupErr)
			}
		}()
	}
	executable, err := exec.LookPath("gh")
	if err != nil {
		return err
	}
	command := exec.CommandContext(w.ctx, executable, "dash")
	command.Dir = directory
	command.Env = ghDashEnvironment(os.Environ(), map[string]string{
		"GH_HOST": parsed.Host, "GH_REPO": parsed.Host + "/" + repository.FullName,
		"GH_NO_UPDATE_NOTIFIER": "1", "GH_NO_EXTENSION_UPDATE_NOTIFIER": "1",
	})
	command.Stdin, command.Stdout, command.Stderr = w.app.In, w.app.Out, w.app.Err
	return command.Run()
}

func (w *ghDashWorkflow) resolveTarget() (forge.RemoteRepo, error) {
	if !w.request.ResolveLocal {
		return w.request.Repository, nil
	}
	choices, err := repobrowse.Choices(w.ctx, w.request.Path, "")
	if err != nil {
		return forge.RemoteRepo{}, err
	}
	var repositories []forge.RemoteRepo
	var names []string
	for _, choice := range choices {
		identity := forge.ParseRemoteIdentity(choice.URL)
		if identity.Kind != forge.GitHub {
			continue
		}
		repositories = append(repositories, forge.RemoteRepo{Forge: forge.GitHub, FullName: identity.Name, URL: choice.URL})
		names = append(names, choice.Remote)
	}
	if len(repositories) == 0 {
		return forge.RemoteRepo{}, fmt.Errorf("selected checkout has no supported GitHub repository target")
	}
	if expected := w.request.Repository; expected.URL != "" {
		if len(repositories) != 1 || repositories[0].URL != expected.URL {
			return forge.RemoteRepo{}, fmt.Errorf("repository remote changed; refresh and select gh-dash again")
		}
		return repositories[0], nil
	}
	if len(repositories) == 1 {
		return repositories[0], nil
	}
	if !w.app.interactive() {
		return forge.RemoteRepo{}, fmt.Errorf("choose an exact GitHub remote before opening gh-dash")
	}
	for index, repository := range repositories {
		fmt.Fprintf(w.app.Out, "  %s  %s\n", names[index], repository.URL)
	}
	prompt := newPrompter(&w.app)
	for {
		name, err := prompt.line("GitHub remote", "")
		if err != nil {
			return forge.RemoteRepo{}, err
		}
		for index, candidate := range names {
			if candidate == name {
				return repositories[index], nil
			}
		}
		fmt.Fprintln(w.app.Out, "Choose an exact remote name from: "+strings.Join(names, ", "))
	}
}
