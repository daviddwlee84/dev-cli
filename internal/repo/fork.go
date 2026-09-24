package repo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// ForkRequest either forks an existing checkout (Path) or forks and acquires a
// new one (CloneRef and Destination). Forking never pushes local commits.
type ForkRequest struct {
	Path         string
	CloneRef     string
	Destination  string
	SourceRemote string
	Config       config.Config
	Submodules   string
}

// ForkPlan is a read-only, source-bound review. Apply accepts only an unchanged
// plan returned in this process; serialized plans are display data, not authority.
type ForkPlan struct {
	Path         string         `json:"path"`
	Clone        bool           `json:"clone"`
	SourceRemote string         `json:"source_remote,omitempty"`
	SourceURL    string         `json:"source_url"`
	ForkURL      string         `json:"fork_url"`
	Forge        forge.ForkPlan `json:"forge"`
	Steps        []string       `json:"steps"`
	NoOp         bool           `json:"no_op"`
	authority    *forkAuthority
}

// ForkResult retains the exact successful phases even when a later phase fails.
// A created fork or checkout is never deleted to disguise a partial result.
type ForkResult struct {
	Plan        ForkPlan         `json:"plan"`
	Fork        forge.ForkResult `json:"fork"`
	Acquisition *AcquireResult   `json:"acquisition,omitempty"`
	Completed   []string         `json:"completed"`
	NoOp        bool             `json:"no_op"`
}

type forkAuthority struct {
	digest      [32]byte
	request     ForkRequest
	forgePlan   forge.ForkPlan
	checkout    forkCheckout
	destination forkDestination
	actions     []forkAction
}

type forkCheckout struct {
	repository gitx.Repo
	identity   string
	local      string
	effective  string
	values     map[string][]string
	localKeys  map[string][]string
	remotes    map[string]forkRemote
}

type forkRemote struct {
	url      string
	identity forge.RemoteIdentity
}

type forkDestination struct {
	path     string
	anchor   string
	identity string
	config   string
}

type forkAction struct {
	phase string
	args  []string
}

// PlanFork performs only local observations and forge reads. It checks local
// conflicts before contacting the forge, and before any fork can be created.
func PlanFork(ctx context.Context, adapter forge.Forge, request ForkRequest) (ForkPlan, error) {
	var plan ForkPlan
	if adapter == nil || adapter.Kind() != forge.GitHub {
		return plan, errors.New("repository forks currently require GitHub")
	}
	if err := request.Config.Submodules.Validate(); err != nil {
		return plan, err
	}
	if err := (config.Submodules{Init: request.Submodules}).Validate(); err != nil {
		return plan, err
	}
	request.Path = strings.TrimSpace(request.Path)
	request.CloneRef = strings.TrimSpace(request.CloneRef)
	request.SourceRemote = strings.TrimSpace(request.SourceRemote)
	request.Destination = strings.TrimSpace(request.Destination)
	clone := request.CloneRef != ""
	if clone && (request.Path != "" || request.SourceRemote != "") {
		return plan, errors.New("fork clone cannot also select a checkout or source remote")
	}
	if !clone && request.Destination != "" {
		return plan, errors.New("fork destination requires a clone reference")
	}
	authority := &forkAuthority{request: request}
	// Acquisition currently reads only this portion of Config. Copy its slice
	// so later caller changes cannot alter the reviewed submodule policy.
	authority.request.Config = config.Config{Submodules: config.Submodules{
		Init: request.Config.Submodules.Init, Develop: append([]string(nil), request.Config.Submodules.Develop...),
	}}
	plan.Clone = clone
	source := request.CloneRef
	if clone {
		destination, err := inspectForkDestination(ctx, request.Destination)
		if err != nil {
			return plan, err
		}
		authority.destination = destination
		plan.Path = destination.path
	} else {
		if request.Path == "" {
			request.Path = "."
			authority.request.Path = request.Path
		}
		checkout, err := inspectForkCheckout(ctx, request.Path)
		if err != nil {
			return plan, err
		}
		authority.checkout = checkout
		plan.Path = checkout.repository.Root
		selected, err := selectForkSource(checkout, request.SourceRemote)
		if err != nil {
			return plan, err
		}
		plan.SourceRemote = selected
		source = checkout.remotes[selected].url
	}
	remotePlan, err := forge.PlanFork(ctx, adapter, source)
	if err != nil {
		return plan, err
	}
	plan.Forge, authority.forgePlan = remotePlan, remotePlan
	plan.SourceURL = forkTransportURL(remotePlan.Source, source)
	plan.ForkURL = forkTransportURL(remotePlan.Fork, source)
	configValues := authority.checkout.values
	if clone {
		configValues = forkConfigValues(authority.destination.config)
	}
	if err := validateForkURLRewrites(configValues, plan.SourceURL, plan.ForkURL); err != nil {
		return plan, err
	}
	if clone {
		plan.Steps = []string{"clone source into " + plan.Path, "rename origin to upstream", "add personal fork as origin", "set remote.pushDefault=origin"}
	} else {
		actions, err := planForkRemotes(authority.checkout, plan)
		if err != nil {
			return plan, err
		}
		authority.actions = actions
		for _, action := range actions {
			plan.Steps = append(plan.Steps, action.phase)
		}
	}
	if remotePlan.Exists {
		plan.Steps = append([]string{"reuse verified personal fork"}, plan.Steps...)
	} else {
		plan.Steps = append([]string{"create personal fork"}, plan.Steps...)
	}
	plan.NoOp = !clone && remotePlan.Exists && len(authority.actions) == 0
	plan.authority = authority
	authority.digest = forkPlanDigest(plan)
	return plan, nil
}

