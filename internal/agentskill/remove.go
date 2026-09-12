package agentskill

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/skill"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type removalPath struct {
	Path, Fingerprint string
	Remove            bool
}
type removalState struct {
	Paths        []removalPath
	Dependencies string
	Bundle       *skill.UninstallPlan
}

// PrepareRemovals selects installed, verified copies, never a local source
// repository. Explicit agent scopes preserve shared consumers or fail closed
// when those consumers use the same logical directory.
func PrepareRemovals(ctx context.Context, rows []Skill, agents []string) []ManageOperation {
	agents = append([]string(nil), agents...)
	sort.Strings(agents)
	agents = slices.Compact(agents)
	validAgents := map[string]bool{}
	for _, agent := range Registry() {
		validAgents[agent.ID] = true
	}
	valid := len(agents) > 0
	for _, agent := range agents {
		valid = valid && validAgents[agent]
	}
	var ops []ManageOperation
	groups := map[string]int{}
	for _, row := range rows {
		name := row.Name
		if row.Lock != nil {
			name = row.Lock.Name
		}
		op := ManageOperation{Root: row.ScopeRoot, Scope: row.Scope, Action: "remove", Names: []string{name}, Agents: agents, rows: []Skill{row}}
		if !valid {
			op.Blocked = "select supported agent scopes explicitly"
		}
		if name == "agent-history-hygiene" {
			op.Blocked = "retained: dev artifact finalize still depends on agent-history-hygiene scripts; this migration replaces scanner setup only"
		}
		if op.Blocked == "" && row.ManagedBy == ManagedByDev && row.Lock != nil {
			op.Blocked = "bundled installation also has a native lock claim; reconcile ownership before batch removal"
		}
		if op.Blocked == "" && row.ManagedBy == ManagedByDev {
			plan, err := skill.PlanUninstall(row.Path)
			if err != nil {
				op.Blocked = err.Error()
			} else {
				for _, installation := range row.Installations {
					for _, agent := range installation.AgentIDs {
						if !slices.Contains(agents, agent) {
							op.Blocked = "bundled removal requires every shared installation consumer"
						}
					}
					for _, path := range installation.LogicalPaths {
						if path != plan.Dir && !slices.Contains(plan.Links, path) {
							op.Blocked = "bundled installation has an unowned agent link; inspect it separately"
						}
					}
				}
				op.removal = &removalState{Bundle: &plan}
				op.seal = op.authorization()
			}
			ops = append(ops, op)
			continue
		}
		if op.Blocked == "" {
			if row.Lock == nil || row.ManagedBy != ManagedBySkills || row.Presence != PresencePresent || !exactRemovalName(name) {
				op.Blocked = "removal requires an installed, unambiguous native-lock-managed skill"
			} else if _, err := installedFingerprint(ctx, row, true); err != nil {
				op.Blocked = err.Error()
			}
		}
		if op.Blocked == "" {
			paths, err := removalPaths(ctx, row, agents)
			if err != nil {
				op.Blocked = err.Error()
			} else {
				op.removal = &removalState{Paths: paths}
			}
		}
		if op.Blocked != "" {
			ops = append(ops, op)
			continue
		}
		key := op.Root + "\x00" + string(op.Scope)
		if index, found := groups[key]; found {
			if !slices.Contains(ops[index].Names, name) {
				ops[index].Names = append(ops[index].Names, name)
				ops[index].rows = append(ops[index].rows, row)
				ops[index].removal.Paths = append(ops[index].removal.Paths, op.removal.Paths...)
			}
		} else {
			groups[key] = len(ops)
			ops = append(ops, op)
		}
	}
	for i := range ops {
		op := &ops[i]
		if op.Blocked != "" || op.removal == nil || op.removal.Bundle != nil {
			continue
		}
		dependencies, err := removalDependencies(ctx, *op)
		if err != nil {
			op.Blocked = err.Error()
			continue
		}
		op.removal.Dependencies = dependencies
		sealOperation(ctx, op)
	}
	return ops
}

