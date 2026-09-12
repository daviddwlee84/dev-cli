package sshhost

import (
	"context"
	"strconv"
	"strings"
	"unicode"
)

// Bound the cross-alias guard analysis as well as the scanner's input bytes.
// Large or adversarial configurations still provide honest unknown rows.
const maxConnectionHintComparisons = 1 << 20

// ConnectionHints performs the same bounded static scan as Discover. It never
// evaluates ssh -G, Match exec, DNS, or a provider, and never rereads a source to
// recover fields after the scanner has validated its open-file snapshot.
func (s *Service) ConnectionHints(ctx context.Context) ([]ConnectionHint, error) {
	inv, err := s.Discover(ctx)
	return inv.ConnectionHints, err
}

type connectionLine struct {
	keyword     string
	arguments   []string
	source      Location
	current     guardState
	constraints []Guard
}

func (s *scanner) recordConnectionLine(directive string, arguments []string, source Location, current guardState, constraints []Guard) {
	keyword := strings.ToLower(directive)
	switch keyword {
	case "hostname", "user", "port", "proxycommand", "proxyjump", "canonicalizehostname":
	default:
		return
	}
	s.connectionLines = append(s.connectionLines, connectionLine{
		keyword: keyword, arguments: append([]string(nil), arguments...), source: source,
		current: guardState{guard: cloneGuard(current.guard), set: current.set}, constraints: cloneGuards(constraints),
	})
}

func (s *scanner) finishConnectionHints() {
	fingerprint := digestBytes([]byte(strings.Join(s.sourceDigests, "\n")))
	bounded := len(s.connectionLines) == 0 || len(s.inventory.Aliases) <= maxConnectionHintComparisons/len(s.connectionLines)
	for _, alias := range s.inventory.Aliases {
		hint := ConnectionHint{Alias: alias.Name, State: "unknown", Fingerprint: fingerprint}
		if len(alias.Definitions) == 0 {
			s.inventory.ConnectionHints = append(s.inventory.ConnectionHints, hint)
			continue
		}
		definition := alias.Definitions[0]
		hint.Source = definition.Source
		// Public hints expose source locations, not command-like Match guards or
		// original Include expressions that a joined inventory might serialize.
		for _, frame := range definition.Provenance {
			hint.Provenance = append(hint.Provenance, IncludeFrame{Source: frame.Source, Resolved: frame.Resolved})
		}
		known := bounded && s.inventory.Complete && !alias.Conflict && len(alias.Definitions) == 1 &&
			definition.Reachability == Reachable && simpleExactPatterns(definition.Patterns)
		if !known {
			s.inventory.ConnectionHints = append(s.inventory.ConnectionHints, hint)
			continue
		}
		seen := map[string]bool{}
		for _, line := range s.connectionLines {
			guards := cloneGuards(line.constraints)
			if line.current.set {
				guards = append(guards, line.current.guard)
			}
			if evaluateReachability(alias.Name, guards) == Unreachable {
				continue
			}
			if len(line.arguments) == 1 && line.keyword == "canonicalizehostname" && strings.EqualFold(line.arguments[0], "no") {
				continue
			}
			if len(line.arguments) == 1 && (line.keyword == "proxycommand" || line.keyword == "proxyjump") && strings.EqualFold(line.arguments[0], "none") {
				continue
			}
			// A global, wildcard, Match, inherited Include or different Host
			// block can affect semantics beyond this literal observation.
			if !line.current.set || line.current.guard.Kind != GuardHost ||
				line.current.guard.Source != definition.Source || len(line.constraints) != 0 ||
				!simpleExactPatterns(line.current.guard.Arguments) || len(line.arguments) != 1 ||
				line.source.Path != definition.Source.Path || seen[line.keyword] {
				known = false
				continue
			}
			seen[line.keyword] = true
			value := line.arguments[0]
			if !literalConnectionValue(value) {
				known = false
				continue
			}
			switch line.keyword {
			case "hostname":
				hint.HostName = value
			case "user":
				hint.User = value
			case "port":
				port, err := strconv.Atoi(value)
				if err != nil || port < 1 || port > 65535 {
					known = false
				} else {
					hint.Port = port
				}
			default:
				known = false
			}
		}
		if known && hint.HostName != "" {
			hint.State = "known"
		} else {
			hint.HostName, hint.User, hint.Port = "", "", 0
		}
		s.inventory.ConnectionHints = append(s.inventory.ConnectionHints, hint)
	}
}

func simpleExactPatterns(patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "!") || hasHostPattern(pattern) || strings.Contains(pattern, ",") {
			return false
		}
	}
	return true
}

func literalConnectionValue(value string) bool {
	return value != "" && len(value) <= 255 && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "%$`\\\"'@*?[]!/") &&
		!strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}
