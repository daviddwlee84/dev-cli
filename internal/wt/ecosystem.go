package wt

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Strategy is how a worktree gets its installed dependencies.
//
// The choice is a real trade-off, not a preference: reinstalling is always
// correct but can take minutes; copying is fast but only sound for dependency
// trees that carry no absolute paths; sharing via a symlink is fastest but
// makes two checkouts write to one directory, which is how a build in one
// worktree breaks the other.
type Strategy string

const (
	// Reinstall runs the ecosystem's install command. Correct, and the default.
	Reinstall Strategy = "reinstall"
	// Copy duplicates the dependency directory from the source checkout.
	Copy Strategy = "copy"
	// Link symlinks it, so both checkouts share one tree.
	Link Strategy = "link"
	// Skip leaves the worktree without dependencies.
	Skip Strategy = "skip"
)

// Strategies lists the valid values, for validation and help text.
var Strategies = []Strategy{Reinstall, Copy, Link, Skip}

// ParseStrategy validates a configured strategy name.
func ParseStrategy(s string) (Strategy, bool) {
	for _, v := range Strategies {
		if Strategy(s) == v {
			return v, true
		}
	}
	return "", false
}

// Ecosystem describes one language and package manager pairing, and what is
// safe to do with the directory it installs into.
type Ecosystem struct {
	// Name groups mutually exclusive managers: only one "python" ecosystem
	// runs even if a project carries several markers.
	Name string
	// Manager is the tool that owns the install.
	Manager string
	// Marker is the file whose presence identifies this ecosystem.
	Marker string
	// Install reproduces the environment from the lockfile.
	Install string
	// Tool is the binary Install needs.
	Tool string
	// DepDirs are the directories holding installed dependencies. Empty when
	// the manager keeps everything in a global cache, which makes the whole
	// strategy question moot — reinstalling is already nearly free.
	DepDirs []string
	// CopySafe reports whether DepDirs survive being copied to another path.
	CopySafe bool
	// LinkSafe reports whether two checkouts can share one DepDirs tree.
	LinkSafe bool
	// Hazard explains why copying or linking is unsafe, so a refusal can say
	// something more useful than "not allowed".
	Hazard string
}

// SupportsStrategy reports whether s is sound for this ecosystem, and why not
// when it is not.
func (e Ecosystem) SupportsStrategy(s Strategy) (bool, string) {
	switch s {
	case Reinstall, Skip:
		return true, ""
	case Copy:
		if e.CopySafe {
			return true, ""
		}
		return false, e.Hazard
	case Link:
		if e.LinkSafe {
			return true, ""
		}
		return false, e.Hazard
	}
	return false, "unknown strategy"
}

// ecosystems is the detection table, in priority order within each Name.
//
// Order matters: a project with both a uv.lock and a requirements.txt is a uv
// project, and running pip afterwards would fight it.
var ecosystems = []Ecosystem{
	{
		Name: "python", Manager: "uv", Marker: "uv.lock", Install: "uv sync", Tool: "uv",
		DepDirs: []string{".venv"},
		Hazard: "a virtualenv bakes its own absolute path into pyvenv.cfg and bin/activate, " +
			"so it does not work from a second location; uv reinstalls from a shared cache in seconds",
	},
	{
		Name: "python", Manager: "poetry", Marker: "poetry.lock", Install: "poetry install", Tool: "poetry",
		DepDirs: []string{".venv"},
		Hazard:  "a virtualenv bakes its own absolute path into pyvenv.cfg and bin/activate",
	},
	{
		Name: "python", Manager: "pipenv", Marker: "Pipfile.lock", Install: "pipenv install --dev", Tool: "pipenv",
		DepDirs: []string{".venv"},
		Hazard:  "a virtualenv bakes its own absolute path into pyvenv.cfg and bin/activate",
	},
	{
		Name: "node", Manager: "pnpm", Marker: "pnpm-lock.yaml",
		Install: "pnpm install --frozen-lockfile", Tool: "pnpm",
		DepDirs: []string{"node_modules"}, CopySafe: true,
		Hazard: "sharing one node_modules means an install in either worktree silently changes the other",
	},
	{
		Name: "node", Manager: "bun", Marker: "bun.lockb", Install: "bun install", Tool: "bun",
		DepDirs: []string{"node_modules"}, CopySafe: true,
		Hazard: "sharing one node_modules means an install in either worktree silently changes the other",
	},
	{
		Name: "node", Manager: "yarn", Marker: "yarn.lock", Install: "yarn install --immutable", Tool: "yarn",
		DepDirs: []string{"node_modules"}, CopySafe: true,
		Hazard: "sharing one node_modules means an install in either worktree silently changes the other",
	},
	{
		Name: "node", Manager: "npm", Marker: "package-lock.json", Install: "npm ci", Tool: "npm",
		DepDirs: []string{"node_modules"}, CopySafe: true,
		Hazard: "sharing one node_modules means an install in either worktree silently changes the other",
	},
	{
		// The module cache is global and content-addressed, so a worktree
		// needs nothing copied and the download is usually a no-op.
		Name: "go", Manager: "go", Marker: "go.mod", Install: "go mod download", Tool: "go",
	},
	{
		Name: "rust", Manager: "cargo", Marker: "Cargo.toml", Install: "cargo fetch", Tool: "cargo",
		DepDirs: []string{"target"}, CopySafe: true,
		Hazard: "two cargo builds writing one target directory corrupt each other's lock; " +
			"and target/ is usually large enough that copying costs more than rebuilding",
	},
	{
		Name: "ruby", Manager: "bundler", Marker: "Gemfile.lock", Install: "bundle install", Tool: "bundle",
		DepDirs: []string{"vendor/bundle"}, CopySafe: true,
		Hazard: "sharing vendored gems across checkouts makes a bundle update in one affect the other",
	},
	{
		Name: "elixir", Manager: "mix", Marker: "mix.lock", Install: "mix deps.get", Tool: "mix",
		DepDirs: []string{"deps", "_build"}, CopySafe: true,
		Hazard: "_build holds compiled artifacts keyed to a path",
	},
}

// DetectEcosystems reports the ecosystems a checkout uses, one per Name.
//
// A detected ecosystem whose tool is missing is still reported — knowing the
// project needs uv is useful even on a machine without it, and silently
// dropping it would make `dev wt plan` lie about what the project is.
func DetectEcosystems(dir string) []Ecosystem {
	var out []Ecosystem
	claimed := map[string]bool{}
	for _, e := range ecosystems {
		if claimed[e.Name] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Marker)); err != nil {
			continue
		}
		claimed[e.Name] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ToolInstalled reports whether the ecosystem's install command can run here.
func (e Ecosystem) ToolInstalled() bool {
	if e.Tool == "" {
		return true
	}
	_, err := exec.LookPath(e.Tool)
	return err == nil
}

// Detect infers the provisioning commands for a checkout from its lockfiles.
//
// Kept as the simple form for callers that only want the commands. A detected
// command whose tool is not installed is skipped rather than run and failed:
// the user may simply not have that toolchain on this machine, and a missing
// pnpm should not make every worktree creation report an error.
func Detect(dir string) []string {
	var out []string
	for _, e := range DetectEcosystems(dir) {
		if e.Install == "" || !e.ToolInstalled() {
			continue
		}
		out = append(out, e.Install)
	}
	return out
}
