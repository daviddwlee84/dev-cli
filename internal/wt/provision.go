// Package wt creates, provisions and removes the linked worktrees dev owns.
//
// Ownership, stated once so agents and humans stop improvising: dev owns every
// worktree a person might come back to tomorrow — features, fixes, experiments,
// cross-machine handoffs — and places them at the configured path template.
// Claude Code's `.claude/worktrees/` stays for harness-owned, turn-scoped
// subagent isolation. dev never calls `herdr worktree create`, because
// the path policy has to hold on machines without herdr; it creates the
// worktree with git and asks herdr only to *open* it.
package wt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Provisioner brings a fresh checkout up to a working state.
//
// A worktree is a clean checkout: no node_modules, no .venv, and none of the
// gitignored env files the project needs. Without this step every new worktree
// starts broken, which is the commonest reason people give up on worktrees.
type Provisioner struct {
	Settings Settings
	Timeout  time.Duration
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

// Provision brings dst up to a working state, using src as the source of
// gitignored files and any copied dependency trees.
func (p *Provisioner) Provision(ctx context.Context, src, dst string) (Result, error) {
	plan := BuildPlan(ctx, p.Settings, src)
	return p.Apply(ctx, plan, src, dst)
}

// Apply executes a plan. Splitting this from BuildPlan is what lets
// `dev wt plan` show exactly what would happen without doing any of it.
func (p *Provisioner) Apply(ctx context.Context, plan Plan, src, dst string) (Result, error) {
	var res Result
	for _, w := range plan.Warnings {
		p.logf("warning: %s", w)
	}

	for _, step := range plan.Steps {
		if step.Skipped {
			res.Skipped = append(res.Skipped, step.What+" ("+step.Why+")")
			continue
		}
		switch step.Kind {
		case StepCopyFile:
			if err := p.copyFileStep(src, dst, step.What); err != nil {
				switch {
				case errors.Is(err, errAlreadyPresent):
					res.Skipped = append(res.Skipped, step.What+" (already present)")
				case errors.Is(err, errSourceMissing):
					res.Skipped = append(res.Skipped, step.What+" (source disappeared)")
				default:
					res.Failures = append(res.Failures, err)
				}
				continue
			}
			res.Copied = append(res.Copied, step.What)
			p.logf("copied %s", step.What)

		case StepCopyDir:
			from, to := filepath.Join(src, step.What), filepath.Join(dst, step.What)
			if _, err := os.Lstat(to); err == nil {
				res.Skipped = append(res.Skipped, step.What+" (already present)")
				continue
			}
			p.logf("copying %s …", step.What)
			if err := copyTree(from, to); err != nil {
				res.Failures = append(res.Failures, fmt.Errorf("copy %s: %w", step.What, err))
				continue
			}
			res.Copied = append(res.Copied, step.What+"/")
			p.logf("copied %s", step.What)

		case StepLinkDir:
			if err := p.linkStep(src, dst, step.What); err != nil {
				if errors.Is(err, errAlreadyPresent) {
					res.Skipped = append(res.Skipped, step.What+" (already present)")
					continue
				}
				res.Failures = append(res.Failures, err)
				continue
			}
			res.Linked = append(res.Linked, step.What)
			p.logf("linked %s", step.What)

		case StepRun:
			p.logf("running: %s", step.What)
			if err := p.runShell(ctx, dst, step.What); err != nil {
				res.Failures = append(res.Failures, err)
				continue
			}
			res.Ran = append(res.Ran, step.What)
		}
	}
	return res, nil
}

var (
	errAlreadyPresent = errors.New("already present")
	errSourceMissing  = errors.New("source disappeared")
)

func (p *Provisioner) copyFileStep(src, dst, rel string) error {
	from, to := filepath.Join(src, rel), filepath.Join(dst, rel)
	info, err := os.Lstat(from)
	if errors.Is(err, os.ErrNotExist) {
		return errSourceMissing
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy %s: refusing non-regular source", rel)
	}
	if err := rejectSymlinkParents(dst, filepath.Dir(to)); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if _, err := os.Lstat(to); err == nil {
		return errAlreadyPresent // never clobber what the checkout already has
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := rejectSymlinkParents(dst, filepath.Dir(to)); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if err := copyFile(from, to, info); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	return nil
}

func (p *Provisioner) linkStep(src, dst, rel string) error {
	target, link := filepath.Join(src, rel), filepath.Join(dst, rel)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("link %s: not present in the source checkout", rel)
	}
	if _, err := os.Lstat(link); err == nil {
		return errAlreadyPresent
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("link %s: %w", rel, err)
	}
	return nil
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

func rejectSymlinkParents(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("destination escapes worktree")
	}
	current := filepath.Clean(root)
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("destination parent %s is a symlink", current)
		case !info.IsDir():
			return fmt.Errorf("destination parent %s is not a directory", current)
		}
	}
	return nil
}

func copyFile(from, to string, expected os.FileInfo) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return errors.New("source changed after validation")
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, expected.Mode().Perm())
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

// copyTree duplicates a directory.
//
// Symlinks are recreated rather than followed: a pnpm node_modules is a farm
// of links into a global store, and dereferencing them would turn a 200MB
// copy into several gigabytes of duplicated packages.
func copyTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)

		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			dest, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(dest, target)
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyFile(path, target, info)
		}
		return nil // sockets, devices and the like have no place in a checkout
	})
}
