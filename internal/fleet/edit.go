package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/pelletier/go-toml/v2/unstable"
)

type HostEditPlan struct {
	Name    string          `json:"name"`
	NewName string          `json:"new_name,omitempty"`
	Action  string          `json:"action"`
	Files   configedit.Plan `json:"files"`
	primary string
	name    string
	newName string
	action  string
	names   []string
	files   configedit.Plan
}

func PlanHostEdit(ctx context.Context, primary, name, action, newName string) (HostEditPlan, error) {
	p := HostEditPlan{Name: name, NewName: newName, Action: action, primary: primary, name: name, newName: newName, action: action}
	if action != "rename" && action != "remove" {
		return p, errors.New("fleet edit action must be rename or remove")
	}
	cfg, err := LoadConfig(primary)
	if err != nil {
		return p, err
	}
	var selected *Host
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Name == name {
			selected = &cfg.Hosts[i]
		}
	}
	if selected == nil {
		return p, errors.New("fleet host not found")
	}
	if action == "rename" {
		if strings.TrimSpace(newName) == "" || strings.TrimSpace(newName) != newName || strings.ContainsAny(newName, "\r\n\x00") {
			return p, errors.New("new fleet name must be nonempty and trimmed")
		}
		for _, h := range cfg.Hosts {
			if h.Name == newName && h.Name != name {
				return p, errors.New("fleet name already exists")
			}
		}
	}
	path := selected.Origin()
	before, err := configedit.Read(ctx, path)
	if err != nil {
		return p, err
	}
	var after []byte
	if selected.Managed() {
		managed, parseErr := ValidateManagedFragment(path)
		if parseErr != nil {
			return p, parseErr
		}
		if action == "rename" {
			managed.Name = newName
			after, err = RenderManagedFragment(managed)
		}
	} else {
		after, err = patchPrimaryHost(before, name, newName, action == "remove")
	}
	if err != nil {
		return p, err
	}
	files, err := configedit.New(ctx, []string{path}, [][]byte{after}, []string{path})
	if err != nil {
		return p, err
	}
	if err = files.RequireContents(map[string][]byte{path: before}); err != nil {
		return p, err
	}
	files = files.WithLocks(filepath.Dir(primary), ManagedFragmentDir(primary))
	p.Files = files
	p.files = files
	return p, nil
}

func ApplyHostEdit(ctx context.Context, p HostEditPlan, recovery string) (configedit.Result, error) {
	var result configedit.Result
	if p.primary == "" {
		return result, errors.New("invalid fleet edit plan")
	}
	return configedit.ApplyChecked(ctx, p.files, recovery, func(ctx context.Context) error {
		cfg, err := LoadConfig(p.primary)
		if err != nil {
			return err
		}
		names := p.names
		if len(names) == 0 {
			names = []string{p.name}
		}
		selected := map[string]bool{}
		for _, name := range names {
			selected[name] = true
		}
		var hosts []Host
		for _, h := range cfg.Hosts {
			if selected[h.Name] {
				delete(selected, h.Name)
				if p.action == "remove" {
					continue
				}
				h.Name = p.newName
			}
			hosts = append(hosts, h)
		}
		if len(selected) > 0 {
			return configedit.ErrStale
		}
		cfg.Hosts = hosts
		return cfg.Validate()
	})
}

