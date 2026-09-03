package sshhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const maxManagedAliasBytes = 128

type managedPlanState struct {
	serviceID  uint64
	operation  ManagedOperation
	action     PlanAction
	alias      string
	path       string
	definition ManagedDefinition
	source     fileSnapshot
	desired    []byte
}

// ManagedFile is a validated concrete dev-owned fragment.
type ManagedFile struct {
	Path       string            `json:"path"`
	Definition ManagedDefinition `json:"definition"`
	Digest     string            `json:"digest"`
	Mode       fs.FileMode       `json:"mode"`
}

// ValidateManagedAlias enforces the portable lowercase v1 filename grammar.
func ValidateManagedAlias(alias string) error {
	if len(alias) == 0 || len(alias) > maxManagedAliasBytes || !validUTF8NoControl(alias) {
		return fmt.Errorf("alias %q must be 1-%d printable bytes: %w", alias, maxManagedAliasBytes, ErrInvalidAlias)
	}
	if alias != strings.ToLower(alias) {
		return fmt.Errorf("alias %q must be lowercase: %w", alias, ErrInvalidAlias)
	}
	if err := pathx.ValidateComponent(alias); err != nil {
		return fmt.Errorf("alias %q: %w", alias, ErrInvalidAlias)
	}
	for index, r := range alias {
		allowed := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '_' || r == '-')
		if !allowed {
			return fmt.Errorf("alias %q contains non-portable character %q: %w", alias, r, ErrInvalidAlias)
		}
	}
	last := alias[len(alias)-1]
	if !asciiAlphaNumeric(last) {
		return fmt.Errorf("alias %q must end in a letter or digit: %w", alias, ErrInvalidAlias)
	}
	base := strings.ToLower(strings.SplitN(alias, ".", 2)[0])
	if windowsReservedBase(base) {
		return fmt.Errorf("alias %q is a reserved Windows filename: %w", alias, ErrInvalidAlias)
	}
	return nil
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func windowsReservedBase(base string) bool {
	switch base {
	case "con", "prn", "aux", "nul", "clock$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// ValidateManagedDefinition applies the v1 directive allowlist and value rules.
func ValidateManagedDefinition(definition ManagedDefinition) error {
	if err := ValidateManagedAlias(definition.Alias); err != nil {
		return err
	}
	if definition.HostName == "" {
		return errors.New("managed HostName is required")
	}
	for name, value := range map[string]string{
		"HostName": definition.HostName, "User": definition.User, "ProxyJump": definition.ProxyJump,
		"IdentityFile": definition.IdentityFile,
	} {
		if value != "" && !validConfigValue(value) {
			return fmt.Errorf("managed %s contains whitespace control or invalid UTF-8", name)
		}
	}
	if definition.Port < 0 || definition.Port > 65535 {
		return fmt.Errorf("managed Port %d is outside 1-65535", definition.Port)
	}
	return nil
}

func cloneManagedDefinition(definition ManagedDefinition) ManagedDefinition {
	copy := definition
	if definition.IdentitiesOnly != nil {
		value := *definition.IdentitiesOnly
		copy.IdentitiesOnly = &value
	}
	return copy
}

func validConfigValue(value string) bool {
	if !validUTF8NoControl(value) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == 0 {
			return false
		}
	}
	return true
}

// RenderManaged returns the only canonical byte representation of a v1 file.
func RenderManaged(definition ManagedDefinition) ([]byte, error) {
	if err := ValidateManagedDefinition(definition); err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(ManagedHeader)
	body.WriteByte('\n')
	body.WriteString("Host ")
	body.WriteString(definition.Alias)
	body.WriteByte('\n')
	writeDirective := func(name, value string) {
		if value == "" {
			return
		}
		body.WriteString("    ")
		body.WriteString(name)
		body.WriteByte(' ')
		body.WriteString(quoteConfigValue(value))
		body.WriteByte('\n')
	}
	writeDirective("HostName", definition.HostName)
	writeDirective("User", definition.User)
	if definition.Port != 0 {
		writeDirective("Port", strconv.Itoa(definition.Port))
	}
	writeDirective("ProxyJump", definition.ProxyJump)
	writeDirective("IdentityFile", definition.IdentityFile)
	if definition.IdentitiesOnly != nil {
		value := "no"
		if *definition.IdentitiesOnly {
			value = "yes"
		}
		writeDirective("IdentitiesOnly", value)
	}
	return []byte(body.String()), nil
}

func quoteConfigValue(value string) string {
	safe := value != ""
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("#\"'\\", r) {
			safe = false
			break
		}
	}
	if safe {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

// ParseManaged accepts only canonical v1 content. A valid-looking block with
// comments, reordered directives, duplicates, or unknown text is manual drift.
func ParseManaged(data []byte) (ManagedDefinition, error) {
	if bytes.ContainsRune(data, 0) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return ManagedDefinition{}, fmt.Errorf("managed file has a BOM or NUL: %w", ErrNotManaged)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 || lines[0] != ManagedHeader || lines[len(lines)-1] != "" {
		return ManagedDefinition{}, fmt.Errorf("managed file has no canonical v1 header/ending: %w", ErrNotManaged)
	}
	hostDirective, hostArguments, empty, err := parseConfigLine(lines[1])
	if err != nil || empty || !strings.EqualFold(hostDirective, "Host") || len(hostArguments) != 1 {
		return ManagedDefinition{}, fmt.Errorf("managed file must contain exactly one Host alias: %w", ErrNotManaged)
	}
	definition := ManagedDefinition{Alias: hostArguments[0]}
	seen := make(map[string]bool)
	for index, line := range lines[2 : len(lines)-1] {
		directive, arguments, empty, err := parseConfigLine(line)
		if err != nil || empty || len(arguments) != 1 {
			return ManagedDefinition{}, fmt.Errorf("managed line %d is malformed: %w", index+3, ErrNotManaged)
		}
		key := strings.ToLower(directive)
		if seen[key] {
			return ManagedDefinition{}, fmt.Errorf("managed directive %s is duplicated: %w", directive, ErrNotManaged)
		}
		seen[key] = true
		value := arguments[0]
		switch key {
		case "hostname":
			definition.HostName = value
		case "user":
			definition.User = value
		case "port":
			port, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return ManagedDefinition{}, fmt.Errorf("managed Port is invalid: %w", ErrNotManaged)
			}
			definition.Port = port
		case "proxyjump":
			definition.ProxyJump = value
		case "identityfile":
			definition.IdentityFile = value
		case "identitiesonly":
			enabled := false
			switch strings.ToLower(value) {
			case "yes":
				enabled = true
			case "no":
			default:
				return ManagedDefinition{}, fmt.Errorf("managed IdentitiesOnly is invalid: %w", ErrNotManaged)
			}
			definition.IdentitiesOnly = &enabled
		default:
			return ManagedDefinition{}, fmt.Errorf("managed directive %s is not allowlisted: %w", directive, ErrNotManaged)
		}
	}
	canonical, err := RenderManaged(definition)
	if err != nil {
		return ManagedDefinition{}, fmt.Errorf("validate managed file: %w", ErrNotManaged)
	}
	if !bytes.Equal(canonical, data) {
		return ManagedDefinition{}, fmt.Errorf("managed file differs from canonical v1 rendering: %w", ErrNotManaged)
	}
	return definition, nil
}

// InspectManaged validates the concrete filename, file security, and canonical
// bytes for alias.
func (s *Service) InspectManaged(alias string) (ManagedFile, error) {
	if err := inspectExistingTree(s.paths); err != nil {
		return ManagedFile{}, err
	}
	path, err := s.paths.ManagedPath(alias)
	if err != nil {
		return ManagedFile{}, err
	}
	snapshot, err := readSecureFile(path, false)
	if err != nil {
		return ManagedFile{}, err
	}
	definition, err := ParseManaged(snapshot.data)
	if err != nil {
		return ManagedFile{}, err
	}
	if definition.Alias != alias {
		return ManagedFile{}, fmt.Errorf("managed alias %q does not match filename %q: %w", definition.Alias, alias, ErrNotManaged)
	}
	return ManagedFile{Path: path, Definition: definition, Digest: snapshot.digest, Mode: snapshot.info.Mode().Perm()}, nil
}

func (s *Service) classifyDiscoveredOwnership(definition AliasDefinition) Ownership {
	relative, err := filepath.Rel(s.paths.ManagedDir, definition.Source.Path)
	inside := err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if !inside {
		return OwnershipForeign
	}
	if err := ValidateManagedAlias(definition.Alias); err != nil {
		return OwnershipConflict
	}
	expected, err := s.paths.ManagedPath(definition.Alias)
	if err != nil || !samePath(expected, definition.Source.Path) {
		return OwnershipConflict
	}
	managed, err := s.InspectManaged(definition.Alias)
	if err != nil || !samePath(managed.Path, definition.Source.Path) {
		return OwnershipConflict
	}
	return OwnershipManaged
}

// PlanManaged performs static collision and concrete ownership checks without
// creating directories, lock files, or staging files.
func (s *Service) PlanManaged(ctx context.Context, request ManagedRequest) (ManagedPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation := request.Operation
	if operation == "" {
		operation = ManagedUpsert
	}
	if operation != ManagedUpsert && operation != ManagedRemove {
		return ManagedPlan{}, fmt.Errorf("unknown managed operation %q", operation)
	}
	alias := request.Definition.Alias
	if err := ValidateManagedAlias(alias); err != nil {
		return ManagedPlan{}, err
	}
	var desired []byte
	var err error
	if operation == ManagedUpsert {
		desired, err = RenderManaged(request.Definition)
		if err != nil {
			return ManagedPlan{}, err
		}
	}
	path, err := s.paths.ManagedPath(alias)
	if err != nil {
		return ManagedPlan{}, err
	}
	plan := ManagedPlan{
		Operation: operation, Alias: alias, Path: path, Mode: 0o600,
		AfterDigest: digestBytes(desired),
	}
	if operation == ManagedRemove {
		plan.AfterDigest = ""
	} else {
		copy := cloneManagedDefinition(request.Definition)
		plan.Definition = &copy
	}
	block := func(code, message string) (ManagedPlan, error) {
		plan.Action = ActionBlocked
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Code: code, Message: message, Path: path, BlocksMutation: true,
		})
		return plan, nil
	}
	if err := inspectExistingTree(s.paths); err != nil {
		return block("unsafe_tree", err.Error())
	}
	if diagnostics := s.inspectManagedNamespace(); len(diagnostics) > 0 {
		plan.Diagnostics = append(plan.Diagnostics, diagnostics...)
		plan.Action = ActionBlocked
		return plan, nil
	}
	source, err := readSecureFileIfExists(path, false)
	if err != nil {
		return block("unsafe_managed_target", err.Error())
	}
	plan.BeforeDigest = source.digest
	if source.exists {
		definition, parseErr := ParseManaged(source.data)
		if parseErr != nil || definition.Alias != alias {
			return block("managed_drift", "target is not the expected canonical dev-owned fragment")
		}
	}
	if operation == ManagedRemove {
		if !source.exists {
			plan.Action = ActionNoop
		} else {
			plan.Action = ActionRemove
		}
		plan.state = &managedPlanState{
			serviceID: s.id, operation: operation, action: plan.Action, alias: alias, path: path, source: source,
		}
		return plan, nil
	}

	inventory, err := s.Discover(ctx)
	if err != nil {
		return ManagedPlan{}, err
	}
	if !inventory.ManagedIncludeActive {
		return block("managed_include_inactive", "the exact top-level "+rootIncludeLine()+" is not active")
	}
	collisions := s.managedCollisionDiagnostics(alias, path, inventory)
	if len(collisions) > 0 {
		plan.Diagnostics = append(plan.Diagnostics, collisions...)
		plan.Action = ActionBlocked
		return plan, nil
	}
	switch {
	case !source.exists:
		plan.Action = ActionCreate
	case bytes.Equal(source.data, desired):
		plan.Action = ActionNoop
	default:
		plan.Action = ActionUpdate
	}
	plan.state = &managedPlanState{
		serviceID: s.id, operation: operation, action: plan.Action, alias: alias, path: path,
		definition: cloneManagedDefinition(request.Definition), source: source, desired: append([]byte(nil), desired...),
	}
	return plan, nil
}