// ApplyFork revalidates the checkout and native config under the shared Git
// lifecycle lock before writing externally or locally. Unknown forge outcomes
// stop here; the adapter may reconcile them with reads, never another create.
func ApplyFork(ctx context.Context, adapter forge.Forge, plan ForkPlan) (ForkResult, error) {
	return applyFork(ctx, adapter, plan, Acquire)
}

func applyFork(ctx context.Context, adapter forge.Forge, plan ForkPlan, acquire func(context.Context, AcquireRequest) (AcquireResult, error)) (ForkResult, error) {
	result := ForkResult{Plan: plan, Completed: []string{}}
	authority := plan.authority
	if authority == nil || authority.digest != forkPlanDigest(plan) {
		return result, errors.New("fork plan changed or was not created by PlanFork; preview again")
	}
	if adapter == nil || adapter.Kind() != forge.GitHub {
		return result, errors.New("repository forks currently require GitHub")
	}
	applyRemote := func() error {
		remote, err := forge.ApplyFork(ctx, adapter, authority.forgePlan)
		result.Fork = remote
		if remote.Created {
			result.Completed = append(result.Completed, "fork_created")
		} else if remote.Reused {
			result.Completed = append(result.Completed, "fork_reused")
		} else if remote.Plan.Exists && !remote.Unknown && err == nil {
			result.Completed = append(result.Completed, "fork_verified")
		}
		return err
	}
	var err error
	if plan.Clone {
		if err = revalidateForkDestination(ctx, authority.destination); err == nil {
			err = applyRemote()
		}
		if err == nil {
			err = revalidateForkDestination(ctx, authority.destination)
		}
		if err == nil {
			acquisition, acquireErr := acquire(ctx, AcquireRequest{
				Kind: AcquireClone, CloneRef: plan.SourceURL, Destination: plan.Path, CloneRemote: "origin",
				Config: authority.request.Config, Submodules: authority.request.Submodules,
			})
			result.Acquisition = &acquisition
			if acquisition.Cloned {
				result.Completed = append(result.Completed, "checkout_cloned")
			}
			err = acquireErr
		}
		if err == nil {
			var checkout forkCheckout
			checkout, err = inspectForkCheckout(ctx, plan.Path)
			if err == nil {
				// Acquire deliberately clones the source, so even an old personal
				// fork starts with the current upstream default branch.
				localPlan := plan
				localPlan.SourceRemote = "origin"
				var actions []forkAction
				actions, err = planForkRemotes(checkout, localPlan)
				if err == nil {
					err = gitx.WithLifecycleLock(ctx, checkout.repository.GitCommonDir, func() error {
						if err := revalidateForkCheckout(ctx, checkout); err != nil {
							return err
						}
						return applyForkActions(ctx, plan.Path, actions, &result)
					})
				}
			}
		}
	} else {
		err = gitx.WithLifecycleLock(ctx, authority.checkout.repository.GitCommonDir, func() error {
			if err := revalidateForkCheckout(ctx, authority.checkout); err != nil {
				return err
			}
			if err := applyRemote(); err != nil {
				return err
			}
			// Native Git writers do not share dev's lease. Detect changes during
			// the remote request before touching any local configuration.
			if err := revalidateForkCheckout(ctx, authority.checkout); err != nil {
				return err
			}
			return applyForkActions(ctx, plan.Path, authority.actions, &result)
		})
	}
	if err != nil {
		if len(result.Completed) > 0 {
			return result, fmt.Errorf("fork stopped after %s; completed work is retained: %w", strings.Join(result.Completed, ", "), err)
		}
		return result, err
	}
	result.NoOp = plan.NoOp
	return result, nil
}

