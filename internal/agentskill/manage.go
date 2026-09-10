package agentskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

type ManageOperation struct {
	Root         string
	Scope        Scope
	Action       string
	Names        []string
	Agents       []string
	Blocked      string
	rows         []Skill
	fingerprint  string
	provider     string
	providerHash string
	seal         string
	nativeHash   string
	dependencies bool
}
type ManageOutcome struct {
	Root   string `json:"root"`
	Scope  Scope  `json:"scope"`
	Skill  string `json:"skill"`
	Action string `json:"action"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type ManageReceipt struct {
	Version  int             `json:"schema_version"`
	ID       string          `json:"id"`
	At       time.Time       `json:"at"`
	Outcomes []ManageOutcome `json:"outcomes"`
}

func (op ManageOperation) CommandLabel() string {
	args := []string{"skills", op.Action}
	if op.Action == "update" {
		args = append(args, "--"+string(op.Scope), "--yes")
		args = append(args, op.Names...)
	}
	if op.Action == "experimental_sync" {
		args = append(args, "--yes", "--agent")
		args = append(args, op.Agents...)
	}
	return strings.Join(args, " ")
}

func scopeInventory(ctx context.Context, root string, scope Scope) (Result, error) {
	if scope == ScopeGlobal {
		return Scan(ctx, nil, ListOptions{Global: true})
	}
	target, err := agenttarget.Current(ctx, root)
	if err != nil {
		return Result{}, err
	}
	actual, actualErr := pathx.Canonical(target.CheckoutRoot)
	expected, expectedErr := pathx.Canonical(root)
	if actualErr != nil || expectedErr != nil || actual != expected {
		return Result{}, errors.New("checkout root changed")
	}
	target.CheckoutRoot = root
	return Scan(ctx, []agenttarget.Target{target}, ListOptions{Project: true})
}
func scopeFingerprint(ctx context.Context, root string, scope Scope, rows []Skill) (string, error) {
	lock := GlobalLockPath()
	if scope == ScopeProject {
		lock = filepath.Join(root, "skills-lock.json")
	}
	data, err := safefile.ReadRegular(ctx, lock, maxLockBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	h := sha256.New()
	h.Write(data)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(h, real)
	for _, row := range rows {
		if row.Lock == nil {
			continue
		}
		fingerprint, err := installedFingerprint(ctx, row, false)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", row.Lock.Name, fingerprint)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// PrepareUpdates only selects checked, present, unchanged provider-owned skills.
// The scope lock and selected installed bytes are revalidated under the mutation
// lease before any provider command runs.
func PrepareUpdates(ctx context.Context, rows []Skill) []ManageOperation {
	var ops []ManageOperation
	groups := map[string]int{}
	for _, row := range rows {
		name := row.Name
		if row.Lock != nil {
			name = row.Lock.Name
		}
		op := ManageOperation{Root: row.ScopeRoot, Scope: row.Scope, Action: "update", Names: []string{name}, rows: []Skill{row}}
		switch {
		case !CanUpdate(row):
			op.Blocked = "not a supported lock-managed skill"
		case row.Presence == PresenceMissing:
			op.Blocked = "installation missing; use Restore from lock"
		case row.UpdateStatus != UpdateAvailable:
			op.Blocked = "no confirmed update (" + string(row.UpdateStatus) + ")"
		default:
			_, err := installedFingerprint(ctx, row, true)
			if err != nil {
				op.Blocked = err.Error()
			}
		}
		if op.Blocked != "" {
			ops = append(ops, op)
			continue
		}
		key := op.Root + "\x00" + string(op.Scope)
		if i, ok := groups[key]; ok {
			ops[i].Names = append(ops[i].Names, name)
			ops[i].rows = append(ops[i].rows, row)
		} else {
			groups[key] = len(ops)
			ops = append(ops, op)
		}
	}
	for i := range ops {
		if ops[i].Blocked == "" {
			sealOperation(ctx, &ops[i])
		}
	}
	return ops
}

func sealOperation(ctx context.Context, op *ManageOperation) {
	providerRoot := op.Root
	if op.Scope == ScopeGlobal {
		providerRoot = ""
	}
	provider := MutationProviderStatusFor(providerRoot)
	if !provider.Available {
		op.Blocked = provider.Detail
		return
	}
	version, features, err := ProviderFeatures(ctx, op.Root, op.Scope)
	if err != nil {
		op.Blocked = err.Error()
		return
	}
	parts := strings.Split(strings.TrimPrefix(version, "skills "), ".")
	major, minor, patch := 0, 0, 0
	if len(parts) == 3 {
		major, _ = strconv.Atoi(parts[0])
		minor, _ = strconv.Atoi(parts[1])
		patch, _ = strconv.Atoi(parts[2])
	}
	if major != 1 || minor < 5 || minor == 5 && patch < 23 || !features[op.Action] {
		op.Blocked = "skills 1.5.23 or newer compatible 1.x is required; update the global skills dependency"
		return
	}
	op.provider = provider.Path
	hash, err := fileHashContext(ctx, provider.Path)
	if err != nil {
		op.Blocked = "could not verify skills executable"
		return
	}
	op.providerHash = hash
	inventory, err := scopeInventory(ctx, op.Root, op.Scope)
	if err != nil || len(inventory.Diagnostics) > 0 {
		op.Blocked = "scope inventory is incomplete; inspect lock or installation diagnostics"
		if err != nil {
			op.Blocked += ": " + err.Error()
		} else if len(inventory.Diagnostics) > 0 {
			op.Blocked += ": " + inventory.Diagnostics[0].Message
		}
		return
	}
	// Use current rows, but reject drift from the rows shown by the caller.
	for _, shown := range op.rows {
		if shown.Lock == nil {
			continue
		}
		found := false
		for _, live := range inventory.Skills {
			if live.Lock == nil || live.Lock.Name != shown.Lock.Name {
				continue
			}
			left, e1 := installedFingerprint(ctx, shown, false)
			right, e2 := installedFingerprint(ctx, live, false)
			a, _ := json.Marshal(shown.Lock)
			b, _ := json.Marshal(live.Lock)
			if e1 != nil || e2 != nil || left != right || !bytes.Equal(a, b) {
				op.Blocked = "skill changed while preparing the operation"
				return
			}
			found = true
			break
		}
		if !found {
			op.Blocked = "selected skill is no longer in its lock"
			return
		}
	}
	op.fingerprint, err = scopeFingerprint(ctx, op.Root, op.Scope, inventory.Skills)
	if err != nil {
		op.Blocked = err.Error()
	}
	op.seal = op.authorization()
}

// ProviderFeatures is an explicit management probe, never an inventory read.
func ProviderFeatures(ctx context.Context, root string, scope ...Scope) (string, map[string]bool, error) {
	trustedRoot := root
	if len(scope) > 0 && scope[0] == ScopeGlobal {
		trustedRoot = ""
	}
	status := MutationProviderStatusFor(trustedRoot)
	if !status.Available {
		return "", nil, errors.New(status.Detail)
	}
	provider := provider{bin: status.Path}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := provider.command(ctx, root, "--version").Output()
	if err != nil {
		return "", nil, err
	}
	version := "skills " + strings.TrimSpace(string(raw))
	output, err := provider.command(ctx, root, "--help").Output()
	if err != nil {
		return version, nil, err
	}
	features := map[string]bool{}
	for _, name := range []string{"update", "experimental_install", "experimental_sync"} {
		features[name] = strings.Contains(string(output), name)
	}
	return version, features, nil
}

func PrepareNative(ctx context.Context, root, action string, agents []string) ManageOperation {
	op := ManageOperation{Root: root, Scope: ScopeProject, Action: action, Agents: append([]string(nil), agents...)}
	if action != "experimental_install" && action != "experimental_sync" {
		op.Blocked = "unsupported native operation"
		return op
	}
	_, features, err := ProviderFeatures(ctx, root)
	if err != nil {
		op.Blocked = err.Error()
		return op
	}
	if !features[action] {
		op.Blocked = "installed skills version does not support " + action
		return op
	}
	inv, err := scopeInventory(ctx, root, ScopeProject)
	if err != nil || len(inv.Diagnostics) > 0 {
		op.Blocked = "project inventory is incomplete"
		return op
	}
	document := readProjectLock(filepath.Join(root, "skills-lock.json"))
	if action == "experimental_install" {
		for _, entry := range document.Entries {
			if entry.SourceType == "node_modules" {
				continue
			}
			if source, ok := sourceURL(entry); !ok || strings.HasPrefix(source, "-") || !safeDisplayValue(source) || !safeFetchRef(entry.Ref) || entry.PluginName != "" || len(entry.Subagents) > 0 {
				op.Blocked = "lock source requires its native installer or individual inspection"
				return op
			}
		}
	}
	if action == "experimental_install" && len(document.Entries) == 0 {
		op.Blocked = "no project skills recorded in skills-lock.json"
		return op
	}
	if action == "experimental_sync" {
		if info, err := os.Stat(filepath.Join(root, "node_modules")); err != nil || !info.IsDir() {
			op.Blocked = "node_modules is missing; install project dependencies first"
			return op
		}
		if len(agents) == 0 {
			op.Blocked = "choose at least one project agent"
			return op
		}
		valid := map[string]bool{}
		for _, agent := range Registry() {
			valid[agent.ID] = agent.ProjectSkillsDir != ""
		}
		for _, agent := range agents {
			if !valid[agent] {
				op.Blocked = "unknown project agent"
				return op
			}
		}
	}
	for _, row := range inv.Skills {
		if row.Lock == nil {
			continue
		}
		if directManagedSkillName(row.Lock.Name) {
			op.Blocked = "dev-cli is managed by the dev binary"
			return op
		}
		if action == "experimental_install" || row.Lock.SourceType == "node_modules" {
			if row.Presence == PresencePresent {
				if _, err := installedFingerprint(ctx, row, true); err != nil {
					op.Blocked = err.Error()
					return op
				}
			}
			op.Names = append(op.Names, row.Lock.Name)
			op.rows = append(op.rows, row)
		}
	}
	op.dependencies = action == "experimental_sync"
	for _, entry := range document.Entries {
		if entry.SourceType == "node_modules" {
			op.dependencies = true
		}
	}
	nativeHash, names, err := nativeInputs(ctx, root, op.dependencies)
	if err != nil {
		op.Blocked = err.Error()
		return op
	}
	op.nativeHash = nativeHash
	if op.dependencies {
		for _, name := range names {
			for _, row := range inv.Skills {
				if row.Name == name && (row.Lock == nil || row.Lock.SourceType != "node_modules") {
					op.Blocked = "dependency skill conflicts with existing " + name
					return op
				}
			}
			if !slices.Contains(op.Names, name) {
				op.Names = append(op.Names, name)
			}
		}
		if action == "experimental_sync" && len(names) == 0 {
			op.Blocked = "no dependency skills found in node_modules"
			return op
		}
	}
	sealOperation(ctx, &op)
	return op
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := 64*1024 - b.Len(); left > 0 {
		b.Buffer.Write(p[:min(left, len(p))])
	}
	return n, nil
}

func ApplyManagement(ctx context.Context, ops []ManageOperation, out io.Writer) ManageReceipt {
	receipt := ManageReceipt{Version: 1, ID: uuid.NewString(), At: time.Now().UTC(), Outcomes: []ManageOutcome{}}
	for _, op := range ops {
		names := op.Names
		if len(names) == 0 {
			names = []string{"dependency skills"}
		}
		record := func(status, detail string) {
			for _, name := range names {
				receipt.Outcomes = append(receipt.Outcomes, ManageOutcome{op.Root, op.Scope, name, op.Action, status, gitx.SafeDiagnosticText(detail)})
			}
		}
		if ctx.Err() != nil {
			record("canceled", "not started")
			continue
		}
		if op.Blocked == "" && (op.seal == "" || op.seal != op.authorization()) {
			record("stale", "operation differs from its preview")
			continue
		}
		if op.Blocked != "" {
			record("skipped", op.Blocked)
			continue
		}
		args := []string{op.Action}
		if op.Action == "update" {
			args = append(args, "--"+string(op.Scope), "--yes")
			args = append(args, op.Names...)
		}
		if op.Action == "experimental_sync" {
			args = append(args, "--yes", "--agent")
			args = append(args, op.Agents...)
		}
		provider := provider{bin: op.provider}
		mutation := &MutationCommand{Command: provider.command(ctx, op.Root, args...), ctx: ctx}
		command, finish, err := mutation.Prepare()
		if err != nil {
			record("failed", err.Error())
			continue
		}
		inv, err := scopeInventory(ctx, op.Root, op.Scope)
		fingerprint, fingerprintErr := scopeFingerprint(ctx, op.Root, op.Scope, inv.Skills)
		hash, hashErr := fileHashContext(ctx, op.provider)
		nativeChanged := false
		if op.nativeHash != "" {
			current, _, e := nativeInputs(ctx, op.Root, op.dependencies)
			nativeChanged = e != nil || current != op.nativeHash
		}
		if nativeChanged || err != nil || len(inv.Diagnostics) > 0 || fingerprintErr != nil || fingerprint != op.fingerprint || hashErr != nil || hash != op.providerHash {
			finish(nil)
			record("stale", "scope, lock, installed files or provider changed; preview again")
			continue
		}
		var output limitedOutput
		if out == nil {
			out = io.Discard
		}
		command.Stdout = io.MultiWriter(out, &output)
		command.Stderr = io.MultiWriter(out, &output)
		err = finish(command.Run())
		after, readErr := scopeInventory(ctx, op.Root, op.Scope)
		if op.Action == "experimental_sync" && len(op.Names) == 0 {
			names = nil
			for _, row := range after.Skills {
				if row.Lock != nil && row.Lock.SourceType == "node_modules" {
					names = append(names, row.Lock.Name)
				}
			}
			if len(names) == 0 {
				names = []string{"dependency skills"}
			}
		}
		for _, name := range names {
			status, detail := "unverified", "provider returned without a verified installed result"
			if err != nil {
				status, detail = "failed", err.Error()+"\n"+output.String()
			}
			if readErr == nil && len(after.Diagnostics) == 0 {
				for _, row := range after.Skills {
					if row.Lock == nil || row.Lock.Name != name || row.Presence != PresencePresent {
						continue
					}
					if _, verifyErr := installedFingerprint(ctx, row, true); verifyErr != nil {
						detail = verifyErr.Error()
						continue
					}
					changed := true
					if op.Action == "update" {
						for _, before := range op.rows {
							if before.Lock != nil && before.Lock.Name == name {
								changed = before.Lock.RecordedHash != row.Lock.RecordedHash
							}
						}
					}
					if changed {
						status = "completed"
						detail = "installed files verified against the resulting lock"
					} else {
						detail = "recorded content did not change; inspect provider output"
					}
				}
			}
			if status != "completed" && output.Len() > 0 && err == nil {
				detail += "\n" + output.String()
			}
			receipt.Outcomes = append(receipt.Outcomes, ManageOutcome{op.Root, op.Scope, name, op.Action, status, gitx.SafeDiagnosticText(detail)})
		}
	}
	return receipt
}

func SaveManageReceipt(stateDir string, receipt ManageReceipt) (string, error) {
	dir := filepath.Join(stateDir, "skills", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if _, err := uuid.Parse(receipt.ID); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, receipt.ID+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return path, errors.Join(writeErr, closeErr)
}

func SortManagement(ops []ManageOperation) {
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Root < ops[j].Root })
}

func (op ManageOperation) authorization() string {
	data, _ := json.Marshal(struct {
		Root                                            string
		Scope                                           Scope
		Action                                          string
		Names, Agents                                   []string
		Fingerprint, Provider, ProviderHash, NativeHash string
	}{op.Root, op.Scope, op.Action, op.Names, op.Agents, op.fingerprint, op.provider, op.providerHash, op.nativeHash})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