// PlanUpsert is a convenience wrapper around PlanManaged.
func (s *Service) PlanUpsert(ctx context.Context, definition ManagedDefinition) (ManagedPlan, error) {
	return s.PlanManaged(ctx, ManagedRequest{Operation: ManagedUpsert, Definition: definition})
}

// PlanRemove is a convenience wrapper around PlanManaged.
func (s *Service) PlanRemove(ctx context.Context, alias string) (ManagedPlan, error) {
	return s.PlanManaged(ctx, ManagedRequest{Operation: ManagedRemove, Definition: ManagedDefinition{Alias: alias}})
}

// ApplyManaged applies only a ready plan produced by this Service. Reapplying a
// converged plan is a no-op; any other source divergence is rejected.
func (s *Service) ApplyManaged(ctx context.Context, plan ManagedPlan) (ManagedResult, error) {
	result := ManagedResult{Action: plan.Action, Alias: plan.Alias, Path: plan.Path}
	if plan.Action == ActionBlocked || plan.state == nil {
		return result, ErrBlocked
	}
	if plan.state.serviceID != s.id || plan.Path != plan.state.path || plan.Alias != plan.state.alias ||
		plan.Operation != plan.state.operation || plan.Action != plan.state.action {
		return result, errors.New("managed plan was not produced by this service or its public fields were modified")
	}
	if plan.Action == ActionNoop {
		current, err := snapshotStillCurrent(plan.state.source)
		if err != nil && plan.Operation == ManagedUpsert {
			if converged, ok := bytesAlreadyDesired(plan.Path, plan.state.desired); ok {
				current, err = converged, nil
			}
		}
		if err != nil {
			return result, fmt.Errorf("apply managed no-op: %w", ErrSourceChanged)
		}
		result.Action = ActionNoop
		result.Digest = current.digest
		if plan.Operation == ManagedUpsert {
			if err := s.revalidateManagedInventory(ctx, plan.state.alias, plan.state.path); err != nil {
				return result, err
			}
			if err := s.verifyManagedSnapshot(ctx, plan.state.definition, current); err != nil {
				return result, err
			}
			result.Verified = true
		}
		return result, nil
	}
	if err := ensureMutationTree(s.paths); err != nil {
		return result, err
	}
	err := lockx.WithDir(ctx, s.paths.ManagedDir, "SSH host config", func() error {
		if diagnostics := s.inspectManagedNamespace(); len(diagnostics) > 0 {
			return fmt.Errorf("managed namespace changed: %s: %w", diagnostics[0].Message, ErrSourceChanged)
		}
		if plan.Operation == ManagedUpsert {
			if err := s.revalidateManagedInventory(ctx, plan.state.alias, plan.state.path); err != nil {
				return err
			}
			if converged, ok := bytesAlreadyDesired(plan.Path, plan.state.desired); ok {
				result.Action = ActionNoop
				result.Digest = converged.digest
				if err := s.verifyManagedSnapshot(ctx, plan.state.definition, converged); err != nil {
					return err
				}
				result.Verified = true
				return nil
			}
		}
		if plan.Operation == ManagedRemove {
			current, err := readSecureFileIfExists(plan.state.path, false)
			if err != nil {
				return err
			}
			if !current.exists {
				result.Action = ActionNoop
				return nil
			}
			if _, err := compareFileSnapshot(plan.state.source, current); err != nil {
				return fmt.Errorf("revalidate managed removal: %w", ErrSourceChanged)
			}
			if s.beforeManagedCommit != nil {
				s.beforeManagedCommit()
			}
			if err := removeSecureFile(plan.state.source); err != nil {
				return err
			}
			result.Action = ActionRemove
			result.Changed = true
			return nil
		}

		if _, err := snapshotStillCurrent(plan.state.source); err != nil {
			return fmt.Errorf("revalidate managed source: %w", ErrSourceChanged)
		}
		staged, err := createStagedFile(s.paths.ManagedDir, plan.state.desired, nil)
		if err != nil {
			return err
		}
		defer staged.discard()
		if s.beforeManagedCommit != nil {
			s.beforeManagedCommit()
		}
		if _, err := snapshotStillCurrent(plan.state.source); err != nil {
			return fmt.Errorf("source changed before managed publication: %w", ErrSourceChanged)
		}
		if plan.Action == ActionCreate {
			err = commitNoReplace(staged, plan.state.path, plan.state.source)
		} else {
			err = commitReplace(staged, plan.state.path, plan.state.source)
		}
		if err != nil {
			return err
		}
		written, err := readSecureFile(plan.Path, false)
		if err != nil {
			return fmt.Errorf("verify managed publication: %w", err)
		}
		if !bytes.Equal(written.data, plan.state.desired) {
			return errors.New("managed publication did not retain desired bytes")
		}
		result.Action = plan.Action
		result.Changed = true
		result.Digest = written.digest
		if err := s.verifyManagedSnapshot(ctx, plan.state.definition, written); err != nil {
			rollbackErr := s.rollbackManagedPublication(plan, written)
			if rollbackErr == nil {
				result.Changed = false
				result.RolledBack = true
				result.Digest = plan.BeforeDigest
				return fmt.Errorf("verify managed SSH config (publication rolled back): %w", err)
			}
			return errors.Join(fmt.Errorf("verify managed SSH config: %w", err), fmt.Errorf("rollback managed publication: %w", rollbackErr))
		}
		result.Verified = true
		return nil
	})
	return result, err
}