func forkPlanDigest(plan ForkPlan) [32]byte {
	encoded, _ := json.Marshal(plan)
	return sha256.Sum256(encoded)
}

func inspectForkDestination(ctx context.Context, raw string) (forkDestination, error) {
	var destination forkDestination
	if raw == "" {
		return destination, errors.New("repository destination is required")
	}
	path, err := pathx.Canonical(raw)
	if err != nil {
		return destination, fmt.Errorf("resolve repository destination: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return destination, fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return destination, fmt.Errorf("inspect repository destination: %w", err)
	}
	if err := rejectNestedRepository(ctx, filepath.Dir(path)); err != nil {
		return destination, err
	}
	anchor := filepath.Dir(path)
	for {
		if _, err := os.Stat(anchor); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return destination, err
		}
		parent := filepath.Dir(anchor)
		if parent == anchor {
			return destination, errors.New("repository destination has no existing parent")
		}
		anchor = parent
	}
	identity, err := gitx.DirectoryIdentity(anchor)
	if err != nil {
		return destination, err
	}
	gitConfig, err := gitx.Run(ctx, anchor, "config", "--includes", "--null", "--list")
	if err != nil {
		return destination, err
	}
	if err := validateForkCloneConfig(forkConfigValues(gitConfig)); err != nil {
		return destination, err
	}
	return forkDestination{path: path, anchor: anchor, identity: identity, config: gitConfig}, nil
}

func validateForkCloneConfig(values map[string][]string) error {
	for key, entries := range values {
		if strings.HasPrefix(key, "remote.origin.") || strings.HasPrefix(key, "remote.upstream.") {
			return errors.New("fork clone would inherit origin or upstream configuration; configure those remotes only inside the checkout")
		}
		if strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".remote") && containsForkValue(entries, "origin") {
			return fmt.Errorf("%s would inherit a pull target that cannot be safely renamed; configure it inside the checkout", key)
		}
		if key == "remote.pushdefault" || strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".pushremote") {
			if len(entries) != 1 || entries[0] != "origin" && entries[0] != "upstream" {
				return fmt.Errorf("%s has an unrelated or ambiguous inherited push target; resolve it before forking", key)
			}
		}
	}
	return nil
}

func revalidateForkDestination(ctx context.Context, expected forkDestination) error {
	current, err := inspectForkDestination(ctx, expected.path)
	if err != nil {
		return err
	}
	identity, err := gitx.DirectoryIdentity(expected.anchor)
	if err != nil || identity != expected.identity || current.path != expected.path || current.config != expected.config {
		return errors.New("repository destination parent changed; preview again")
	}
	return nil
}

