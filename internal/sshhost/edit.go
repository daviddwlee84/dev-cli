package sshhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const layoutMarker = "# dev-ssh-layout: grouped-v1"

// FormatConfig preserves every byte after leading horizontal whitespace. It
// never evaluates Match, command options, Includes, keys or DNS.
func FormatConfig(data []byte, indent string) ([]byte, error) {
	if indent != "  " && indent != "    " && indent != "\t" {
		return nil, errors.New("indent must be 2, 4 or tab")
	}
	var out bytes.Buffer
	inBlock := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		body := strings.TrimLeft(line, " \t")
		parse := strings.TrimSuffix(strings.TrimSuffix(body, "\n"), "\r")
		if strings.TrimSpace(parse) == "" {
			out.WriteString(line)
			continue
		}
		key, _, comment, err := parseConfigLine(parse)
		if err != nil {
			return nil, err
		}
		if comment {
			// Top-level comments commonly introduce the following Host block.
			if len(body) < len(line) && inBlock {
				out.WriteString(indent)
			}
			out.WriteString(body)
			continue
		}
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Match") {
			inBlock = true
		} else if inBlock {
			out.WriteString(indent)
		}
		out.WriteString(body)
	}
	return append([]byte{}, out.Bytes()...), nil
}

func (s *Service) EditablePath(path string) error {
	if path == s.paths.RootConfig {
		return nil
	}
	rel, err := filepath.Rel(filepath.Join(s.paths.SSHDir, "config.d"), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("select the SSH root config or a user file under ~/.ssh/config.d")
	}
	return nil
}

func (s *Service) PlanFormat(ctx context.Context, paths []string, indent string) (configedit.Plan, error) {
	var zero configedit.Plan
	if len(paths) == 0 {
		paths = []string{s.paths.RootConfig}
	}
	var desired [][]byte
	originals := map[string][]byte{}
	for _, path := range paths {
		if err := s.EditablePath(path); err != nil {
			return zero, err
		}
		data, err := configedit.Read(ctx, path)
		if err != nil {
			return zero, err
		}
		if data == nil {
			return zero, fmt.Errorf("configuration does not exist: %s", path)
		}
		if bytes.Contains(data, []byte(ManagedHeader)) || bytes.Contains(data, []byte("# BEGIN tsnet")) {
			return zero, errors.New("provider-managed configuration must be edited by its owner")
		}
		formatted, err := FormatConfig(data, indent)
		if err != nil {
			return zero, fmt.Errorf("format %s: %w", path, err)
		}
		originals[path] = data
		desired = append(desired, formatted)
	}
	p, err := configedit.New(ctx, paths, desired, paths)
	if err != nil {
		return p, err
	}
	p = p.WithLocks(s.paths.ManagedDir)
	return p, p.RequireContents(originals)
}

type ConfigBlock struct {
	ID       string   `json:"id"`
	Source   Location `json:"source"`
	Aliases  []string `json:"aliases"`
	Patterns []string `json:"patterns"`
	Group    string   `json:"group"`
	Text     string   `json:"-"`
}
type Layout struct {
	Blocks    []ConfigBlock `json:"blocks"`
	pieces    []layoutPiece
	originals map[string][]byte
	inventory Inventory
}
type layoutPiece struct {
	text string
	ids  []string
}

func splitBlocks(path string, data []byte) (string, []ConfigBlock, error) {
	lines := strings.SplitAfter(string(data), "\n")
	var starts, hostLines []int
	pending := -1
	for i, line := range lines {
		k, args, empty, err := parseConfigLine(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return "", nil, err
		}
		if empty {
			if pending < 0 {
				pending = i
			}
			continue
		}
		if strings.EqualFold(k, "Match") {
			return "", nil, errors.New("Match sections need manual organization; formatting remains available")
		}
		if strings.EqualFold(k, "Host") {
			if len(args) == 0 {
				return "", nil, errors.New("empty Host declaration")
			}
			start := i
			if len(starts) > 0 && pending >= 0 {
				start = pending
			}
			starts = append(starts, start)
			hostLines = append(hostLines, i)
		}
		pending = -1
	}
	if len(starts) == 0 {
		return string(data), nil, nil
	}
	var blocks []ConfigBlock
	for i, start := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		_, patterns, _, _ := parseConfigLine(strings.TrimRight(lines[hostLines[i]], "\r\n"))
		b := ConfigBlock{Source: Location{Path: path, Line: hostLines[i] + 1}, Patterns: patterns, Group: "ungrouped", Text: strings.Join(lines[start:end], "")}
		for _, p := range patterns {
			for _, alias := range splitPatternList(p) {
				if alias != "" && !strings.HasPrefix(alias, "!") && !hasHostPattern(alias) {
					b.Aliases = append(b.Aliases, alias)
				}
			}
		}
		blocks = append(blocks, b)
	}
	return strings.Join(lines[:starts[0]], ""), blocks, nil
}

