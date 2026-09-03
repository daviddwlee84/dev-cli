// Package textmatch provides the small query language shared by interactive
// lists. It deliberately matches terms rather than assigning fuzzy scores so
// filtering stays predictable across dev's built-in interfaces.
package textmatch

import "strings"

// Terms reports whether every whitespace-separated query term appears in the
// haystack, case-insensitively and in any order. An empty query matches all.
func Terms(haystack, query string) bool {
	return TermsFolded(strings.ToLower(haystack), strings.ToLower(query))
}

// TermsFolded is Terms for callers that cache lowercase haystacks and queries.
func TermsFolded(haystack, query string) bool {
	query = strings.TrimSpace(query)
	for term := range strings.FieldsSeq(query) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}
