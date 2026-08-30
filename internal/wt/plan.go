package wt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

// StepKind is what a provisioning step does.
type StepKind string

const (
	// StepCopyFile copies one gitignored file into the new checkout.
	StepCopyFile StepKind = "copy"
	// StepCopyDir duplicates a dependency directory.
	StepCopyDir StepKind = "copy-dir"
	// StepLinkDir symlinks a directory so both checkouts share it.
	StepLinkDir StepKind = "link"
	// StepRun executes a shell command in the new checkout.
	StepRun StepKind = "run"
)

// Step is one action in a provisioning plan.
type Step struct {
	Kind StepKind
	// What is the path or command the step acts on.
	What string
	// Why explains the step in one line, for `dev wt plan`.
	Why string
	// Ecosystem names the language this step belongs to, empty for the
	// file-copying steps that belong to no ecosystem.
	Ecosystem string
	// Skipped marks a step that will not run, with Why holding the reason.
	Skipped bool
}

// Plan is everything provisioning would do to a new worktree of a repository,
// computed without touching anything.
//
// Making this inspectable matters more than it looks: a worktree that comes up
// broken is the single most common reason people stop using worktrees at all,
// and "what will this actually do" is not answerable from the config alone —
// it depends on which lockfiles exist, which tools are installed, and which
// files are genuinely gitignored.
type Plan struct {
	RepoPath   string
	Ecosystems []Ecosystem
	Steps      []Step
	// Warnings are problems that will not stop provisioning but will surprise
	// someone: an unsound strategy that was downgraded, a missing tool.
	Warnings []string
}

// Runnable returns the steps that will actually execute.
func (p Plan) Runnable() []Step {
	var out []Step
	for _, s := range p.Steps {
		if !s.Skipped {
			out = append(out, s)
		}
	}
	return out
}

// Empty reports a plan that would do nothing at all.
func (p Plan) Empty() bool { return len(p.Runnable()) == 0 }

// Settings are the provisioning inputs, resolved from global config and the
// repository's .dev-cli/config.toml (plus legacy .dev.toml compatibility).
type Settings struct {
	Include          []string
	Link             []string
	Cmds             config.PostCreate
	Strategy         Strategy
	Strategies       map[string]string
	ProvisionTimeout time.Duration
}

// SettingsFor resolves effective provisioning settings without enforcing
// executable-config trust; use SettingsForTrusted before running commands.
func SettingsFor(cfg config.Config, repoPath string) Settings {
	s := Settings{
		Include:          cfg.Worktree.Include,
		Link:             cfg.Worktree.Link,
		Cmds:             cfg.Worktree.PostCreate,
		Strategy:         Reinstall,
		Strategies:       map[string]string{},
		ProvisionTimeout: cfg.Worktree.ProvisionTimeout.Duration,
	}
	if v, ok := ParseStrategy(cfg.Worktree.Strategy); ok {
		s.Strategy = v
	}
	for k, v := range cfg.Worktree.Strategies {
		s.Strategies[k] = v
	}
	if o, ok := LoadRepoOverride(repoPath); ok {
		if len(o.Worktree.Include) > 0 {
			s.Include = o.Worktree.Include
		}
		if len(o.Worktree.Link) > 0 {
			s.Link = o.Worktree.Link
		}
		if o.Worktree.PostCreate != nil {
			s.Cmds = *o.Worktree.PostCreate
		}
		if o.Worktree.Strategy != "" {
			// Keep an invalid value so strategyFor can report it in the plan
			// instead of silently turning a repo-local typo into reinstall.
			s.Strategy = Strategy(o.Worktree.Strategy)
		}
		for k, v := range o.Worktree.Strategies {
			s.Strategies[k] = v
		}
	}
	// The directory-based project config supersedes the legacy .dev.toml while
	// retaining the same deliberately narrow provisioning surface.
	if project, err := projectconfig.Load(repoPath, nil); err == nil && project.ConfigPresent {
		o := project.Effective.Worktree
		if o.Include != nil {
			s.Include = append([]string(nil), (*o.Include)...)
		}
		if o.Link != nil {
			s.Link = append([]string(nil), (*o.Link)...)
		}
		if o.PostCreate != nil {
			s.Cmds = *o.PostCreate
		}
		if o.Strategy != nil {
			s.Strategy = Strategy(*o.Strategy)
		}
		if o.Strategies != nil {
			for k, v := range *o.Strategies {
				s.Strategies[k] = v
			}
		}
		if o.ProvisionTimeout != nil {
			s.ProvisionTimeout = o.ProvisionTimeout.Duration
		}
	}
	return s
}

// SettingsForTrusted resolves provisioning settings and refuses executable
// commands from the new project config until their exact content hash has been
// approved locally. Legacy .dev.toml retains its compatibility behavior.
func SettingsForTrusted(ctx context.Context, cfg config.Config, repoPath string) (Settings, error) {
	project, err := projectconfig.Load(repoPath, nil)
	if err != nil {
		return Settings{}, err
	}
	if project.ConfigPresent && project.RequiresTrust() && project.Effective.Worktree.PostCreate != nil {
		store := projectconfig.NewTrustStore(filepath.Join(cfg.StateDir(), "trust", "project-config-v1.json"))
		trusted, err := store.Check(ctx, repoPath, project.ExecutionHash)
		if err != nil {
			return Settings{}, err
		}
		if !trusted {
			return Settings{}, fmt.Errorf("project worktree setup is executable and not trusted; review it, then run dev config trust %s --yes", config.Contract(repoPath))
		}
	}
	return SettingsFor(cfg, repoPath), nil
}

