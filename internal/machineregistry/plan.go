package machineregistry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

func (s *Store) Plan(ctx context.Context, request Request) (Plan, error) {
	before, source, err := s.read(ctx)
	if err != nil {
		return Plan{}, err
	}
	request.Bindings = append([]Binding{}, request.Bindings...)
	request.Imports = append([]SSHImport(nil), request.Imports...)
	if request.Action == "adopt" && request.MachineID == "" {
		request.MachineID = uuid.NewString()
	}
	after, effects, err := transition(before, request)
	if err != nil {
		return Plan{}, err
	}
	changed := !reflect.DeepEqual(before, after)
	status := "planned"
	if !changed {
		status = "noop"
	}
	return Plan{
		SchemaVersion: SchemaVersion, Action: request.Action, Status: status,
		MachineID: request.MachineID, Into: request.Into, Revision: before.Revision,
		Effects: effects, After: cloneSnapshot(after),
		state: &planState{path: s.Path, request: request, before: cloneSnapshot(before), after: cloneSnapshot(after), source: source, changed: changed},
	}, nil
}

func transition(before Snapshot, request Request) (Snapshot, []string, error) {
	if request.Action == "record-imports" {
		return transitionImports(before, request)
	}
	if len(request.Imports) > 0 {
		return before, nil, fmt.Errorf("import provenance requires record-imports")
	}
	after := cloneSnapshot(before)
	effects := []string{}
	if !validID(request.MachineID) {
		return after, effects, fmt.Errorf("machine_id must be a canonical non-zero UUID")
	}
	if len(request.Bindings) > maxBindings {
		return after, effects, fmt.Errorf("too many requested bindings")
	}
	seen := map[string]bool{}
	for _, binding := range request.Bindings {
		if err := validateBindingIdentity(binding); err != nil {
			return after, effects, err
		}
		if binding.Suppressed || binding.MachineID != "" && binding.MachineID != request.MachineID {
			return after, effects, fmt.Errorf("request binding must name its selected machine and cannot be suppressed")
		}
		key := bindingKey(binding)
		if seen[key] {
			return after, effects, fmt.Errorf("duplicate provider binding in request")
		}
		seen[key] = true
	}
	machineIndex := -1
	for i, machine := range after.Machines {
		if machine.ID == request.MachineID {
			machineIndex = i
		}
	}
	if request.Action != "merge" && request.Into != "" || request.Action != "adopt" && request.Label != "" {
		return after, effects, fmt.Errorf("into is only valid for merge and label is only valid for adopt")
	}
	switch request.Action {
	case "adopt":
		if machineIndex >= 0 {
			return after, effects, fmt.Errorf("machine ID already exists: %w", ErrConflict)
		}
		if !validText(request.Label, 256, true) {
			return after, effects, fmt.Errorf("machine label must be nonempty text of at most 256 bytes")
		}
		after.Machines = append(after.Machines, Machine{ID: request.MachineID, Label: request.Label, Revision: 1})
		machineIndex = len(after.Machines) - 1
		effects = append(effects, "Create a controller-local machine identity")
	case "link", "unlink":
		if machineIndex < 0 {
			return after, effects, ErrNotFound
		}
		if after.Machines[machineIndex].MergedInto != "" {
			return after, effects, fmt.Errorf("select the surviving machine ID: %w", ErrConflict)
		}
		if len(request.Bindings) == 0 {
			return after, effects, fmt.Errorf("%s requires at least one binding", request.Action)
		}
	case "merge":
		if len(request.Bindings) > 0 || !validID(request.Into) || request.Into == request.MachineID {
			return after, effects, fmt.Errorf("merge requires two different machine IDs and no bindings")
		}
		intoIndex := -1
		for i, machine := range after.Machines {
			if machine.ID == request.Into {
				intoIndex = i
			}
		}
		if machineIndex < 0 || intoIndex < 0 {
			return after, effects, ErrNotFound
		}
		if after.Machines[machineIndex].MergedInto != "" || after.Machines[intoIndex].MergedInto != "" {
			return after, effects, fmt.Errorf("merge requires active machine IDs: %w", ErrConflict)
		}
		for i := range after.Bindings {
			if after.Bindings[i].MachineID == request.MachineID {
				after.Bindings[i].MachineID = request.Into
			}
		}
		after.Machines[machineIndex].MergedInto = request.Into
		after.Machines[machineIndex].PreferredProfile = ""
		after.Machines[machineIndex].Revision++
		after.Machines[intoIndex].Revision++
		effects = append(effects, "Move source associations to the survivor and retain the source ID as a redirect", "Keep the survivor label and preferred profile")
	default:
		return after, effects, fmt.Errorf("action must be adopt, link, unlink or merge")
	}
	bindingsChanged := false
	if request.Action != "merge" {
		for _, binding := range request.Bindings {
			index := -1
			for i, current := range after.Bindings {
				if bindingKey(current) == bindingKey(binding) {
					index = i
					break
				}
			}
			if request.Action == "unlink" {
				if index < 0 {
					return before, nil, fmt.Errorf("binding does not exist: %w", ErrNotFound)
				}
				current := after.Bindings[index]
				if current.Suppressed {
					continue
				}
				if current.MachineID != request.MachineID {
					return before, nil, fmt.Errorf("binding belongs to another machine: %w", ErrConflict)
				}
				current.MachineID, current.Suppressed = "", true
				after.Bindings[index] = current
				bindingsChanged = true
				continue
			}
			binding.MachineID = request.MachineID
			if index >= 0 {
				current := after.Bindings[index]
				if !current.Suppressed && current.MachineID != request.MachineID {
					return before, nil, fmt.Errorf("binding belongs to another machine: %w", ErrConflict)
				}
				if current == binding {
					continue
				}
				after.Bindings[index] = binding
			} else {
				after.Bindings = append(after.Bindings, binding)
			}
			bindingsChanged = true
		}
	}
	if bindingsChanged {
		if request.Action != "adopt" {
			after.Machines[machineIndex].Revision++
		}
		if request.Action == "unlink" {
			effects = append(effects, "Unlink selected provider records and retain rediscovery suppression")
		} else {
			effects = append(effects, "Associate the selected provider records with this machine")
		}
	}
	sortSnapshot(&after)
	if !reflect.DeepEqual(before, after) {
		after.Revision++
	}
	if err := validateSnapshot(after); err != nil {
		return before, nil, err
	}
	return after, effects, nil
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validText(value string, max int, required bool) bool {
	return len(value) <= max && (!required || strings.TrimSpace(value) != "") && !strings.ContainsFunc(value, unicode.IsControl)
}

func bindingKey(binding Binding) string {
	return binding.Provider + "\x00" + binding.Scope + "\x00" + binding.NativeID
}

func validateBindingIdentity(binding Binding) error {
	if !validText(binding.Provider, 64, true) || !validText(binding.Scope, 4096, true) || !validText(binding.NativeID, 4096, true) || !validText(binding.Fingerprint, 1024, false) {
		return fmt.Errorf("invalid or oversized provider binding fields")
	}
	for i, r := range binding.Provider {
		if !(r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-')) {
			return fmt.Errorf("provider must be a lowercase identifier")
		}
	}
	return nil
}

func sortSnapshot(s *Snapshot) {
	sort.Slice(s.Machines, func(i, j int) bool { return s.Machines[i].ID < s.Machines[j].ID })
	sort.Slice(s.Bindings, func(i, j int) bool { return bindingKey(s.Bindings[i]) < bindingKey(s.Bindings[j]) })
	sort.Slice(s.Imports, func(i, j int) bool { return s.Imports[i].LocalAlias < s.Imports[j].LocalAlias })
}

func validateSnapshot(s Snapshot) error {
	if s.SchemaVersion != SchemaVersion || s.Revision > math.MaxInt64 || len(s.Machines) > maxMachines || len(s.Bindings) > maxBindings || len(s.Imports) > maxBindings {
		return ErrSchema
	}
	machines := map[string]Machine{}
	for _, machine := range s.Machines {
		if !validID(machine.ID) || !validText(machine.Label, 256, true) || machine.Revision == 0 || machine.Revision > math.MaxInt64 ||
			!validText(machine.PreferredProfile, 4096, false) || machine.MergedInto != "" && !validID(machine.MergedInto) {
			return fmt.Errorf("invalid machine registry record: %w", ErrSchema)
		}
		if _, exists := machines[machine.ID]; exists {
			return fmt.Errorf("duplicate machine ID: %w", ErrSchema)
		}
		machines[machine.ID] = machine
	}
	for _, machine := range s.Machines {
		seen := map[string]bool{machine.ID: true}
		for next := machine.MergedInto; next != ""; {
			target, exists := machines[next]
			if !exists || seen[next] {
				return fmt.Errorf("invalid machine merge redirect: %w", ErrSchema)
			}
			seen[next] = true
			next = target.MergedInto
		}
	}
	bindings := map[string]bool{}
	for _, binding := range s.Bindings {
		if err := validateBindingIdentity(binding); err != nil {
			return fmt.Errorf("invalid stored binding: %w", ErrSchema)
		}
		if bindings[bindingKey(binding)] {
			return fmt.Errorf("duplicate stored binding: %w", ErrSchema)
		}
		bindings[bindingKey(binding)] = true
		machine, found := machines[binding.MachineID]
		if binding.Suppressed {
			if binding.MachineID != "" {
				return fmt.Errorf("suppressed binding still owns a machine: %w", ErrSchema)
			}
		} else if !found || machine.MergedInto != "" {
			return fmt.Errorf("binding has no active machine: %w", ErrSchema)
		}
	}
	imports := map[string]bool{}
	for _, imported := range s.Imports {
		if err := validateImport(imported); err != nil || imports[imported.LocalAlias] {
			return errors.Join(ErrSchema, err)
		}
		imports[imported.LocalAlias] = true
	}
	return nil
}