// Organization discovers complete Host blocks. Foreign Includes retain their
// original position. Only direct, exact grouped fragment references are opened
// for regrouping; wildcard/nested/provider Includes remain owned by their author.
func (s *Service) Organization(ctx context.Context) (Layout, error) {
	l := Layout{Blocks: []ConfigBlock{}, originals: map[string][]byte{}}
	inv, err := s.Discover(ctx)
	if err != nil {
		return l, err
	}
	l.inventory = inv
	if !inv.Complete {
		return l, errors.New("SSH Include discovery is incomplete; use formatting or resolve its diagnostics first")
	}
	data, err := configedit.Read(ctx, s.paths.RootConfig)
	if err != nil {
		return l, err
	}
	if data == nil {
		return l, errors.New("SSH config is missing")
	}
	if bytes.Contains(data, []byte("# BEGIN tsnet")) {
		return l, errors.New("provider-managed root regions require native organization")
	}
	l.originals[s.paths.RootConfig] = data
	pre, blocks, err := splitBlocks(s.paths.RootConfig, data)
	if err != nil {
		return l, err
	}
	add := func(bs []ConfigBlock, header, group string) {
		var ids []string
		for i, b := range bs {
			b.ID = strconv.Itoa(len(l.Blocks) + 1)
			if group != "" {
				b.Group = group
			}
			if i == 0 {
				b.Text = header + b.Text
			}
			ids = append(ids, b.ID)
			l.Blocks = append(l.Blocks, b)
		}
		l.pieces = append(l.pieces, layoutPiece{ids: ids})
	}
	if len(blocks) > 0 {
		// Keep the file header and global Include directives before all new Includes.
		l.pieces = append(l.pieces, layoutPiece{text: pre})
		add(blocks, "", "")
	} else {
		for _, line := range strings.SplitAfter(pre, "\n") {
			k, args, _, parseErr := parseConfigLine(strings.TrimRight(line, "\r\n"))
			if parseErr != nil {
				return l, parseErr
			}
			if !strings.EqualFold(k, "Include") || len(args) != 1 {
				l.pieces = append(l.pieces, layoutPiece{text: line})
				continue
			}
			p := args[0]
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(s.paths.Home, p[2:])
			} else if !filepath.IsAbs(p) {
				p = filepath.Join(s.paths.SSHDir, p)
			}
			rel, relErr := filepath.Rel(filepath.Join(s.paths.SSHDir, "config.d"), p)
			parts := strings.Split(rel, string(filepath.Separator))
			if relErr != nil || len(parts) != 2 || parts[0] == ".." || strings.ContainsAny(p, "*?[%$") {
				l.pieces = append(l.pieces, layoutPiece{text: line})
				continue
			}
			for _, edge := range inv.Includes {
				if edge.Resolved == p && edge.Repeated {
					return l, errors.New("repeated fragment references need manual organization")
				}
			}
			b, readErr := configedit.Read(ctx, p)
			if readErr != nil {
				return l, readErr
			}
			header, bs, splitErr := splitBlocks(p, b)
			if splitErr != nil {
				return l, splitErr
			}
			if len(bs) == 0 {
				l.pieces = append(l.pieces, layoutPiece{text: line})
				continue
			}
			for _, hl := range strings.Split(header, "\n") {
				_, _, empty, _ := parseConfigLine(hl)
				if !empty {
					return l, errors.New("fragment global directives need manual organization")
				}
			}
			if bytes.Contains(b, []byte(ManagedHeader)) || bytes.Contains(b, []byte("# BEGIN tsnet")) {
				return l, errors.New("provider-managed fragment cannot be reorganized")
			}
			l.originals[p] = b
			add(bs, header, parts[0])
		}
	}
	return l, nil
}