// Native skills sanitizes lock keys before filesystem removal. Only pass names
// whose supported ASCII spelling is already its exact canonical folder name.
func exactRemovalName(name string) bool {
	if len(name) > 255 || !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`).MatchString(name) {
		return false
	}
	normalized := regexp.MustCompile(`[^a-z0-9._]+`).ReplaceAllString(name, "-")
	return strings.Trim(normalized, ".-") == name
}

func removalPaths(ctx context.Context, row Skill, agents []string) ([]removalPath, error) {
	root := row.ScopeRoot
	if row.Scope == ScopeGlobal {
		root = homeDirectory()
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	canonical := filepath.Join(canonicalRoot, ".agents", "skills", row.Lock.Name)
	known := map[string][]string{}
	for _, agent := range Registry() {
		base := agent.GlobalSkillsDir
		if row.Scope == ScopeProject {
			base = filepath.Join(canonicalRoot, agent.ProjectSkillsDir)
			if agent.ProjectSkillsDir == "" {
				continue
			}
		}
		if base != "" {
			path := filepath.Join(base, row.Lock.Name)
			known[path] = append(known[path], agent.ID)
		}
	}
	if row.Scope == ScopeGlobal {
		known[canonical] = append(known[canonical], "universal", "codex")
	}
	paths := map[string]bool{}
	for _, installation := range row.Installations {
		for _, path := range installation.LogicalPaths {
			paths[path] = true
		}
		if installation.Path != "" {
			paths[installation.Path] = true
		}
	}
	var result []removalPath
	removes := 0
	for path := range paths {
		owners := known[path]
		if len(owners) == 0 {
			return nil, errors.New("skill destination has unknown ownership")
		}
		selected, retained := false, false
		for _, agent := range owners {
			if slices.Contains(agents, agent) {
				selected = true
			} else {
				retained = true
			}
		}
		if selected && retained {
			return nil, errors.New("selected and retained agents share one skill directory; select all shared consumers or a distinct agent link")
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || real != path && real != canonical {
			return nil, errors.New("skill link or ancestor resolves outside the owned installation")
		}
		if path == canonical && real != canonical {
			return nil, errors.New("canonical installation points to an external source")
		}
		if row.Lock.SourceType == "local" {
			source := row.Lock.Source
			if !filepath.IsAbs(source) {
				source = filepath.Join(root, source)
			}
			if source, err := filepath.EvalSymlinks(source); err == nil && (source == real || strings.HasPrefix(real, source+string(filepath.Separator))) {
				return nil, errors.New("installation is also the local skill source; preserve source files")
			}
		}
		fingerprint, err := removalPathFingerprint(ctx, path)
		if err != nil {
			return nil, err
		}
		result = append(result, removalPath{path, fingerprint, selected})
		if selected {
			removes++
		}
	}
	if removes == 0 {
		return nil, errors.New("selected agents have no removable installation")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func removalPathFingerprint(ctx context.Context, path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	id, err := gitx.DirectoryIdentity(real)
	if err != nil {
		return "", err
	}
	hashes, err := installedHashes(ctx, real)
	if err != nil {
		return "", err
	}
	link, _ := os.Readlink(path)
	value := sha256.Sum256([]byte(real + "\x00" + id + "\x00" + link + "\x00" + hashes["snapshot"]))
	return fmt.Sprintf("%x", value), nil
}

func removalDependencies(ctx context.Context, op ManageOperation) (string, error) {
	paths := []string{filepath.Join(op.Root, ".pre-commit-config.yaml"), filepath.Join(op.Root, "Makefile"), filepath.Join(op.Root, "justfile"), filepath.Join(op.Root, "package.json")}
	if op.Scope == ScopeProject {
		repo, err := gitx.Discover(ctx, op.Root)
		if err != nil {
			return "", err
		}
		hooks := filepath.Join(repo.GitCommonDir, "hooks")
		if value, err := gitx.Run(ctx, op.Root, "config", "--path", "--get", "core.hooksPath"); err == nil && value != "" {
			hooks = value
			if !filepath.IsAbs(hooks) {
				hooks = filepath.Join(op.Root, hooks)
			}
		}
		for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit"} {
			paths = append(paths, filepath.Join(hooks, name))
		}
	} else if hooks, err := gitx.Run(ctx, op.Root, "config", "--global", "--path", "--get", "core.hooksPath"); err == nil && hooks != "" {
		if !filepath.IsAbs(hooks) {
			return "", errors.New("relative global hook path needs repository-specific dependency review")
		}
		for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit"} {
			paths = append(paths, filepath.Join(hooks, name))
		}
	}
	h := sha256.New()
	for _, path := range paths {
		data, err := safefile.ReadStablePath(ctx, path, 1<<20)
		if os.IsNotExist(err) {
			fmt.Fprintln(h, path, "absent")
			continue
		}
		// Git's internal hook paths require their own safe read boundary.
		if err != nil && strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			data, err = safefile.ReadRegular(ctx, path, 1<<20)
		}
		if os.IsNotExist(err) {
			fmt.Fprintln(h, path, "absent")
			continue
		}
		if err != nil {
			return "", errors.New("skill dependency files could not be safely inspected")
		}
		for _, name := range op.Names {
			if strings.Contains(string(data), "skills/"+name) || strings.Contains(string(data), "skills\\"+name) {
				return "", errors.New("selected skill is referenced by repository hooks or development commands")
			}
		}
		fmt.Fprintln(h, path)
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func applyRemoval(ctx context.Context, op ManageOperation, out io.Writer) []ManageOutcome {
	record := func(status, detail string) []ManageOutcome {
		result := []ManageOutcome{}
		for _, name := range op.Names {
			result = append(result, ManageOutcome{op.Root, op.Scope, name, "remove", status, gitx.SafeDiagnosticText(detail)})
		}
		return result
	}
	if ctx.Err() != nil {
		return record("canceled", "not started")
	}
	if op.Blocked != "" {
		return record("skipped", op.Blocked)
	}
	if op.removal == nil || op.seal == "" || op.seal != op.authorization() {
		return record("stale", "removal differs from its preview")
	}
	if op.removal.Bundle != nil {
		if err := skill.ApplyUninstall(*op.removal.Bundle); err != nil {
			return record("failed", err.Error())
		}
		return record("completed", "bundled files and owned links removed")
	}
	args := append([]string{"remove"}, op.Names...)
	if op.Scope == ScopeGlobal {
		args = append(args, "--global")
	}
	args = append(args, "--yes", "--agent")
	args = append(args, op.Agents...)
	p := provider{bin: op.provider}
	mutation := &MutationCommand{Command: p.command(ctx, op.Root, args...), ctx: ctx}
	command, finish, err := mutation.Prepare()
	if err != nil {
		return record("failed", err.Error())
	}
	defer finish(nil)
	before, err := scopeInventory(ctx, op.Root, op.Scope)
	fingerprint, e := scopeFingerprint(ctx, op.Root, op.Scope, before.Skills)
	providerHash, hashErr := fileHashContext(ctx, op.provider)
	dependencies, depErr := removalDependencies(ctx, op)
	if err != nil || len(before.Diagnostics) > 0 || e != nil || fingerprint != op.fingerprint || hashErr != nil || providerHash != op.providerHash || depErr != nil || dependencies != op.removal.Dependencies {
		return record("stale", "scope, provider or dependency changed")
	}
	for _, path := range op.removal.Paths {
		got, e := removalPathFingerprint(ctx, path.Path)
		if e != nil || got != path.Fingerprint {
			return record("stale", "installation identity or contents changed")
		}
	}
	preservedHashes := map[string]string{}
	for _, row := range before.Skills {
		if row.Lock != nil && !slices.Contains(op.Names, row.Lock.Name) {
			value, err := installedFingerprint(ctx, row, false)
			if err != nil {
				return record("stale", "unrelated installation could not be inspected")
			}
			preservedHashes[row.Lock.Name] = value
		}
	}
	// Use the existing bounded process-tree runner so cancellation of a Windows
	// npm shim also closes its own descendants, while the provider lease is held.
	if command.Err != nil {
		return record("failed", command.Err.Error())
	}
	result, runErr := (sshhost.ExecRunner{}).Run(ctx, sshhost.RunRequest{Name: command.Path, Args: command.Args[1:], Dir: command.Dir, Env: managementEnvironment(op.Root, op.Scope), Display: "selected skill removal"})
	if runErr == nil && result.ExitCode != 0 {
		runErr = errors.New("skill provider failed")
	}
	after, readErr := scopeInventory(context.WithoutCancel(ctx), op.Root, op.Scope)
	verified := readErr == nil && len(after.Diagnostics) == 0
	for _, path := range op.removal.Paths {
		if path.Remove {
			_, e := os.Lstat(path.Path)
			verified = verified && os.IsNotExist(e)
		} else {
			got, e := removalPathFingerprint(context.WithoutCancel(ctx), path.Path)
			verified = verified && e == nil && got == path.Fingerprint
		}
	}
	for _, row := range before.Skills {
		if row.Lock == nil {
			continue
		}
		selected := slices.Contains(op.Names, row.Lock.Name)
		preserved := false
		for _, path := range op.removal.Paths {
			if !path.Remove && filepath.Base(path.Path) == row.Lock.Name {
				preserved = true
			}
		}
		var current *Skill
		for i := range after.Skills {
			if after.Skills[i].Lock != nil && after.Skills[i].Lock.Name == row.Lock.Name {
				current = &after.Skills[i]
				break
			}
		}
		if selected && !preserved {
			verified = verified && current == nil
			continue
		}
		if current == nil {
			verified = false
			continue
		}
		oldLock, _ := json.Marshal(row.Lock)
		newLock, _ := json.Marshal(current.Lock)
		verified = verified && string(oldLock) == string(newLock)
		if !selected {
			new, e := installedFingerprint(context.WithoutCancel(ctx), *current, false)
			verified = verified && e == nil && preservedHashes[row.Lock.Name] == new
		}
	}
	if runErr != nil {
		return record("partial", "provider failed or was canceled; inspect observed installations before retrying")
	}
	if !verified {
		return record("unverified", "provider exited but selected removal/shared-consumer postconditions were not proven")
	}
	return record("completed", "selected installations removed; retained consumers and unrelated locks verified")
}
