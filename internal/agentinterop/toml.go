package agentinterop

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/pelletier/go-toml/v2/unstable"
)

func tomlServer(data []byte, name string) (map[string]json.RawMessage, bool, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, false, errors.New("malformed TOML configuration")
	}
	servers, ok := doc["mcp_servers"].(map[string]any)
	if !ok {
		if _, exists := doc["mcp_servers"]; exists {
			return nil, false, errors.New("mcp_servers must be a table")
		}
		return nil, false, nil
	}
	v, ok := servers[name]
	if !ok {
		return nil, false, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false, ErrUnsupported
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return nil, false, errors.New("MCP server must be a table")
	}
	return fields, true, nil
}

// patchTOML uses a pinned parser for real table boundaries (including quoted
// names and multiline values). Inline/dotted selected definitions fail closed;
// arbitrary whole-document decoding/re-encoding is never used for writes.
func patchTOML(data []byte, name string, fields map[string]any, remove bool) ([]byte, error) {
	_, exists, err := tomlServer(data, name)
	if err != nil {
		return nil, err
	}
	type block struct {
		start, end int
		selected   bool
	}
	var blocks []block
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	pendingComment := -1
	selectedSeen := false
	currentKeys := []string{}
	lineStart := func(pos int) int {
		for pos > 0 && data[pos-1] != '\n' {
			pos--
		}
		return pos
	}
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.Comment {
			start := int(n.Raw.Offset)
			line := lineStart(start)
			if len(bytes.TrimSpace(data[line:start])) == 0 && pendingComment < 0 {
				pendingComment = line
			}
			continue
		}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			keys := []string{}
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
				return nil, ErrUnsupported
			}
			start := lineStart(offset)
			if len(blocks) > 0 {
				end := start
				if pendingComment >= 0 {
					end = pendingComment
				}
				blocks[len(blocks)-1].end = end
			}
			selected := len(keys) >= 2 && keys[0] == "mcp_servers" && keys[1] == name
			if selected && n.Kind == unstable.ArrayTable {
				return nil, errors.New("array MCP table layout is unsupported")
			}
			selectedSeen = selectedSeen || selected
			blocks = append(blocks, block{start, len(data), selected})
			currentKeys = keys
		} else if n.Kind == unstable.KeyValue {
			keys := []string{}
			it := n.Key()
			for it.Next() {
				keys = append(keys, string(it.Node().Data))
			}
			full := append(append([]string{}, currentKeys...), keys...)
			if len(full) == 1 && full[0] == "mcp_servers" {
				return nil, errors.New("inline mcp_servers layout needs a native edit")
			}
			if len(currentKeys) < 2 && len(full) >= 2 && full[0] == "mcp_servers" && full[1] == name {
				return nil, errors.New("inline or dotted selected server layout needs a native edit")
			}
		}
		pendingComment = -1
	}
	if parser.Error() != nil {
		return nil, errors.New("TOML layout is outside the supported parser profile")
	}
	if len(blocks) > 0 && pendingComment >= 0 {
		blocks[len(blocks)-1].end = pendingComment
	}
	if exists && !selectedSeen {
		return nil, errors.New("selected server does not have a safely editable table range")
	}
	var replacement []byte
	if !remove {
		var buf bytes.Buffer
		if err = toml.NewEncoder(&buf).Encode(map[string]any{"mcp_servers": map[string]any{name: fields}}); err != nil {
			return nil, errors.New("cannot encode MCP server")
		}
		lines := strings.Split(buf.String(), "\n")
		var keep []string
		for _, line := range lines {
			if strings.TrimSpace(line) != "[mcp_servers]" {
				keep = append(keep, line)
			}
		}
		replacement = []byte(strings.TrimLeft(strings.Join(keep, "\n"), "\n") + "\n")
		if bytes.Contains(data, []byte("\r\n")) {
			replacement = bytes.ReplaceAll(replacement, []byte("\n"), []byte("\r\n"))
		}
	}
	var out bytes.Buffer
	cursor := 0
	inserted := false
	for _, block := range blocks {
		if !block.selected {
			continue
		}
		out.Write(data[cursor:block.start])
		if !inserted {
			out.Write(replacement)
			inserted = true
		}
		cursor = block.end
	}
	out.Write(data[cursor:])
	if !inserted && !remove {
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteByte('\n')
		}
		out.Write(replacement)
	}
	if _, _, err = tomlServer(out.Bytes(), name); err != nil {
		return nil, errors.New("TOML patch would change table semantics; use a native edit")
	}
	return out.Bytes(), nil
}