func (s *Service) PlanOrganize(ctx context.Context, l Layout, groups map[string]string, numbered bool) (configedit.Plan, error) {
	var zero configedit.Plan
	if len(l.Blocks) == 0 {
		return zero, errors.New("no movable Host blocks")
	}
	known := map[string]bool{}
	for _, b := range l.Blocks {
		known[b.ID] = true
	}
	for id := range groups {
		if !known[id] {
			return zero, fmt.Errorf("unknown block %q", id)
		}
	}
	newline := "\n"
	if bytes.Contains(l.originals[s.paths.RootConfig], []byte("\r\n")) {
		newline = "\r\n"
	}
	var paths []string
	var desired [][]byte
	includes := map[string]string{}
	destinations := map[string]bool{}
	sourceCounts := map[string]int{}
	for _, b := range l.Blocks {
		sourceCounts[b.Source.Path]++
	}
	for _, b := range l.Blocks {
		group := b.Group
		if value, ok := groups[b.ID]; ok {
			group = value
		}
		if err := pathx.ValidatePortableComponent(group, 80); err != nil || strings.HasPrefix(group, ".") {
			return zero, errors.New("group must be a safe directory name")
		}
		name := "rules-" + b.ID
		if len(b.Aliases) > 0 {
			name = b.Aliases[0]
		} else if len(b.Patterns) == 1 && b.Patterns[0] == "*" {
			name = "defaults"
		}
		preserveName := b.Source.Path != s.paths.RootConfig && sourceCounts[b.Source.Path] == 1 && !numbered
		if preserveName {
			name = strings.TrimSuffix(filepath.Base(b.Source.Path), ".conf")
		} else {
			name = strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
					return r
				}
				return '-'
			}, name)
			name = strings.Trim(name, ".-")
		}
		if name == "" {
			name = "host"
		}
		if numbered {
			name = fmt.Sprintf("%03d-%s", mustBlockOrdinal(b.ID), name)
		}
		dest := filepath.Join(s.paths.SSHDir, "config.d", group, name+".conf")
		if destinations[dest] {
			dest = filepath.Join(filepath.Dir(dest), name+"-"+b.ID+".conf")
		}
		if destinations[dest] {
			return zero, errors.New("duplicate fragment destination")
		}
		existing, err := configedit.Read(ctx, dest)
		if err != nil {
			return zero, err
		}
		if existing != nil {
			if _, selected := l.originals[dest]; !selected {
				return zero, fmt.Errorf("destination already exists: %s", dest)
			}
		}
		if existing != nil && !bytes.Equal(existing, []byte(b.Text)) {
			// Never shorten an active source before the root Include switch.
			dest = filepath.Join(filepath.Dir(dest), name+"-"+configedit.Digest([]byte(b.Text))[:10]+".conf")
			prior, e := configedit.Read(ctx, dest)
			if e != nil {
				return zero, e
			}
			if prior != nil {
				return zero, fmt.Errorf("destination already exists: %s", dest)
			}
		}
		if existing == nil || dest != filepath.Join(s.paths.SSHDir, "config.d", group, name+".conf") {
			scan := scanner{service: s, options: s.options.Discovery.withDefaults()}
			for _, edge := range l.inventory.Includes {
				pattern, e := scan.expandInclude(edge.Argument)
				if e != nil {
					return zero, e
				}
				candidates := []string{dest}
				for parent := filepath.Dir(dest); parent != s.paths.SSHDir && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
					if _, e := os.Lstat(parent); e == nil {
						break
					} else if !errors.Is(e, os.ErrNotExist) {
						return zero, e
					}
					candidates = append(candidates, parent)
				}
				for _, candidate := range candidates {
					if matched, _ := filepath.Match(pattern, candidate); matched {
						return zero, errors.New("a current Include would match a new file or directory before the root switch; narrow that Include before organizing")
					}
				}
			}
		}
		destinations[dest] = true
		paths = append(paths, dest)
		desired = append(desired, []byte(b.Text))
		includes[b.ID] = "Include " + quoteConfigInclude("~/.ssh/config.d/"+group+"/"+filepath.Base(dest)) + newline
	}
	var root strings.Builder
	if !bytes.Contains(l.originals[s.paths.RootConfig], []byte(layoutMarker)) {
		root.WriteString(layoutMarker + newline)
	}
	for _, piece := range l.pieces {
		root.WriteString(piece.text)
		if len(piece.ids) > 0 && root.Len() > 0 && !strings.HasSuffix(root.String(), "\n") {
			root.WriteString(newline)
		}
		for _, id := range piece.ids {
			root.WriteString(includes[id])
		}
	}
	paths = append(paths, s.paths.RootConfig)
	desired = append(desired, []byte(root.String()))
	sources := []string{}
	for source := range l.originals {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		if source != s.paths.RootConfig && !destinations[source] {
			paths = append(paths, source)
			desired = append(desired, nil)
		}
	}
	guards := []string{}
	for _, f := range l.inventory.Files {
		guards = append(guards, f.Path)
	}
	for source := range l.originals {
		guards = append(guards, source)
	}
	p, err := configedit.New(ctx, paths, desired, guards)
	if err != nil {
		return p, err
	}
	p = p.WithLocks(s.paths.ManagedDir)
	current, err := s.Discover(ctx)
	if err != nil {
		return p, err
	}
	if !reflect.DeepEqual(current, l.inventory) {
		return p, configedit.ErrStale
	}
	return p, p.RequireContents(l.originals)
}

func mustBlockOrdinal(id string) int { n, _ := strconv.Atoi(id); return n }

func quoteConfigInclude(path string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `"`, `\"`) + `"`
}
