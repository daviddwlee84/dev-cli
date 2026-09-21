package hygiene

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Only recognize bundled rules and the inherited default API-key rule. A rule
// with the same ID but customized semantics must retain its scanner results.
func sourceContextRules(config []byte) map[string]bool {
	type document struct {
		Extend struct {
			UseDefault bool
			Path       string
			URL        string
		}
		Rules []map[string]any
	}
	var current, defaults document
	if _, err := toml.Decode(string(config), &current); err != nil || current.Extend.Path != "" || current.Extend.URL != "" {
		return nil
	}
	if _, err := toml.Decode(string(DefaultGitleaks), &defaults); err != nil {
		return nil
	}
	allowed := map[string]bool{"generic-api-key": current.Extend.UseDefault}
	counts := map[string]int{}
	for _, rule := range current.Rules {
		id, _ := rule["id"].(string)
		counts[id]++
		switch id {
		case "generic-api-key":
			allowed[id] = false
		case "generic-password-assignment", "generic-password-assignment-single-quoted":
			for _, original := range defaults.Rules {
				if original["id"] == id {
					allowed[id] = reflect.DeepEqual(rule, original)
				}
			}
		}
	}
	for id, count := range counts {
		if count > 1 {
			allowed[id] = false
		}
	}
	return allowed
}

type goSourceContext struct {
	protected [][2]int
	valid     bool
}

func parseGoSource(data []byte) goSourceContext {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "source.go", data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return goSourceContext{}
	}
	c := goSourceContext{valid: true}
	protect := func(node ast.Node) {
		start := fset.PositionFor(node.Pos(), false).Offset
		end := fset.PositionFor(node.End(), false).Offset
		// AST literal/comment text drops carriage returns. Locate raw-string
		// and comment ends in the original bytes so CRLF cannot expose a tail.
		switch {
		case data[start] == '`':
			end = start + 2 + bytes.IndexByte(data[start+1:], '`')
		case bytes.HasPrefix(data[start:], []byte("/*")):
			end = start + 4 + bytes.Index(data[start+2:], []byte("*/"))
		case bytes.HasPrefix(data[start:], []byte("//")):
			end = len(data)
			if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
				end = start + i
			}
		}
		c.protected = append(c.protected, [2]int{start, end})
	}
	ast.Inspect(f, func(node ast.Node) bool {
		if _, ok := node.(*ast.BasicLit); ok {
			protect(node)
		}
		return true
	})
	// Detached comments are not visited through the declaration AST.
	for _, group := range f.Comments {
		for _, comment := range group.List {
			protect(comment)
		}
	}
	return c
}

func (c goSourceContext) codeOnly(data []byte, d Detection) bool {
	if !c.valid {
		return false
	}
	spans := secretSpans(data, d)
	if len(spans) == 0 {
		return false
	}
	// Check every occurrence within the reported lines. If the same text also
	// occurs in a literal/comment, retain the whole finding rather than guessing.
	for _, span := range spans {
		for _, protected := range c.protected {
			if span[0] < protected[1] && protected[0] < span[1] {
				return false
			}
		}
	}
	return true
}

var sourceCommit = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// filterGoSourceFindings reads only scanner-observed Git objects, never the
// working file. Grouping bounds memory to one source and parses each object once.
// Missing, oversized, invalid or ambiguous source preserves the finding.
func filterGoSourceFindings(ctx context.Context, q EngineRequest, rows []Detection) ([]Detection, error) {
	allowed := sourceContextRules(q.Config)
	groups := map[string][]int{}
	for i, d := range rows {
		file := filepath.ToSlash(d.File)
		if !allowed[d.RuleID] || !validRelative(file) || filepath.Ext(file) != ".go" {
			continue
		}
		object := ":" + file
		if q.History {
			if !sourceCommit.MatchString(d.Commit) {
				continue
			}
			object = d.Commit + ":" + file
		}
		groups[object] = append(groups[object], i)
	}
	objects := make([]string, 0, len(groups))
	for object := range groups {
		objects = append(objects, object)
	}
	sort.Strings(objects)
	discard := make([]bool, len(rows))
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size, err := runGitBytes(ctx, q.Root, nil, !q.History, "cat-file", "-s", object)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
		if err != nil || n < 0 || n > MaxFileBytes {
			continue
		}
		data, err := runGitBytes(ctx, q.Root, nil, !q.History, "cat-file", "blob", object)
		if err != nil || int64(len(data)) != n {
			continue
		}
		c := parseGoSource(data)
		for _, i := range groups[object] {
			discard[i] = c.codeOnly(data, rows[i])
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kept := make([]Detection, 0, len(rows))
	for i, d := range rows {
		if !discard[i] {
			kept = append(kept, d)
		}
	}
	return kept, nil
}
