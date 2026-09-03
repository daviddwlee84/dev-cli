package sshhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultMaxDepth       = 16
	defaultMaxFiles       = 512
	defaultMaxTotalBytes  = 16 << 20
	defaultMaxFileBytes   = 2 << 20
	defaultMaxLineBytes   = 64 << 10
	defaultMaxGlobMatches = 512
)

// DiscoverOptions bounds static discovery and exposes expansion seams for
// hermetic tests. Environment must distinguish an unset variable from an empty
// one. LookupHome resolves ~user; current-user ~ always uses Paths.Home.
type DiscoverOptions struct {
	MaxDepth       int
	MaxFiles       int
	MaxTotalBytes  int64
	MaxFileBytes   int64
	MaxLineBytes   int
	MaxGlobMatches int
	Environment    func(string) (string, bool)
	LookupHome     func(string) (string, error)
}

func (o DiscoverOptions) withDefaults() DiscoverOptions {
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultMaxDepth
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = defaultMaxFiles
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = defaultMaxTotalBytes
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = defaultMaxFileBytes
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = defaultMaxLineBytes
	}
	if o.MaxGlobMatches <= 0 {
		o.MaxGlobMatches = defaultMaxGlobMatches
	}
	if o.Environment == nil {
		o.Environment = os.LookupEnv
	}
	if o.LookupHome == nil {
		o.LookupHome = func(name string) (string, error) {
			account, err := user.Lookup(name)
			if err != nil {
				return "", err
			}
			return account.HomeDir, nil
		}
	}
	return o
}

type scanner struct {
	service         *Service
	options         DiscoverOptions
	inventory       Inventory
	totalBytes      int64
	visits          map[string]int
	stack           []openSource
	aliasGroups     map[string]*Alias
	declarationSeen map[string]struct{}
	definitionSeen  map[string]struct{}
}

type openSource struct {
	path string
	info fs.FileInfo
}

type guardState struct {
	guard Guard
	set   bool
}

// Discover statically scans only Paths.RootConfig and Include files named by
// it. It never invokes Runner, ssh, a shell, or Match exec.
func (s *Service) Discover(ctx context.Context) (Inventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scan := &scanner{
		service: s,
		options: s.options.Discovery.withDefaults(),
		inventory: Inventory{
			Root:     s.paths.RootConfig,
			Complete: true,
		},
		visits:          make(map[string]int),
		aliasGroups:     make(map[string]*Alias),
		declarationSeen: make(map[string]struct{}),
		definitionSeen:  make(map[string]struct{}),
	}
	if _, err := os.Lstat(s.paths.RootConfig); errors.Is(err, fs.ErrNotExist) {
		scan.inventory.RootMissing = true
		return scan.inventory, nil
	} else if err != nil {
		scan.addDiagnostic(Diagnostic{
			Code: "root_unreadable", Message: fmt.Sprintf("cannot inspect user SSH config: %v", err),
			Path: s.paths.RootConfig, Incomplete: true, BlocksMutation: true,
		})
		return scan.inventory, nil
	}
	if _, err := scan.scanFile(ctx, s.paths.RootConfig, 0, nil, nil, guardState{}); err != nil {
		return Inventory{}, err
	}
	scan.finish()
	return scan.inventory, nil
}

