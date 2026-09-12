package machineregistry

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func validateImport(value SSHImport) error {
	if err := sshhost.ValidateManagedAlias(value.LocalAlias); err != nil {
		return err
	}
	for _, field := range []struct {
		value string
		limit int
	}{
		{value.OriginID, 256}, {value.ProfileID, 256}, {value.FleetHost, 4096},
		{value.RemoteAlias, 4096}, {value.SourceFingerprint, 1024},
		{value.RouteFingerprint, 1024}, {value.DefinitionFingerprint, 1024},
	} {
		if !validText(field.value, field.limit, true) {
			return fmt.Errorf("invalid SSH import provenance")
		}
	}
	return nil
}

func transitionImports(before Snapshot, request Request) (Snapshot, []string, error) {
	if request.MachineID != "" || request.Into != "" || request.Label != "" || len(request.Bindings) != 0 || len(request.Imports) == 0 || len(request.Imports) > maxBindings {
		return before, nil, fmt.Errorf("record-imports requires only exact import records")
	}
	after := cloneSnapshot(before)
	seen := map[string]bool{}
	for _, imported := range request.Imports {
		if err := validateImport(imported); err != nil {
			return before, nil, err
		}
		if seen[imported.LocalAlias] {
			return before, nil, fmt.Errorf("duplicate local import alias")
		}
		seen[imported.LocalAlias] = true
		found := false
		for i, previous := range after.Imports {
			if previous.LocalAlias != imported.LocalAlias {
				continue
			}
			if previous.OriginID != imported.OriginID || previous.ProfileID != imported.ProfileID {
				return before, nil, fmt.Errorf("local alias belongs to another import source; choose a new alias: %w", ErrConflict)
			}
			after.Imports[i], found = imported, true
			break
		}
		if !found {
			after.Imports = append(after.Imports, imported)
		}
	}
	sortSnapshot(&after)
	if !reflect.DeepEqual(before, after) {
		after.Revision++
	}
	if err := validateSnapshot(after); err != nil {
		return before, nil, err
	}
	aliases := make([]string, len(request.Imports))
	for i, imported := range request.Imports {
		aliases[i] = imported.LocalAlias
	}
	return after, []string{"Record exact SSH import origins for " + strings.Join(aliases, ", ")}, nil
}