// patchPrimaryHost changes only the name value or the complete selected array
// table and its child tables. Parser spans preserve all unrelated bytes,
// including quoted keys, multiline strings, comments and password references.
func patchPrimaryHost(data []byte, name, newName string, remove bool) ([]byte, error) {
	var parsed struct {
		Hosts []Host `toml:"hosts"`
	}
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return nil, errors.New("invalid primary TOML")
	}
	selectedIndex := -1
	for i, h := range parsed.Hosts {
		if h.Name == name {
			selectedIndex = i
		}
	}
	if selectedIndex < 0 {
		return nil, errors.New("fleet host not found")
	}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	index := -1
	start, end, nameStart, nameEnd := -1, len(data), -1, -1
	pending := -1
	inHost := false
	inSelected := false
	lineStart := func(pos int) int {
		for pos > 0 && data[pos-1] != '\n' {
			pos--
		}
		return pos
	}
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.Comment {
			offset := int(n.Raw.Offset)
			if len(bytes.TrimSpace(data[lineStart(offset):offset])) == 0 && pending < 0 {
				pending = lineStart(offset)
			}
			continue
		}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			var keys []string
			offset := -1
			it := n.Key()
			for it.Next() {
				child := it.Node()
				keys = append(keys, string(child.Data))
				if offset < 0 {
					offset = int(child.Raw.Offset)
				}
			}
			if offset < 0 {
				return nil, errors.New("unsupported TOML table layout")
			}
			boundary := lineStart(offset)
			if pending >= 0 {
				boundary = pending
			}
			isHost := n.Kind == unstable.ArrayTable && len(keys) == 1 && keys[0] == "hosts"
			child := len(keys) > 1 && keys[0] == "hosts"
			if inSelected && !child {
				end = boundary
				inSelected = false
			}
			if isHost {
				index++
				inHost = true
				inSelected = index == selectedIndex
				if inSelected {
					start = boundary
				}
			}
			if !isHost && !child {
				inHost = false
			}
		} else if n.Kind == unstable.KeyValue && inHost && index == selectedIndex {
			var keys []string
			it := n.Key()
			for it.Next() {
				keys = append(keys, string(it.Node().Data))
			}
			if len(keys) == 1 && keys[0] == "name" && inSelected {
				v := n.Value()
				nameStart = int(v.Raw.Offset)
				nameEnd = nameStart + int(v.Raw.Length)
			}
		}
		pending = -1
	}
	if parser.Error() != nil || start < 0 || index+1 != len(parsed.Hosts) {
		return nil, errors.New("inline or unsupported hosts layout needs a native edit")
	}
	if remove {
		return append(append([]byte{}, data[:start]...), data[end:]...), nil
	}
	if nameStart < start || nameEnd > end || nameStart < 0 {
		return nil, errors.New("cannot locate exact fleet name value")
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(map[string]string{"name": newName}); err != nil {
		return nil, err
	}
	value := strings.TrimSpace(strings.TrimPrefix(encoded.String(), "name ="))
	result := append([]byte{}, data[:nameStart]...)
	result = append(result, []byte(value)...)
	result = append(result, data[nameEnd:]...)
	var check struct {
		Hosts []Host `toml:"hosts"`
	}
	if _, err := toml.Decode(string(result), &check); err != nil {
		return nil, fmt.Errorf("validate edited host: %w", err)
	}
	if len(check.Hosts) != len(parsed.Hosts) || check.Hosts[selectedIndex].Name != newName {
		return nil, errors.New("TOML edit changed an unexpected record")
	}
	return result, nil
}

// PlanHostRemovals coalesces selections from the same source into one file
// transaction, so a bulk remove never invalidates its own next record's plan.
func PlanHostRemovals(ctx context.Context, primary string, names []string) ([]HostEditPlan, error) {
	cfg, err := LoadConfig(primary)
	if err != nil {
		return nil, err
	}
	groups := map[string][]Host{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return nil, errors.New("duplicate fleet selection")
		}
		seen[name] = true
		found := false
		for _, h := range cfg.Hosts {
			if h.Name == name {
				groups[h.Origin()] = append(groups[h.Origin()], h)
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("fleet host not found")
		}
	}
	paths := []string{}
	for path := range groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var plans []HostEditPlan
	for _, path := range paths {
		hosts := groups[path]
		before, err := configedit.Read(ctx, path)
		if err != nil {
			return nil, err
		}
		after := before
		var selected []string
		for _, h := range hosts {
			selected = append(selected, h.Name)
			if h.Managed() {
				if _, err = ValidateManagedFragment(path); err != nil {
					return nil, err
				}
				after = nil
			} else {
				after, err = patchPrimaryHost(after, h.Name, "", true)
				if err != nil {
					return nil, err
				}
			}
		}
		files, err := configedit.New(ctx, []string{path}, [][]byte{after}, []string{path})
		if err != nil {
			return nil, err
		}
		if err = files.RequireContents(map[string][]byte{path: before}); err != nil {
			return nil, err
		}
		files = files.WithLocks(filepath.Dir(primary), ManagedFragmentDir(primary))
		plans = append(plans, HostEditPlan{Name: strings.Join(selected, ", "), Action: "remove", Files: files, primary: primary, files: files, action: "remove", names: selected})
	}
	return plans, nil
}