func (s *Service) revalidateManagedInventory(ctx context.Context, alias, path string) error {
	inventory, err := s.Discover(ctx)
	if err != nil {
		return err
	}
	if !inventory.ManagedIncludeActive {
		return fmt.Errorf("managed Include became inactive: %w", ErrSourceChanged)
	}
	if diagnostics := s.managedCollisionDiagnostics(alias, path, inventory); len(diagnostics) > 0 {
		return fmt.Errorf("SSH collision appeared after planning: %s: %w", diagnostics[0].Message, ErrSourceChanged)
	}
	return nil
}

func (s *Service) verifyManagedSnapshot(ctx context.Context, definition ManagedDefinition, expected fileSnapshot) error {
	if err := s.verifyManagedEffective(ctx, definition); err != nil {
		return err
	}
	if _, err := snapshotStillCurrent(expected); err != nil {
		return fmt.Errorf("managed file changed during ssh -G verification: %w", ErrSourceChanged)
	}
	return nil
}

func (s *Service) rollbackManagedPublication(plan ManagedPlan, written fileSnapshot) error {
	if _, err := snapshotStillCurrent(written); err != nil {
		return fmt.Errorf("published managed identity is no longer owned: %w", ErrSourceChanged)
	}
	switch plan.state.action {
	case ActionCreate:
		if err := removeSecureFile(written); err != nil {
			return err
		}
		current, err := readSecureFileIfExists(plan.state.path, false)
		if err != nil {
			return err
		}
		if current.exists {
			return fmt.Errorf("created managed file remains after rollback: %w", ErrSourceChanged)
		}
		return nil
	case ActionUpdate:
		staged, err := createStagedFile(s.paths.ManagedDir, plan.state.source.data, nil)
		if err != nil {
			return err
		}
		defer staged.discard()
		if err := commitReplace(staged, plan.state.path, written); err != nil {
			return err
		}
		restored, err := readSecureFile(plan.state.path, false)
		if err != nil {
			return err
		}
		if !bytes.Equal(restored.data, plan.state.source.data) || restored.digest != plan.state.source.digest {
			return fmt.Errorf("managed bytes differ after rollback: %w", ErrSourceChanged)
		}
		return nil
	default:
		return fmt.Errorf("managed action %q cannot be rolled back", plan.state.action)
	}
}