func (s *scanner) scanFile(ctx context.Context, path string, depth int, provenance []IncludeFrame, constraints []Guard, initial guardState) (guardState, error) {
	if err := ctx.Err(); err != nil {
		return guardState{}, err
	}
	if depth > s.options.MaxDepth {
		s.addDiagnostic(Diagnostic{
			Code: "include_depth_exceeded", Message: fmt.Sprintf("Include depth exceeds %d", s.options.MaxDepth),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if len(s.inventory.Files) >= s.options.MaxFiles {
		s.addDiagnostic(Diagnostic{
			Code: "file_limit_exceeded", Message: fmt.Sprintf("Include visit count exceeds %d", s.options.MaxFiles),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}

	pathInfo, err := os.Lstat(path)
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "source_unreadable", Message: fmt.Sprintf("cannot inspect included SSH config path: %v", err),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		s.addDiagnostic(Diagnostic{
			Code: "source_not_regular", Message: "included SSH config is not a direct regular file",
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	file, err := os.Open(path)
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "source_unreadable", Message: fmt.Sprintf("cannot read included SSH config: %v", err),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "source_unreadable", Message: fmt.Sprintf("cannot inspect included SSH config: %v", err),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if !stableScannedInfo(pathInfo, info) {
		s.addDiagnostic(Diagnostic{
			Code: "source_changed", Message: "SSH config identity or metadata changed while it was being opened",
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if !info.Mode().IsRegular() {
		s.addDiagnostic(Diagnostic{
			Code: "source_not_regular", Message: "included SSH config is not a regular file",
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if info.Size() > s.options.MaxFileBytes {
		s.addDiagnostic(Diagnostic{
			Code: "file_size_exceeded", Message: fmt.Sprintf("SSH config exceeds %d bytes", s.options.MaxFileBytes),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if s.totalBytes+info.Size() > s.options.MaxTotalBytes {
		s.addDiagnostic(Diagnostic{
			Code: "byte_limit_exceeded", Message: fmt.Sprintf("SSH config closure exceeds %d bytes", s.options.MaxTotalBytes),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, s.options.MaxFileBytes+1))
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "source_unreadable", Message: fmt.Sprintf("cannot read included SSH config: %v", err),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	if int64(len(data)) > s.options.MaxFileBytes {
		s.addDiagnostic(Diagnostic{
			Code: "file_size_exceeded", Message: fmt.Sprintf("SSH config exceeds %d bytes", s.options.MaxFileBytes),
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}
	text := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(text) {
		s.addDiagnostic(Diagnostic{
			Code: "invalid_encoding", Message: "SSH config is not valid UTF-8",
			Path: path, Incomplete: true, BlocksMutation: true,
		})
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || pathAfter.Mode()&os.ModeSymlink != 0 ||
		!stableScannedInfo(info, after) || !stableScannedInfo(after, pathAfter) || after.Size() != int64(len(data)) {
		s.addDiagnostic(Diagnostic{
			Code: "source_changed", Message: "SSH config identity or metadata changed while it was being scanned",
			Path: path, Incomplete: true, BlocksMutation: true,
		})
		return initial, nil
	}

	cleanPath, _ := filepath.Abs(path)
	cleanPath = filepath.Clean(cleanPath)
	identityPath := sourceIdentityPath(cleanPath)
	s.visits[identityPath]++
	s.totalBytes += int64(len(data))
	s.inventory.Files = append(s.inventory.Files, ScannedFile{
		Path: cleanPath, Visit: s.visits[identityPath], Bytes: int64(len(data)), Provenance: cloneProvenance(provenance),
	})
	s.stack = append(s.stack, openSource{path: cleanPath, info: info})
	defer func() { s.stack = s.stack[:len(s.stack)-1] }()

	current := initial
	lines := bytes.Split(data, []byte{'\n'})
	for index, raw := range lines {
		if err := ctx.Err(); err != nil {
			return guardState{}, err
		}
		lineNumber := index + 1
		line := raw
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if lineNumber == 1 {
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
		}
		location := Location{Path: cleanPath, Line: lineNumber}
		if len(line) > s.options.MaxLineBytes {
			s.addDiagnostic(Diagnostic{
				Code: "line_size_exceeded", Message: fmt.Sprintf("SSH config line exceeds %d bytes", s.options.MaxLineBytes),
				Path: cleanPath, Source: &location, Incomplete: true, BlocksMutation: true,
			})
			continue
		}
		directive, arguments, empty, parseErr := parseConfigLine(string(line))
		if parseErr != nil {
			s.addDiagnostic(Diagnostic{
				Code: "malformed_line", Message: parseErr.Error(), Path: cleanPath, Source: &location,
				Incomplete: true, BlocksMutation: true,
			})
			continue
		}
		if empty {
			continue
		}
		switch {
		case strings.EqualFold(directive, "host"):
			if len(arguments) == 0 {
				s.addDiagnostic(Diagnostic{
					Code: "malformed_host", Message: "Host requires at least one pattern", Path: cleanPath,
					Source: &location, Incomplete: true, BlocksMutation: true,
				})
				current = guardState{guard: Guard{Kind: GuardHost, Source: location, Dynamic: true}, set: true}
				continue
			}
			current = guardState{guard: Guard{Kind: GuardHost, Arguments: append([]string(nil), arguments...), Source: location}, set: true}
			s.recordHost(arguments, location, provenance, constraints)
		case strings.EqualFold(directive, "match"):
			if len(arguments) == 0 {
				s.addDiagnostic(Diagnostic{
					Code: "malformed_match", Message: "Match requires criteria", Path: cleanPath,
					Source: &location, Incomplete: true, BlocksMutation: true,
				})
				current = guardState{guard: Guard{Kind: GuardMatch, Source: location, Dynamic: true}, set: true}
				continue
			}
			current = guardState{guard: Guard{
				Kind: GuardMatch, Arguments: append([]string(nil), arguments...), Source: location,
				Dynamic: !staticMatchGuard(arguments),
			}, set: true}
		case strings.EqualFold(directive, "include"):
			if len(arguments) == 0 {
				s.addDiagnostic(Diagnostic{
					Code: "malformed_include", Message: "Include requires at least one path", Path: cleanPath,
					Source: &location, Incomplete: true, BlocksMutation: true,
				})
				continue
			}
			if len(constraints) == 0 && !current.set && samePath(cleanPath, s.service.paths.RootConfig) {
				for _, argument := range arguments {
					if argument == ManagedInclude {
						s.inventory.ManagedIncludeActive = true
					}
				}
			}
			includeConstraints := cloneGuards(constraints)
			if current.set {
				if len(includeConstraints) == 0 || !guardsEqual(includeConstraints[len(includeConstraints)-1], current.guard) {
					includeConstraints = append(includeConstraints, cloneGuard(current.guard))
				}
				if current.guard.Dynamic {
					s.addDiagnostic(Diagnostic{
						Code: "dynamic_match_include", Message: "Include is guarded by Match criteria that static discovery cannot prove",
						Path: cleanPath, Source: &location, Incomplete: true, BlocksMutation: true,
					})
				}
			}
			// Include matches are scanned in OpenSSH's inline lexical order. The
			// final Host/Match state from one included source is the initial state
			// for the next source and for the line following Include.
			for _, argument := range arguments {
				next, scanErr := s.scanInclude(ctx, argument, location, depth, provenance, includeConstraints, current)
				if scanErr != nil {
					return current, scanErr
				}
				current = next
			}
		}
	}
	return current, nil
}

func (s *scanner) scanInclude(ctx context.Context, argument string, source Location, depth int, provenance []IncludeFrame, constraints []Guard, initial guardState) (guardState, error) {
	expanded, err := s.expandInclude(argument)
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "include_expansion_failed", Message: err.Error(), Path: source.Path, Source: &source,
			Incomplete: true, BlocksMutation: true,
		})
		s.inventory.Includes = append(s.inventory.Includes, IncludeEdge{
			Source: source, Argument: argument, Guards: cloneGuards(constraints),
		})
		return initial, nil
	}
	matches, err := filepath.Glob(expanded)
	if err != nil {
		s.addDiagnostic(Diagnostic{
			Code: "include_glob_invalid", Message: fmt.Sprintf("invalid Include glob %q: %v", argument, err),
			Path: source.Path, Source: &source, Incomplete: true, BlocksMutation: true,
		})
		s.inventory.Includes = append(s.inventory.Includes, IncludeEdge{
			Source: source, Argument: argument, Guards: cloneGuards(constraints),
		})
		return initial, nil
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		s.inventory.Includes = append(s.inventory.Includes, IncludeEdge{
			Source: source, Argument: argument, Guards: cloneGuards(constraints),
		})
		s.addDiagnostic(Diagnostic{
			Code: "include_no_match", Message: fmt.Sprintf("Include %q currently matches no files", argument),
			Path: source.Path, Source: &source,
		})
		return initial, nil
	}
	if len(matches) > s.options.MaxGlobMatches {
		s.addDiagnostic(Diagnostic{
			Code: "glob_match_limit_exceeded", Message: fmt.Sprintf("Include %q matches more than %d files", argument, s.options.MaxGlobMatches),
			Path: source.Path, Source: &source, Incomplete: true, BlocksMutation: true,
		})
		matches = matches[:s.options.MaxGlobMatches]
	}

	flow := initial
	for _, match := range matches {
		clean, absErr := filepath.Abs(match)
		if absErr != nil {
			s.addDiagnostic(Diagnostic{
				Code: "include_path_invalid", Message: fmt.Sprintf("cannot make Include path absolute: %v", absErr),
				Path: match, Source: &source, Incomplete: true, BlocksMutation: true,
			})
			continue
		}
		clean = filepath.Clean(clean)
		info, statErr := os.Lstat(clean)
		if statErr != nil {
			s.addDiagnostic(Diagnostic{
				Code: "include_unreadable", Message: fmt.Sprintf("cannot inspect Include match: %v", statErr),
				Path: clean, Source: &source, Incomplete: true, BlocksMutation: true,
			})
			continue
		}
		cycle := s.inStack(info)
		identity := sourceIdentityPath(clean)
		edge := IncludeEdge{
			Source: source, Argument: argument, Resolved: clean, Guards: cloneGuards(constraints),
			Cycle: cycle, Repeated: s.visits[identity] > 0,
		}
		s.inventory.Includes = append(s.inventory.Includes, edge)
		if cycle {
			s.addDiagnostic(Diagnostic{
				Code: "include_cycle", Message: "Include cycle stopped before revisiting an active source",
				Path: clean, Source: &source, Incomplete: true, BlocksMutation: true,
			})
			continue
		}
		frame := IncludeFrame{
			Source: source, Argument: argument, Resolved: clean, Guards: cloneGuards(constraints),
		}
		childProvenance := append(cloneProvenance(provenance), frame)
		next, scanErr := s.scanFile(ctx, clean, depth+1, childProvenance, constraints, flow)
		if scanErr != nil {
			return flow, scanErr
		}
		flow = next
	}
	return flow, nil
}

func (s *scanner) recordHost(patterns []string, source Location, provenance []IncludeFrame, constraints []Guard) {
	declaration := HostDeclaration{
		Patterns: append([]string(nil), patterns...), Source: source, Provenance: cloneProvenance(provenance),
	}
	for _, raw := range patterns {
		for _, pattern := range splitPatternList(raw) {
			if pattern == "" || strings.HasPrefix(pattern, "!") || hasHostPattern(pattern) {
				continue
			}
			declaration.ExactAliases = append(declaration.ExactAliases, pattern)
		}
	}
	recordKey := hostRecordKey(source, patterns, provenance)
	_, declarationRepeated := s.declarationSeen[recordKey]
	if !declarationRepeated {
		s.declarationSeen[recordKey] = struct{}{}
		s.inventory.Declarations = append(s.inventory.Declarations, declaration)
	}
	if len(declaration.ExactAliases) == 0 {
		if !declarationRepeated {
			s.addDiagnostic(Diagnostic{
				Code: "wildcard_only_host", Message: "Host declaration has no selectable exact positive alias",
				Path: source.Path, Source: &source,
			})
		}
		return
	}
	for _, aliasName := range declaration.ExactAliases {
		reachability := evaluateReachability(aliasName, constraints)
		definition := AliasDefinition{
			Alias: aliasName, Patterns: append([]string(nil), patterns...), Source: source,
			Provenance: cloneProvenance(provenance), Reachability: reachability, Ownership: OwnershipForeign,
		}
		if reachability == Unreachable {
			s.addDiagnostic(Diagnostic{
				Code: "unreachable_alias", Message: fmt.Sprintf("exact alias %q cannot satisfy the guards on its Include path", aliasName),
				Path: source.Path, Source: &source,
			})
			continue
		}
		definitionKey := hostRecordKey(source, patterns, nil) + "\x00" + foldAlias(aliasName)
		if _, repeated := s.definitionSeen[definitionKey]; repeated {
			continue
		}
		s.definitionSeen[definitionKey] = struct{}{}
		key := foldAlias(aliasName)
		group := s.aliasGroups[key]
		if group == nil {
			group = &Alias{Name: aliasName}
			s.aliasGroups[key] = group
		}
		group.Definitions = append(group.Definitions, definition)
	}
}

func (s *scanner) finish() {
	for _, alias := range s.aliasGroups {
		for index := range alias.Definitions {
			alias.Definitions[index].Ownership = s.service.classifyDiscoveredOwnership(alias.Definitions[index])
			if alias.Definitions[index].Ownership == OwnershipConflict {
				alias.Conflict = true
			}
		}
		if len(alias.Definitions) > 1 {
			alias.Conflict = true
		}
		s.inventory.Aliases = append(s.inventory.Aliases, *alias)
	}
	sort.SliceStable(s.inventory.Aliases, func(i, j int) bool {
		left, right := foldAlias(s.inventory.Aliases[i].Name), foldAlias(s.inventory.Aliases[j].Name)
		if left == right {
			return s.inventory.Aliases[i].Name < s.inventory.Aliases[j].Name
		}
		return left < right
	})
}

func (s *scanner) addDiagnostic(diagnostic Diagnostic) {
	if diagnostic.Incomplete {
		s.inventory.Complete = false
	}
	if diagnostic.Source != nil {
		copy := *diagnostic.Source
		diagnostic.Source = &copy
	}
	s.inventory.Diagnostics = append(s.inventory.Diagnostics, diagnostic)
}

func (s *scanner) inStack(info fs.FileInfo) bool {
	for _, active := range s.stack {
		if os.SameFile(active.info, info) {
			return true
		}
	}
	return false
}

func (s *scanner) expandInclude(argument string) (string, error) {
	if argument == "" {
		return "", fmt.Errorf("Include path is empty")
	}
	expanded, err := expandEnvironment(argument, s.options.Environment)
	if err != nil {
		return "", err
	}
	expanded, err = expandPercentD(expanded, s.service.paths.Home)
	if err != nil {
		return "", err
	}
	expanded, err = s.expandTilde(expanded)
	if err != nil {
		return "", err
	}
	expanded = filepath.FromSlash(expanded)
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(s.service.paths.SSHDir, expanded)
	}
	return filepath.Clean(expanded), nil
}

func (s *scanner) expandTilde(path string) (string, error) {
	if path == "~" {
		return s.service.paths.Home, nil
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(s.service.paths.Home, filepath.FromSlash(path[2:])), nil
	}
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	end := strings.IndexAny(path, `/\`)
	name := path[1:]
	rest := ""
	if end >= 0 {
		name = path[1:end]
		rest = path[end+1:]
	}
	if name == "" {
		return "", fmt.Errorf("invalid tilde expansion %q", path)
	}
	home, err := s.options.LookupHome(name)
	if err != nil || home == "" {
		if err == nil {
			err = errors.New("lookup returned an empty home")
		}
		return "", fmt.Errorf("cannot expand ~%s: %w", name, err)
	}
	if rest == "" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(rest)), nil
}

func expandEnvironment(input string, lookup func(string) (string, bool)) (string, error) {
	var output strings.Builder
	for index := 0; index < len(input); {
		if input[index] != '$' || index+1 >= len(input) || input[index+1] != '{' {
			output.WriteByte(input[index])
			index++
			continue
		}
		end := strings.IndexByte(input[index+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated environment expansion in %q", input)
		}
		end += index + 2
		name := input[index+2 : end]
		if !validEnvironmentName(name) {
			return "", fmt.Errorf("invalid environment variable %q in Include", name)
		}
		value, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("environment variable %s used by Include is not set", name)
		}
		output.WriteString(value)
		index = end + 1
	}
	return output.String(), nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if index == 0 && r != '_' && !unicode.IsLetter(r) {
			return false
		}
		if index > 0 && r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func expandPercentD(input, home string) (string, error) {
	var output strings.Builder
	for index := 0; index < len(input); index++ {
		if input[index] != '%' {
			output.WriteByte(input[index])
			continue
		}
		if index+1 >= len(input) {
			return "", fmt.Errorf("trailing %% token in Include %q", input)
		}
		index++
		switch input[index] {
		case '%':
			output.WriteByte('%')
		case 'd':
			output.WriteString(home)
		default:
			return "", fmt.Errorf("Include token %%%c is host-dependent or unsupported by static discovery", input[index])
		}
	}
	return output.String(), nil
}

func sourceIdentityPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func evaluateReachability(alias string, guards []Guard) Reachability {
	unknown := false
	for _, guard := range guards {
		switch guard.Kind {
		case GuardHost:
			if !patternListMatches(alias, guard.Arguments) {
				return Unreachable
			}
		case GuardMatch:
			matched, known := evaluateMatchGuard(alias, guard.Arguments)
			if known && !matched {
				return Unreachable
			}
			if !known {
				unknown = true
			}
		default:
			unknown = true
		}
	}
	if unknown {
		return Unknown
	}
	return Reachable
}

func staticMatchGuard(arguments []string) bool {
	_, known := evaluateMatchGuard("static-probe", arguments)
	return known
}

func evaluateMatchGuard(alias string, arguments []string) (bool, bool) {
	if len(arguments) == 1 && strings.EqualFold(arguments[0], "all") {
		return true, true
	}
	if len(arguments) == 2 && strings.EqualFold(arguments[0], "originalhost") {
		return patternListMatches(alias, []string{arguments[1]}), true
	}
	// OpenSSH evaluates Match host against the resolved HostName, not the
	// original lookup token. Static discovery does not currently carry a
	// target-specific, first-value-wins HostName proof through Include edges,
	// so treating the alias as the host would be unsound.
	return false, false
}

func patternListMatches(alias string, raw []string) bool {
	positive := false
	matched := false
	for _, item := range raw {
		for _, pattern := range splitPatternList(item) {
			negated := strings.HasPrefix(pattern, "!")
			if negated {
				pattern = strings.TrimPrefix(pattern, "!")
			}
			if pattern == "" {
				continue
			}
			if !negated {
				positive = true
			}
			if hostPatternMatches(pattern, alias) {
				if negated {
					return false
				}
				matched = true
			}
		}
	}
	return positive && matched
}

func hostPatternMatches(pattern, alias string) bool {
	pattern = foldAlias(pattern)
	alias = foldAlias(alias)
	// OpenSSH host patterns use '*' and '?'. Brackets are treated as ordinary
	// bytes by the matcher but still make a declaration non-exact elsewhere so a
	// future OpenSSH extension cannot be mistaken for a safe exact collision.
	p, a := 0, 0
	star, checkpoint := -1, 0
	for a < len(alias) {
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == alias[a]) {
			p++
			a++
			continue
		}
		if p < len(pattern) && pattern[p] == '*' {
			star = p
			checkpoint = a
			p++
			continue
		}
		if star >= 0 {
			p = star + 1
			checkpoint++
			a = checkpoint
			continue
		}
		return false
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func splitPatternList(value string) []string {
	parts := strings.Split(value, ",")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func hasHostPattern(value string) bool {
	return strings.ContainsAny(value, "*?[]")
}

func foldAlias(value string) string      { return strings.ToLower(value) }
func equalAlias(left, right string) bool { return strings.EqualFold(left, right) }

func hostRecordKey(source Location, patterns []string, provenance []IncludeFrame) string {
	var key strings.Builder
	fmt.Fprintf(&key, "%q:%d", sourceIdentityPath(source.Path), source.Line)
	for _, pattern := range patterns {
		fmt.Fprintf(&key, "|p=%q", pattern)
	}
	for _, frame := range provenance {
		fmt.Fprintf(&key, "|f=%q:%d:%q:%q", sourceIdentityPath(frame.Source.Path), frame.Source.Line, frame.Argument, sourceIdentityPath(frame.Resolved))
		for _, guard := range frame.Guards {
			fmt.Fprintf(&key, "|g=%q:%d:%q:%t", sourceIdentityPath(guard.Source.Path), guard.Source.Line, guard.Kind, guard.Dynamic)
			for _, argument := range guard.Arguments {
				fmt.Fprintf(&key, ":%q", argument)
			}
		}
	}
	return key.String()
}

func cloneGuard(guard Guard) Guard {
	guard.Arguments = append([]string(nil), guard.Arguments...)
	return guard
}

func cloneGuards(guards []Guard) []Guard {
	copy := make([]Guard, len(guards))
	for index, guard := range guards {
		copy[index] = cloneGuard(guard)
	}
	return copy
}

func cloneProvenance(provenance []IncludeFrame) []IncludeFrame {
	copy := make([]IncludeFrame, len(provenance))
	for index, frame := range provenance {
		copy[index] = frame
		copy[index].Guards = cloneGuards(frame.Guards)
	}
	return copy
}

func validUTF8NoControl(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func stableScannedInfo(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func guardsEqual(left, right Guard) bool {
	if left.Kind != right.Kind || left.Source != right.Source || left.Dynamic != right.Dynamic || len(left.Arguments) != len(right.Arguments) {
		return false
	}
	for index := range left.Arguments {
		if left.Arguments[index] != right.Arguments[index] {
			return false
		}
	}
	return true
}
