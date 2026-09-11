package config

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Parse reads an in-memory config with the same defaults and validation as Load.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

// PatchDiscoveryPath appends one literal path without re-encoding the document.
// Only conventional [paths] tables are edited; unsupported layouts remain intact.
func PatchDiscoveryPath(data []byte, key, path string) ([]byte, error) {
	if key != "repo_paths" && key != "scan_roots" {
		return nil, errors.New("expected repo_paths or scan_roots")
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	values := cfg.Paths.RepoPaths
	if key == "scan_roots" {
		values = cfg.Paths.ScanRoots
	}
	for _, old := range values {
		if old == path {
			return bytes.Clone(data), nil
		}
	}
	desired := append(append([]string(nil), values...), path)
	want := cfg
	if key == "scan_roots" {
		want.Paths.ScanRoots = desired
	} else {
		want.Paths.RepoPaths = desired
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	inPaths, tableEnd := false, -1
	var result []byte
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable && n.Kind != unstable.KeyValue {
			continue
		}
		var keys []string
		keyEnd := 0
		it := n.Key()
		for it.Next() {
			part := it.Node()
			keys = append(keys, string(part.Data))
			keyEnd = int(part.Raw.Offset + part.Raw.Length)
		}
		if n.Kind != unstable.KeyValue {
			inPaths = len(keys) == 1 && keys[0] == "paths" && n.Kind == unstable.Table
			if inPaths {
				tableEnd = len(data)
				if e := bytes.IndexByte(data[keyEnd:], '\n'); e >= 0 {
					tableEnd = keyEnd + e + 1
				}
			}
			continue
		}
		if !inPaths && len(keys) > 0 && keys[0] == "paths" {
			return nil, errors.New("inline or dotted paths configuration needs a manual edit")
		}
		if !inPaths || len(keys) != 1 || keys[0] != key {
			continue
		}
		if n.Value().Kind != unstable.Array {
			return nil, errors.New("discovery paths must be an array")
		}
		open := keyEnd
		for open < len(data) && data[open] != '[' {
			open++
		}
		lastEnd := open + 1
		children := n.Value().Children()
		for children.Next() {
			child := children.Node()
			if child.Kind == unstable.String {
				lastEnd = int(child.Raw.Offset + child.Raw.Length)
			}
		}
		closeAt, trailingComma := lastEnd, false
		for closeAt < len(data) && data[closeAt] != ']' {
			if data[closeAt] == '#' {
				for closeAt < len(data) && data[closeAt] != '\n' {
					closeAt++
				}
				continue
			}
			trailingComma = trailingComma || data[closeAt] == ','
			closeAt++
		}
		if open >= len(data) || closeAt >= len(data) {
			return nil, errors.New("cannot locate discovery array boundaries")
		}
		literal, err := discoveryLiteral(path)
		if err != nil {
			return nil, err
		}
		result = bytes.Clone(data)
		insert := closeAt
		addition := literal
		if bytes.Contains(data[open:closeAt], []byte("\n")) {
			line := bytes.LastIndexByte(data[:closeAt], '\n') + 1
			if len(bytes.TrimSpace(data[line:closeAt])) == 0 {
				insert = line
				addition = string(data[line:closeAt]) + "  " + literal + "," + newline
			} else {
				addition = newline + "  " + literal + "," + newline
			}
		} else if len(values) > 0 {
			addition = " " + literal
			if trailingComma {
				addition += ","
			}
		}
		result = append(append(bytes.Clone(result[:insert]), []byte(addition)...), result[insert:]...)
		if len(values) > 0 && !trailingComma {
			result = append(append(bytes.Clone(result[:lastEnd]), ','), result[lastEnd:]...)
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	if result == nil {
		var encoded bytes.Buffer
		if err := toml.NewEncoder(&encoded).Encode(map[string][]string{key: desired}); err != nil {
			return nil, err
		}
		line := strings.ReplaceAll(encoded.String(), "\n", newline)
		if tableEnd < 0 {
			result = append(bytes.Clone(data), []byte(newline+"[paths]"+newline+line)...)
		} else {
			prefix := bytes.Clone(data[:tableEnd])
			if len(prefix) > 0 && prefix[len(prefix)-1] != '\n' {
				prefix = append(prefix, []byte(newline)...)
			}
			result = append(append(prefix, []byte(line)...), data[tableEnd:]...)
		}
	}
	got, err := Parse(result)
	if err != nil || !reflect.DeepEqual(got, want) {
		return nil, fmt.Errorf("discovery edit did not preserve configuration semantics: %v", err)
	}
	return result, nil
}

func discoveryLiteral(path string) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]string{"path": path}); err != nil {
		return "", err
	}
	_, value, _ := strings.Cut(b.String(), "=")
	return strings.TrimSpace(value), nil
}
