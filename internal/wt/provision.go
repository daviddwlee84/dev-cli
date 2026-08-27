// Package wt creates, provisions and removes the linked worktrees dev owns.
//
// Ownership, stated once so agents and humans stop improvising: dev owns every
// worktree a person might come back to tomorrow — features, fixes, experiments,
// cross-machine handoffs — and places them at the configured path template.
// Claude Code's `.claude/worktrees/` stays for turn-scoped subagent isolation
// that dies with the turn. dev never calls `herdr worktree create`, because
// the path policy has to hold on machines without herdr; it creates the
// worktree with git and asks herdr only to *open* it.
package wt

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Provisioner brings a fresh checkout up to a working state. A worktree is a
// clean checkout: it has no node_modules, no .venv and none of the gitignored
// env files the project needs, so without this step every new worktree starts
// broken.
type Provisioner struct {
	Include []string
	Link    []string
	Cmds    config.PostCreate
	Timeout time.Duration
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// Result records what provisioning did, so callers can report it honestly.
type Result struct {
	Copied  []string
	Linked  []string
	Ran     []string
	Skipped []string
	// Failures are non-fatal: a failed `npm ci` leaves a usable checkout the
	// user can fix by hand, so it is reported rather than rolled back.
	Failures []error
}

func (p *Provisioner) logf(format string, args ...any) {
	if p.Log != nil {
		fmt.Fprintf(p.Log, format+"\n", args...)
	}
}

// Provision copies gitignored files, creates opt-in symlinks and runs the
// post-create commands in dst.
func (p *Provisioner) Provision(ctx context.Context, src, dst string) (Result, error) {
	var res Result

	copied, err := p.copyIgnored(ctx, src, dst)
	if err != nil {
		return res, err
	}
	res.Copied = copied

	for _, rel := range p.Link {
		target := filepath.Join(src, rel)
		link := filepath.Join(dst, rel)
		if _, err := os.Stat(target); err != nil {
			res.Skipped = append(res.Skipped, rel+" (not present in source)")
			continue
		}
		if _, err := os.Lstat(link); err == nil {
			res.Skipped = append(res.Skipped, rel+" (already present)")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			res.Failures = append(res.Failures, err)
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			res.Failures = append(res.Failures, fmt.Errorf("link %s: %w", rel, err))
			continue
		}
		res.Linked = append(res.Linked, rel)
		p.logf("linked %s", rel)
	}

	for _, cmd := range p.commands(dst) {
		p.logf("running: %s", cmd)
		if err := p.runShell(ctx, dst, cmd); err != nil {
			res.Failures = append(res.Failures, err)
			continue
		}
		res.Ran = append(res.Ran, cmd)
	}
	return res, nil
}

// commands resolves the post-create command list, detecting from lockfiles
// when configured as "auto".
func (p *Provisioner) commands(dir string) []string {
	if !p.Cmds.Auto {
		return p.Cmds.Commands
	}
	return Detect(dir)
}

// detector maps a marker file to the command that reproduces its environment.
// Group names mutually exclusive ecosystems: only the first match within a
// group runs, so a project carrying both a uv.lock and a requirements.txt uses
// uv rather than trying both.
var detectors = []struct {
	marker string
	cmd    string
	bin    string
	group  string
}{
	{"uv.lock", "uv sync", "uv", "python"},
	{"poetry.lock", "poetry install", "poetry", "python"},
	{"Pipfile.lock", "pipenv install --dev", "pipenv", "python"},
	{"pnpm-lock.yaml", "pnpm install --frozen-lockfile", "pnpm", "js"},
	{"bun.lockb", "bun install", "bun", "js"},
	{"yarn.lock", "yarn install --immutable", "yarn", "js"},
	{"package-lock.json", "npm ci", "npm", "js"},
	{"go.mod", "go mod download", "go", "go"},
	{"Cargo.toml", "cargo fetch", "cargo", "rust"},
	{"Gemfile.lock", "bundle install", "bundle", "ruby"},
	{"mix.lock", "mix deps.get", "mix", "elixir"},
}

// Detect infers the provisioning commands for a checkout from its lockfiles.
//
// A detected command whose tool is not installed is skipped rather than run
// and failed: the user may simply not have that toolchain on this machine, and
// a missing `pnpm` should not make every worktree creation report an error.
func Detect(dir string) []string {
	var out []string
	done := map[string]bool{}
	for _, d := range detectors {
		if done[d.group] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, d.marker)); err != nil {
			continue
		}
		if _, err := exec.LookPath(d.bin); err != nil {
			// The lockfile settles which ecosystem this project uses, so a
			// missing tool means "cannot provision", not "try the next one".
			done[d.group] = true
			continue
		}
		out = append(out, d.cmd)
		done[d.group] = true
	}
	return out
}

func (p *Provisioner) runShell(ctx context.Context, dir, command string) error {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = p.Log, p.Log
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("post_create %q timed out after %s", command, timeout)
		}
		return fmt.Errorf("post_create %q: %w", command, err)
	}
	return nil
}

// copyIgnored copies the files matching Include from src into dst, but only
// those that are actually gitignored.
//
// The "only gitignored" rule is what makes this safe: a tracked file is
// already in the new checkout, so copying it would overwrite the branch's own
// version with the source branch's. The common mistake is listing something
// like .vscode/settings.json here — committed, therefore already present.
func (p *Provisioner) copyIgnored(ctx context.Context, src, dst string) ([]string, error) {
	if len(p.Include) == 0 {
		return nil, nil
	}
	candidates, err := p.ignoredMatching(ctx, src)
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, rel := range candidates {
		from, to := filepath.Join(src, rel), filepath.Join(dst, rel)
		info, err := os.Lstat(from)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue // directories are what `link` is for
		}
		if _, err := os.Stat(to); err == nil {
			continue // never clobber something the checkout already has
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return copied, err
		}
		if err := copyFile(from, to, info.Mode()); err != nil {
			return copied, fmt.Errorf("copy %s: %w", rel, err)
		}
		copied = append(copied, rel)
		p.logf("copied %s", rel)
	}
	return copied, nil
}

// ignoredMatching asks git which of the Include patterns name real, ignored
// files. Using git rather than matching by hand means .gitignore semantics
// (negations, directory rules, nested ignore files) are exactly right.
func (p *Provisioner) ignoredMatching(ctx context.Context, src string) ([]string, error) {
	args := []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"}
	args = append(args, p.Include...)
	out, err := gitx.Run(ctx, src, args...)
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, f := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if f != "" {
			rels = append(rels, f)
		}
	}
	return rels, nil
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	if _, err := io.Copy(w, in); err != nil {
		return err
	}
	return w.Flush()
}