func inspectForkCheckout(ctx context.Context, path string) (forkCheckout, error) {
	var checkout forkCheckout
	repository, err := gitx.Discover(ctx, path)
	if err != nil {
		return checkout, err
	}
	if repository.Bare || repository.Root == "" {
		return checkout, errors.New("fork requires a working-tree checkout")
	}
	checkout.repository = repository
	checkout.identity, err = gitx.DirectoryIdentity(repository.GitCommonDir)
	if err != nil {
		return checkout, err
	}
	checkout.local, err = gitx.Run(ctx, repository.Root, "config", "--local", "--no-includes", "--null", "--list")
	if err != nil {
		return checkout, err
	}
	checkout.effective, err = gitx.Run(ctx, repository.Root, "config", "--includes", "--null", "--list")
	if err != nil {
		return checkout, err
	}
	checkout.values = forkConfigValues(checkout.effective)
	checkout.localKeys = forkConfigValues(checkout.local)
	checkout.remotes = map[string]forkRemote{}
	for key := range checkout.values {
		if !strings.HasPrefix(key, "remote.") {
			continue
		}
		subkey := strings.TrimPrefix(key, "remote.")
		end := strings.LastIndex(subkey, ".")
		if end < 1 {
			continue
		}
		name := subkey[:end]
		// Keep all remote names to detect collisions. Detailed safety checks
		// apply to the remotes that would be used or changed, not unrelated ones.
		remote := forkRemote{}
		values := checkout.values["remote."+name+".url"]
		if len(values) == 1 {
			remote.url = values[0]
			remote.identity = forge.ParseRemoteIdentity(values[0])
		}
		checkout.remotes[name] = remote
	}
	return checkout, nil
}

func forkConfigValues(raw string) map[string][]string {
	values := map[string][]string{}
	for _, record := range strings.Split(raw, "\x00") {
		if record == "" {
			continue
		}
		key, value, _ := strings.Cut(record, "\n")
		values[key] = append(values[key], value)
	}
	return values
}

func selectForkSource(checkout forkCheckout, requested string) (string, error) {
	selected := requested
	if selected == "" {
		if _, ok := checkout.remotes["upstream"]; ok {
			selected = "upstream"
		} else if _, ok := checkout.remotes["origin"]; ok {
			selected = "origin"
		} else {
			return "", errors.New("cannot infer fork source; select an existing GitHub remote with --source-remote")
		}
	}
	if _, ok := checkout.remotes[selected]; !ok {
		return "", fmt.Errorf("source remote %q does not exist", selected)
	}
	if err := validateForkRemote(checkout, selected); err != nil {
		return "", err
	}
	if checkout.remotes[selected].identity.Kind != forge.GitHub {
		return "", fmt.Errorf("source remote %q is not a supported GitHub repository", selected)
	}
	return selected, nil
}

func validateForkRemote(checkout forkCheckout, name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\n\r") {
		return fmt.Errorf("unsupported remote name %q", name)
	}
	prefix := "remote." + name + "."
	urls := checkout.values[prefix+"url"]
	if len(urls) != 1 || urls[0] == "" {
		return fmt.Errorf("remote %q requires exactly one URL", name)
	}
	for key, values := range checkout.values {
		if strings.HasPrefix(key, prefix) && !reflect.DeepEqual(values, checkout.localKeys[key]) {
			return fmt.Errorf("remote %q uses inherited configuration; configure it locally before forking", name)
		}
	}
	pushURLs := checkout.values[prefix+"pushurl"]
	if len(pushURLs) > 1 {
		return fmt.Errorf("remote %q has multiple push URLs", name)
	}
	if len(pushURLs) == 1 && !sameForkIdentity(forge.ParseRemoteIdentity(urls[0]), forge.ParseRemoteIdentity(pushURLs[0])) {
		return fmt.Errorf("remote %q has a push URL for a different repository", name)
	}
	wantFetch := "+refs/heads/*:refs/remotes/" + name + "/*"
	if fetch := checkout.values[prefix+"fetch"]; len(fetch) != 1 || fetch[0] != wantFetch {
		return fmt.Errorf("remote %q has an unsupported fetch refspec", name)
	}
	if len(checkout.values[prefix+"push"]) > 0 || len(checkout.values[prefix+"mirror"]) > 0 {
		return fmt.Errorf("remote %q has custom push refspecs or mirror settings", name)
	}
	return nil
}

