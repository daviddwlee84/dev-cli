package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

var (
	//go:embed assets/agent-history-pre-commit.yaml
	agentHistoryPreCommit string
	//go:embed assets/agent-history-gitleaks.toml
	agentHistoryGitleaks string
)

func runBuiltinRepoSkillSetup(ctx context.Context, app *App, root, setup string, args []string) error {
	switch setup {
	case "agent-history-hygiene":
		return bootstrapAgentHistoryHygiene(ctx, app, root)
	case "project-knowledge-harness":
		return bootstrapProjectKnowledge(root, argumentValue(args, "--deployment", "none"))
	default:
		return fmt.Errorf("unknown built-in repository setup %q", setup)
	}
}

func bootstrapAgentHistoryHygiene(ctx context.Context, app *App, root string) error {
	if err := writeBuiltinSetupFiles(root, map[string]string{
		".pre-commit-config.yaml": agentHistoryPreCommit,
		".gitleaks.toml":          agentHistoryGitleaks,
	}); err != nil {
		return err
	}
	if hooksPath, err := gitx.Run(ctx, root, "config", "--get", "core.hooksPath"); err == nil && strings.TrimSpace(hooksPath) != "" {
		fmt.Fprintf(app.Out, "core.hooksPath is set to %s; skipped per-repository pre-commit install\n", hooksPath)
		return nil
	}
	command := "pre-commit"
	args := []string{"install"}
	if _, err := exec.LookPath(command); err != nil {
		command = "uvx"
		args = []string{"pre-commit@4", "install"}
		if _, uvxErr := exec.LookPath(command); uvxErr != nil {
			return errors.New("agent-history-hygiene requires pre-commit or uvx to install the Git hook")
		}
	}
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = root
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("install pre-commit hook: %w", err)
	}
	return nil
}

func bootstrapProjectKnowledge(root, deployment string) error {
	name := filepath.Base(root)
	deployment = strings.TrimSpace(deployment)
	if deployment == "" {
		deployment = "none"
	}
	files := map[string]string{
		"TODO.md":            projectTODO(name),
		"backlog/README.md":  projectBacklogREADME(deployment),
		"backlog/inbox.md":   projectBacklogInbox,
		"pitfalls/README.md": projectPitfallsREADME(deployment),
	}
	if err := writeBuiltinSetupFiles(root, files); err != nil {
		return err
	}
	if err := appendBuiltinSetupBlock(root, "AGENTS.md", "<!-- project-knowledge-harness:agent-guidance -->", projectAgentGuidance); err != nil {
		return err
	}
	return appendBuiltinSetupBlock(root, "README.md", "<!-- project-knowledge-harness:readme-roadmap -->", projectReadmeRoadmap)
}

func writeBuiltinSetupFiles(root string, contents map[string]string) error {
	canonicalRoot, err := pathx.Canonical(root)
	if err != nil {
		return err
	}
	plan := scaffold.Plan{Root: canonicalRoot}
	for relative, content := range contents {
		plan.Files = append(plan.Files, scaffold.FilePlan{
			ID: relative, RelativePath: filepath.ToSlash(relative), Path: filepath.Join(canonicalRoot, filepath.FromSlash(relative)),
			Content: strings.TrimRight(content, "\n") + "\n", Mode: 0o644,
		})
	}
	_, err = scaffold.ApplyFiles(plan, scaffold.ExistingSkip)
	return err
}

func appendBuiltinSetupBlock(root, relative, marker, block string) error {
	canonicalRoot, err := pathx.Canonical(root)
	if err != nil {
		return err
	}
	candidate := filepath.Join(canonicalRoot, filepath.FromSlash(relative))
	path, err := pathx.CanonicalChild(canonicalRoot, candidate)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	existing := []byte(nil)
	info, err := os.Lstat(candidate)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file, not a symlink or special file", relative)
		}
		mode = info.Mode().Perm()
		existing, err = os.ReadFile(path)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	} else {
		return err
	}
	if err != nil {
		return err
	}
	if strings.Contains(string(existing), marker) {
		return nil
	}
	updated := strings.TrimRight(string(existing), "\n")
	if updated != "" {
		updated += "\n\n"
	}
	updated += marker + "\n" + strings.TrimSpace(block) + "\n" + strings.TrimSuffix(marker, " -->") + " (end) -->\n"
	_, err = scaffold.ApplyFiles(scaffold.Plan{Root: canonicalRoot, Files: []scaffold.FilePlan{{
		ID: relative, RelativePath: filepath.ToSlash(relative), Path: path, Content: updated, Mode: mode,
	}}}, scaffold.ExistingOverwrite)
	return err
}

func argumentValue(args []string, name, fallback string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return fallback
}

func projectTODO(name string) string {
	return fmt.Sprintf(`# TODO

Long-term backlog for %s. Keep future work here; put resume-friendly research in backlog/.

## P1

## P2

## P3

## P?

Use this lane for items that need a spike before prioritisation.

## Done
`, name)
}

func projectBacklogREADME(deployment string) string {
	return fmt.Sprintf(`# Backlog research

Long-form research, design notes, and paused troubleshooting for items indexed by ../TODO.md.
Write one topic per file so work can resume without repeating the investigation.

Deployment profile recorded during setup: %s.
`, deployment)
}

func projectPitfallsREADME(deployment string) string {
	return fmt.Sprintf(`# Pitfalls

Past traps, indexed by symptom. Preserve verbatim errors, root cause, workaround, and prevention
so the next occurrence is grep-able instead of re-debugged.

Deployment profile recorded during setup: %s.
`, deployment)
}

const projectBacklogInbox = `# Inbox

Quick-capture area for ideas whose priority or wording is not clear yet.
The project-knowledge-harness skill can later sweep these lines into TODO.md.
`

const projectAgentGuidance = `### Project knowledge

- Record deferred work in TODO.md with a priority and effort; use backlog/ for longer research.
- Record non-obvious past traps in pitfalls/, titled by the symptom and preserving exact errors.
- Do not create competing ROADMAP.md, IDEAS.md, or BACKLOG.md indexes.`

const projectReadmeRoadmap = `## Roadmap & lessons learned

Future work is indexed in [TODO.md](TODO.md), with longer research under [backlog/](backlog/).
Past traps and their workarounds live under [pitfalls/](pitfalls/).`