func (s *Service) managedCollisionDiagnostics(alias, target string, inventory Inventory) []Diagnostic {
	var diagnostics []Diagnostic
	for _, diagnostic := range inventory.Diagnostics {
		if diagnostic.BlocksMutation {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	for _, declaration := range inventory.Declarations {
		if samePath(declaration.Source.Path, target) {
			continue
		}
		guards := []Guard(nil)
		if len(declaration.Provenance) > 0 {
			guards = declaration.Provenance[len(declaration.Provenance)-1].Guards
		}
		reachability := evaluateReachability(alias, guards)
		if reachability == Unreachable || !patternListMatches(alias, declaration.Patterns) {
			continue
		}
		code := "foreign_wildcard_collision"
		message := fmt.Sprintf("foreign Host pattern at %s:%d may match %q", declaration.Source.Path, declaration.Source.Line, alias)
		for _, exact := range declaration.ExactAliases {
			if equalAlias(exact, alias) {
				code = "foreign_exact_collision"
				message = fmt.Sprintf("foreign exact Host %q already exists at %s:%d", exact, declaration.Source.Path, declaration.Source.Line)
				break
			}
		}
		location := declaration.Source
		diagnostics = append(diagnostics, Diagnostic{
			Code: code, Message: message, Path: declaration.Source.Path, Source: &location, BlocksMutation: true,
			Incomplete: reachability == Unknown,
		})
	}
	return diagnostics
}

func (s *Service) inspectManagedNamespace() []Diagnostic {
	info, err := os.Lstat(s.paths.ManagedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	block := func(code, message, path string) []Diagnostic {
		return []Diagnostic{{Code: code, Message: message, Path: path, BlocksMutation: true}}
	}
	if err != nil {
		return block("managed_namespace_unreadable", err.Error(), s.paths.ManagedDir)
	}
	if err := validatePrivateDirectory(s.paths.ManagedDir, info); err != nil {
		return block("managed_namespace_unsafe", err.Error(), s.paths.ManagedDir)
	}
	entries, err := os.ReadDir(s.paths.ManagedDir)
	if err != nil {
		return block("managed_namespace_unreadable", err.Error(), s.paths.ManagedDir)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	folded := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(s.paths.ManagedDir, name)
		if !strings.HasSuffix(name, ".conf") {
			return block("managed_namespace_foreign_entry", fmt.Sprintf("unexpected entry %q in managed namespace", name), path)
		}
		alias := strings.TrimSuffix(name, ".conf")
		if err := ValidateManagedAlias(alias); err != nil {
			return block("managed_namespace_invalid_name", err.Error(), path)
		}
		key := foldAlias(name)
		if previous, ok := folded[key]; ok && previous != name {
			return block("managed_namespace_casefold_collision", fmt.Sprintf("%q and %q collide case-insensitively", previous, name), path)
		}
		folded[key] = name
		snapshot, err := readSecureFile(path, false)
		if err != nil {
			return block("managed_namespace_unsafe_entry", err.Error(), path)
		}
		definition, err := ParseManaged(snapshot.data)
		if err != nil || definition.Alias != alias {
			return block("managed_namespace_drift", fmt.Sprintf("%q is not a canonical fragment matching its filename", name), path)
		}
	}
	return nil
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