func planForkRemotes(checkout forkCheckout, plan ForkPlan) ([]forkAction, error) {
	source := forkRepositoryIdentity(plan.Forge.Source)
	personal := forkRepositoryIdentity(plan.Forge.Fork)
	if source.Kind != forge.GitHub || personal.Kind != forge.GitHub || sameForkIdentity(source, personal) {
		return nil, errors.New("fork plan does not identify distinct GitHub source and personal repositories")
	}
	for _, name := range []string{plan.SourceRemote, "origin", "upstream"} {
		if _, exists := checkout.remotes[name]; exists {
			if err := validateForkRemote(checkout, name); err != nil {
				return nil, err
			}
			urls := append([]string(nil), checkout.values["remote."+name+".url"]...)
			urls = append(urls, checkout.values["remote."+name+".pushurl"]...)
			if err := validateForkURLRewrites(checkout.values, urls...); err != nil {
				return nil, err
			}
		}
	}
	selected, selectedExists := checkout.remotes[plan.SourceRemote]
	if !selectedExists || (!sameForkIdentity(selected.identity, source) && !sameForkIdentity(selected.identity, personal)) {
		return nil, errors.New("selected remote no longer identifies the verified source or personal fork")
	}
	upstream, hasUpstream := checkout.remotes["upstream"]
	if hasUpstream && !sameForkIdentity(upstream.identity, source) {
		return nil, errors.New("upstream remote conflicts with the verified source repository")
	}
	origin, hasOrigin := checkout.remotes["origin"]
	originSource := hasOrigin && sameForkIdentity(origin.identity, source)
	if hasOrigin && !originSource && !sameForkIdentity(origin.identity, personal) {
		return nil, errors.New("origin remote conflicts with the verified personal fork")
	}
	if hasUpstream && originSource {
		return nil, errors.New("origin and upstream both identify the source; resolve the duplicate before forking so branch pull targets are preserved")
	}
	var actions []forkAction
	rename := ""
	if !hasUpstream {
		switch {
		case originSource:
			rename = "origin"
			hasOrigin = false
		case sameForkIdentity(selected.identity, source):
			rename = plan.SourceRemote
		default:
			actions = append(actions, forkAction{"remote_added:upstream", []string{"remote", "add", "upstream", plan.SourceURL}})
		}
		if rename != "" {
			// git remote rename rewrites branch.<name>.remote while keeping
			// merge refs, refs/remotes and the actual pull repository intact.
			for key, values := range checkout.values {
				if strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".remote") && containsForkValue(values, rename) && !reflect.DeepEqual(values, checkout.localKeys[key]) {
					return nil, fmt.Errorf("%s uses inherited pull configuration; configure it locally before renaming %s", key, rename)
				}
			}
			actions = append(actions, forkAction{"remote_renamed:" + rename + ":upstream", []string{"remote", "rename", rename, "upstream"}})
		}
	}
	if !hasOrigin {
		actions = append(actions, forkAction{"remote_added:origin", []string{"remote", "add", "origin", plan.ForkURL}})
	}
	keys := []string{"remote.pushdefault"}
	for key := range checkout.values {
		if strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".pushremote") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := checkout.values[key]
		local := checkout.localKeys[key]
		if len(local) > 1 || len(local) == 0 && len(values) > 1 {
			return nil, fmt.Errorf("%s has multiple values; resolve them before forking", key)
		}
		if len(values) > 0 {
			// Scalar local config overrides global defaults. In particular,
			// adding our local pushDefault must not make the next run fail.
			name := values[len(values)-1]
			if len(local) == 1 && local[0] != name {
				return nil, fmt.Errorf("%s is overridden outside repository-local config; resolve it before forking", key)
			}
			remote, ok := checkout.remotes[name]
			// Both canonical names have verified planned identities, including
			// an upstream that will only exist after the source remote rename.
			plannedRemote := (name == "origin" || name == "upstream") && !ok
			allowed := plannedRemote || (name == plan.SourceRemote || name == "upstream" || name == "origin") && ok && (sameForkIdentity(remote.identity, source) || sameForkIdentity(remote.identity, personal))
			if !allowed {
				return nil, fmt.Errorf("%s explicitly targets unrelated remote %q; resolve it before forking", key, name)
			}
			if name == "origin" && rename != "origin" && reflect.DeepEqual(checkout.localKeys[key], []string{"origin"}) {
				continue
			}
		}
		actions = append(actions, forkAction{"config_set:" + key + "=origin", []string{"config", "--local", "--replace-all", key, "origin"}})
	}
	return actions, nil
}