// strategyFor resolves the strategy for one ecosystem, narrowing to something
// sound and recording why when it has to.
func (s Settings) strategyFor(e Ecosystem) (Strategy, string) {
	want := s.Strategy
	if _, ok := ParseStrategy(string(want)); !ok {
		return Reinstall, fmt.Sprintf("%s: %q is not a strategy (want %s), using reinstall",
			e.Name, want, joinStrategies())
	}
	if v, ok := s.Strategies[e.Name]; ok {
		if parsed, ok := ParseStrategy(v); ok {
			want = parsed
		} else {
			return Reinstall, fmt.Sprintf("%s: %q is not a strategy (want %s), using reinstall",
				e.Name, v, joinStrategies())
		}
	}
	// An ecosystem with no dependency directory has nothing to copy or share,
	// so those strategies collapse to reinstalling — which for a global cache
	// is nearly free anyway.
	if len(e.DepDirs) == 0 && (want == Copy || want == Link) {
		return Reinstall, ""
	}
	if ok, hazard := e.SupportsStrategy(want); !ok {
		return Reinstall, fmt.Sprintf("%s: %s is unsound here — %s; using reinstall instead",
			e.Name, want, hazard)
	}
	return want, ""
}

func joinStrategies() string {
	parts := make([]string, len(Strategies))
	for i, s := range Strategies {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// BuildPlan computes what provisioning a new worktree of repoPath would do.
// It reads the filesystem and asks git which files are ignored, but changes
// nothing.
func BuildPlan(ctx context.Context, set Settings, repoPath string) Plan {
	p := Plan{RepoPath: repoPath, Ecosystems: DetectEcosystems(repoPath)}

	// Gitignored files to carry over.
	if files, err := ignoredMatching(ctx, repoPath, set.Include); err != nil {
		p.Warnings = append(p.Warnings, "could not list ignored files: "+err.Error())
	} else {
		for _, rel := range files {
			info, err := os.Lstat(filepath.Join(repoPath, rel))
			if err != nil {
				continue
			}
			if info.IsDir() {
				continue // directories belong to `link`, not `include`
			}
			p.Steps = append(p.Steps, Step{
				Kind: StepCopyFile, What: rel,
				Why: "gitignored, so the checkout would not have it",
			})
		}
	}

	// Explicit symlinks, which are always the user's own decision.
	for _, rel := range set.Link {
		step := Step{Kind: StepLinkDir, What: rel, Why: "configured in worktree.link"}
		if _, err := os.Stat(filepath.Join(repoPath, rel)); err != nil {
			step.Skipped, step.Why = true, "not present in the source checkout"
		}
		p.Steps = append(p.Steps, step)
	}

	// Dependencies, per ecosystem.
	//
	// The strategy and post_create are separate axes. A strategy says how a
	// dependency directory arrives; post_create says what commands run. So an
	// explicit command list replaces the *derived install commands* but still
	// leaves copy and link in effect — a project that bootstraps with `make
	// setup` may still want its node_modules copied rather than rebuilt.
	explicit := !set.Cmds.Auto
	for _, e := range p.Ecosystems {
		strategy, warn := set.strategyFor(e)
		if warn != "" {
			p.Warnings = append(p.Warnings, warn)
		}

		switch strategy {
		case Copy, Link:
			kind, verb := StepCopyDir, "copied"
			if strategy == Link {
				kind, verb = StepLinkDir, "shared"
			}
			for _, dir := range e.DepDirs {
				step := Step{
					Kind: kind, What: dir, Ecosystem: e.Name,
					Why: fmt.Sprintf("%s dependencies, %s instead of reinstalling", e.Manager, verb),
				}
				if _, err := os.Stat(filepath.Join(repoPath, dir)); err != nil {
					step.Skipped = true
					step.Why = "not present in the source checkout — nothing to " + string(strategy)
				}
				p.Steps = append(p.Steps, step)
			}

		case Skip:
			if explicit {
				continue
			}
			p.Steps = append(p.Steps, Step{
				Kind: StepRun, What: e.Install, Ecosystem: e.Name,
				Why: "strategy is skip", Skipped: true,
			})

		default: // Reinstall
			if explicit {
				continue // the configured command list replaces this
			}
			step := Step{
				Kind: StepRun, What: e.Install, Ecosystem: e.Name,
				Why: e.Marker + " detected",
			}
			if !e.ToolInstalled() {
				step.Skipped = true
				step.Why = e.Tool + " is not installed on this machine"
				p.Warnings = append(p.Warnings,
					fmt.Sprintf("%s needs %s, which is not installed — worktrees will come up without its dependencies",
						e.Name, e.Tool))
			}
			p.Steps = append(p.Steps, step)
		}
	}

	// An explicit command list, when configured.
	if explicit {
		for _, c := range set.Cmds.Commands {
			p.Steps = append(p.Steps, Step{Kind: StepRun, What: c, Why: "configured in worktree.post_create"})
		}
	}
	return p
}

// ignoredMatching asks git which of the patterns name real, ignored files.
//
// Using git rather than matching by hand means .gitignore semantics —
// negations, directory rules, nested ignore files — are exactly right, and
// that a *tracked* file matching a pattern is never returned. A tracked file
// is already in the new checkout on the correct branch; copying the source
// branch's version over it would be wrong.
func ignoredMatching(ctx context.Context, src string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	args := []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"}
	args = append(args, patterns...)
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
