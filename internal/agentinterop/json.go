package agentinterop

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tailscale/hujson"
)

func jsonDocument(data []byte, comments bool) (hujson.Value, error) {
	if len(data) == 0 {
		data = []byte("{}\n")
	}
	v, err := hujson.Parse(data)
	if err != nil {
		return v, errors.New("malformed JSON configuration")
	}
	if !comments && !v.IsStandard() {
		return v, errors.New("comments are not supported by this configuration format")
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return v, errors.New("configuration must be a JSON object")
	}
	for node := range v.All() {
		if obj, ok := node.Value.(*hujson.Object); ok {
			seen := map[string]bool{}
			for _, m := range obj.Members {
				var key string
				literal, ok := m.Name.Value.(hujson.Literal)
				if !ok || json.Unmarshal(literal, &key) != nil {
					return v, errors.New("invalid JSON member")
				}
				if seen[key] {
					return v, errors.New("duplicate JSON members are ambiguous")
				}
				seen[key] = true
			}
		}
	}
	return v, nil
}

func pointer(parts ...string) string {
	out := ""
	for _, part := range parts {
		out += "/" + strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return out
}

func jsonMap(node *hujson.Value) (map[string]json.RawMessage, error) {
	if node == nil {
		return nil, errors.New("selected JSON object is missing")
	}
	v := node.Clone()
	v.Standardize()
	var result map[string]json.RawMessage
	if json.Unmarshal(v.Pack(), &result) != nil || result == nil {
		return nil, errors.New("selected declaration must be an object")
	}
	return result, nil
}

// patchJSON uses a syntax tree, retaining unrelated bytes, comments and order.
func patchJSON(data []byte, comments bool, parts []string, value any, remove bool) ([]byte, error) {
	v, err := jsonDocument(data, comments)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("cannot replace the entire configuration")
	}
	patch := func(op, path string, val any) error {
		change := map[string]any{"op": op, "path": path}
		if op != "remove" {
			change["value"] = val
		}
		b, _ := json.Marshal([]any{change})
		if v.Patch(b) != nil {
			return errors.New("configuration patch could not be applied")
		}
		return nil
	}
	for i := 1; i < len(parts); i++ {
		p := pointer(parts[:i]...)
		node := v.Find(p)
		if node == nil {
			if remove {
				return nil, errors.New("source declaration disappeared")
			}
			if err = patch("add", p, map[string]any{}); err != nil {
				return nil, err
			}
		} else if _, ok := node.Value.(*hujson.Object); !ok {
			return nil, errors.New("configuration parent is not an object")
		}
	}
	p := pointer(parts...)
	op := "add"
	if v.Find(p) != nil {
		op = "replace"
	}
	if remove {
		op = "remove"
	}
	if err = patch(op, p, value); err != nil {
		return nil, err
	}
	out := v.Pack()
	if _, err = jsonDocument(out, comments); err != nil {
		return nil, err
	}
	return out, nil
}