func revalidateForkCheckout(ctx context.Context, expected forkCheckout) error {
	current, err := inspectForkCheckout(ctx, expected.repository.Root)
	if err != nil {
		return err
	}
	if current.repository != expected.repository || current.identity != expected.identity || current.local != expected.local || current.effective != expected.effective {
		return errors.New("repository identity or Git configuration changed since fork preview; preview again")
	}
	return nil
}

func validateForkURLRewrites(values map[string][]string, urls ...string) error {
	// Refuse any applicable rewrite to a different repository. This deliberately
	// also refuses conflicting shorter-prefix rules: accepting only unambiguous
	// same-identity transport rewrites keeps both future remotes reviewable.
	for key, prefixes := range values {
		if !strings.HasPrefix(key, "url.") {
			continue
		}
		suffix := ".insteadof"
		if strings.HasSuffix(key, ".pushinsteadof") {
			suffix = ".pushinsteadof"
		} else if !strings.HasSuffix(key, suffix) {
			continue
		}
		base := strings.TrimSuffix(strings.TrimPrefix(key, "url."), suffix)
		for _, prefix := range prefixes {
			for _, raw := range urls {
				if strings.HasPrefix(raw, prefix) && !sameForkIdentity(forge.ParseRemoteIdentity(raw), forge.ParseRemoteIdentity(base+strings.TrimPrefix(raw, prefix))) {
					return errors.New("Git URL rewriting changes a source or personal fork repository; resolve it before forking")
				}
			}
		}
	}
	return nil
}

func applyForkActions(ctx context.Context, path string, actions []forkAction, result *ForkResult) error {
	for _, action := range actions {
		if _, err := gitx.Run(ctx, path, action.args...); err != nil {
			return fmt.Errorf("%s: %w", action.phase, err)
		}
		result.Completed = append(result.Completed, action.phase)
	}
	return nil
}

func forkTransportURL(repository forge.ForkRepository, reference string) string {
	if strings.HasPrefix(reference, "git@") || strings.HasPrefix(reference, "ssh://") {
		return repository.SSHURL
	}
	return repository.CloneURL
}

func forkRepositoryIdentity(repository forge.ForkRepository) forge.RemoteIdentity {
	return forge.RemoteIdentity{Kind: forge.GitHub, Host: repository.Host, Name: repository.FullName}
}

func sameForkIdentity(a, b forge.RemoteIdentity) bool {
	return a.Kind == forge.GitHub && b.Kind == forge.GitHub && a.Host != "" && a.Name != "" && strings.EqualFold(a.Host, b.Host) && strings.EqualFold(a.Name, b.Name)
}

func containsForkValue(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
