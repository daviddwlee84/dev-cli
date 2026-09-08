package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type bridgeBinding struct {
	Protocol        int           `json:"protocol"`
	Definition      mcpDefinition `json:"definition"`
	Program         string        `json:"program"`
	ProgramIdentity string        `json:"program_identity"`
	WorkDir         string        `json:"work_dir"`
}
type launchSpec struct {
	Program string
	Args    []string
	Env     []string
	Dir     string
}

func executableIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("MCP executable prerequisite is unavailable")
	}
	id, err := fileIdentity(info)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%d:%d", id, info.Size(), info.ModTime().UnixNano(), info.Mode()), nil
}

func trustedMCPExecutable(command string, roots ...string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", errors.New("MCP executable prerequisite is missing")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if strings.Contains(filepath.ToSlash(path), "/node_modules/.bin/") {
		return "", errors.New("repository-local MCP executable shims are not trusted")
	}
	for _, root := range roots {
		if pathInside(root, path) {
			return "", errors.New("launcher executable must resolve outside the selected checkout; use native configuration for project executables")
		}
	}
	return path, nil
}

func bindLauncher(b *builder, d mcpDefinition, owner *record) (mcpDefinition, error) {
	req := b.r.Request
	if d.Transport != "stdio" {
		return d, errors.New("HTTP credentials require the destination client environment; a stdio launcher cannot inject them")
	}
	if !filepath.IsAbs(req.Launcher) {
		return d, errors.New("an installed dev launcher is required")
	}
	if req.EnvFile != "" {
		if err := validLocation(Location{req.To.Root, req.EnvFile}); err != nil {
			return d, errors.New("env source must be a relative path within the destination scope")
		}
	}
	program, err := trustedMCPExecutable(d.Command, projectRoots(req)...)
	if err != nil {
		return d, err
	}
	id, err := executableIdentity(program)
	if err != nil {
		return d, err
	}
	cwd := req.To.Root
	if d.Cwd != "" {
		cwd = d.Cwd
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || !pathInside(req.To.Root, resolved) {
		return d, errors.New("launcher cwd must stay in the destination checkout")
	}
	b.r.Bridge = &bridgeBinding{Protocol: 1, Definition: d, Program: program, ProgramIdentity: id, WorkDir: resolved}
	bindingID := b.r.ID
	if owner != nil && owner.Bridge != nil && owner.Request.From == req.From && owner.Request.EnvFile == req.EnvFile && owner.Request.Launcher == req.Launcher && reflect.DeepEqual(owner.Bridge, b.r.Bridge) {
		bindingID = owner.ID
		b.r.Bridge = nil
	}
	b.r.Notes = append(b.r.Notes, "This optional launcher depends on this dev installation and host-local binding. Rebuild the binding after cloning or moving the checkout. Credentials are resolved only when the server launches.")
	forward := map[string]envValue{}
	if req.To.Agent == "codex" {
		for _, value := range d.Env {
			if value.Variable != "" {
				forward[value.Variable] = envValue{Variable: value.Variable}
			}
		}
	}
	return mcpDefinition{Transport: "stdio", Command: req.Launcher, Args: []string{"mcp", "_launch", "--binding", bindingID, "--state", b.st.root.Name()}, Env: forward, Headers: map[string]envValue{}}, nil
}

func (s Service) prepareLaunch(ctx context.Context, id string) (launchSpec, error) {
	var spec launchSpec
	st, err := s.open(false)
	if err != nil {
		return spec, err
	}
	defer st.close()
	r, err := st.load(ctx, id)
	if err != nil {
		return spec, err
	}
	if r.Status != "applied" || r.Bridge == nil || r.Bridge.Protocol != 1 {
		return spec, errors.New("MCP binding is not applied or needs a newer dev protocol")
	}
	h, err := openRoots(r)
	if err != nil {
		return spec, err
	}
	defer h.close()
	if err = verifyReceipt(ctx, st, h, r); err != nil {
		return spec, err
	}
	if err = validateMCPPolicy(ctx, r.Request); err != nil {
		return spec, err
	}
	ref := r.Request.From
	img, data, err := snapshot(ctx, h.roots[ref.Root], ref.Path, st.key)
	if err != nil || img.Kind != "file" {
		return spec, errors.New("source declaration cannot be safely observed")
	}
	fields, exists, err := mcpFields(data, ref.Agent, r.Request.Name)
	if err != nil || !exists {
		return spec, errors.New("source declaration changed; refresh and apply the bridge")
	}
	d, err := parseMCP(ref.Agent, fields, r.Request.Transport)
	if err != nil {
		return spec, err
	}
	if err = bindDefinition(&d, r.Request); err != nil {
		return spec, err
	}
	if d.Cwd != "" && !filepath.IsAbs(d.Cwd) {
		d.Cwd = filepath.Join(r.Request.To.Root, d.Cwd)
	}
	if !reflect.DeepEqual(d, r.Bridge.Definition) {
		return spec, errors.New("source executable, arguments, or reference structure changed; refresh and apply before launching")
	}
	programID, err := executableIdentity(r.Bridge.Program)
	if err != nil || programID != r.Bridge.ProgramIdentity {
		return spec, errors.New("MCP executable changed; refresh its binding")
	}
	local := map[string]string{}
	if r.Request.EnvFile != "" {
		root := h.roots[r.Request.To.Root]
		if root == nil {
			return spec, ErrStale
		}
		img, data, err := snapshot(ctx, root, r.Request.EnvFile, st.key)
		if err != nil || img.Kind != "file" || img.Mode&0o077 != 0 {
			return spec, errors.New("local env source must be a regular owner-private file within the selected scope")
		}
		var document struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal(data, &document) != nil || document.Env == nil {
			return spec, errors.New("local env source needs a JSON env object")
		}
		local = document.Env
	}
	env := map[string]string{}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	// Strip project-controlled PATH components before selecting child helpers.
	for _, item := range providerEnvironment("", projectRoots(r.Request)...) {
		if strings.HasPrefix(item, "PATH=") {
			env["PATH"] = strings.TrimPrefix(item, "PATH=")
		}
	}
	for name, value := range d.Env {
		resolved := value.Literal
		if value.Variable != "" {
			var ok bool
			resolved, ok = os.LookupEnv(value.Variable)
			if !ok {
				resolved, ok = local[value.Variable]
			}
			if !ok && value.Fallback != nil {
				resolved, ok = *value.Fallback, true
			}
			if !ok {
				return spec, fmt.Errorf("missing environment reference %s", value.Variable)
			}
		}
		if strings.ContainsRune(resolved, 0) {
			return spec, errors.New("environment source contains an invalid value")
		}
		env[name] = resolved
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec.Env = append(spec.Env, name+"="+env[name])
	}
	spec.Program = r.Bridge.Program
	spec.Args = append([]string{r.Bridge.Program}, d.Args...)
	spec.Dir = r.Bridge.WorkDir
	return spec, nil
}

// Launch is used only by the hidden MCP protocol entrypoint. Errors contain
// reference names and static diagnostics, never resolved credential values.
func (s Service) Launch(ctx context.Context, id string) error {
	if err := platformTransfers(); err != nil {
		return err
	}
	spec, err := s.prepareLaunch(ctx, id)
	if err != nil {
		return err
	}
	root, _, err := safefile.OpenRoot(spec.Dir)
	if err != nil {
		return errors.New("launcher checkout is unavailable")
	}
	_ = root.Close()
	return executeMCP(ctx, spec)
}
