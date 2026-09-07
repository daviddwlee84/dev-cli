package agentinterop

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func resolveMCP(ref ArtifactRef) (ArtifactRef, error) {
	known := false
	for _, p := range MCPProfiles() {
		if p.Agent == ref.Agent {
			known = true
		}
	}
	if !known {
		return ref, errors.New("choose an MCP agent: claude-code, codex, cursor, gemini-cli, opencode")
	}
	if ref.Path != "" {
		return ref, nil
	}
	if ref.Scope == "project" {
		paths := map[string]string{"claude-code": ".mcp.json", "codex": ".codex/config.toml", "cursor": ".cursor/mcp.json", "gemini-cli": ".gemini/settings.json", "opencode": "opencode.json"}
		ref.Path = filepath.FromSlash(paths[ref.Agent])
		if ref.Agent == "opencode" {
			var found []string
			for _, candidate := range []string{"opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc"} {
				if _, err := os.Lstat(filepath.Join(ref.Root, filepath.FromSlash(candidate))); err == nil {
					found = append(found, candidate)
				} else if !os.IsNotExist(err) {
					return ref, err
				}
			}
			if len(found) > 1 {
				return ref, errors.New("multiple OpenCode files; select --from or --to explicitly")
			}
			if len(found) == 1 {
				ref.Path = filepath.FromSlash(found[0])
			}
		}
		return ref, nil
	}
	home, _ := os.UserHomeDir()
	var path string
	switch ref.Agent {
	case "claude-code":
		path = filepath.Join(home, ".claude.json")
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
			path = filepath.Join(dir, ".claude.json")
		}
	case "codex":
		dir := os.Getenv("CODEX_HOME")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, ".codex")
		}
		path = filepath.Join(dir, "config.toml")
	case "cursor":
		path = filepath.Join(home, ".cursor", "mcp.json")
	case "gemini-cli":
		path = filepath.Join(home, ".gemini", "settings.json")
	case "opencode":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "opencode", "opencode.json")
		_, a := os.Lstat(path)
		_, z := os.Lstat(path + "c")
		if a == nil && z == nil {
			return ref, errors.New("multiple OpenCode user files; select an explicit path")
		}
		if a != nil && z == nil {
			path += "c"
		}
	}
	return absoluteArtifact(ref, path)
}

func mcpFields(data []byte, agent, name string) (map[string]json.RawMessage, bool, error) {
	if agent == "codex" {
		return tomlServer(data, name)
	}
	v, err := jsonDocument(data, agent == "opencode")
	if err != nil {
		return nil, false, err
	}
	key := "mcpServers"
	if agent == "opencode" {
		key = "mcp"
		if v.Find("/mcp/servers") != nil {
			return nil, false, errors.New("OpenCode v2 requires a separate compatibility profile; no schema migration is implied")
		}
	}
	node := v.Find(pointer(key, name))
	if node == nil {
		return nil, false, nil
	}
	fields, err := jsonMap(node)
	return fields, true, err
}

func patchMCP(data []byte, agent, name string, fields map[string]any, remove bool) ([]byte, error) {
	if agent == "codex" {
		return patchTOML(data, name, fields, remove)
	}
	key := "mcpServers"
	if agent == "opencode" {
		key = "mcp"
	}
	return patchJSON(data, agent == "opencode", []string{key, name}, fields, remove)
}

func fieldEquivalent(raw map[string]json.RawMessage, want map[string]any) bool {
	b, _ := json.Marshal(want)
	var w map[string]json.RawMessage
	_ = json.Unmarshal(b, &w)
	if len(raw) != len(w) {
		return false
	}
	for key, a := range raw {
		var left, right any
		if json.Unmarshal(a, &left) != nil || json.Unmarshal(w[key], &right) != nil || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

func planMCP(b *builder, owner *record) error {
	req := b.r.Request
	if req.Mode == "move" && (req.EnvFile != "" || req.Launcher != "") {
		return errors.New("a launcher retains its source declaration; use copy/mirror or a native environment mapping before moving")
	}
	var err error
	req.From, err = resolveMCP(req.From)
	if err != nil {
		return err
	}
	req.To, err = resolveMCP(req.To)
	if err != nil {
		return err
	}
	b.r.Request = req
	if err = validateMCPPolicy(b.ctx, req); err != nil {
		return err
	}
	source, target := location(req.From), location(req.To)
	if err = validLocation(source); err != nil {
		return err
	}
	if err = validLocation(target); err != nil {
		return err
	}
	img, data, err := b.read(source)
	if err != nil {
		return err
	}
	if img.Kind != "file" {
		return errors.New("MCP source must be a regular configuration file")
	}
	fields, present, err := mcpFields(data, req.From.Agent, req.Name)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("selected MCP server was not found")
	}
	d, err := parseMCP(req.From.Agent, fields, req.Transport)
	if err != nil {
		return err
	}
	if source == target && req.Name == req.TargetName {
		return nil
	}
	if err = bindDefinition(&d, req); err != nil {
		return err
	}
	if len(d.Env)+len(d.Headers) > 128 {
		return errors.New("MCP environment/header reference limit exceeded")
	}
	refs := []string{}
	for name, value := range d.Env {
		if value.Variable != "" {
			refs = append(refs, "env "+name+" <- "+value.Variable)
		} else {
			refs = append(refs, "env "+name+": literal value omitted from report")
		}
	}
	for name, value := range d.Headers {
		if value.Variable != "" {
			refs = append(refs, "header "+name+" <- "+value.Variable)
		}
	}
	sort.Strings(refs)
	b.r.Notes = append(b.r.Notes, refs...)
	if req.EnvFile != "" {
		b.r.Notes = append(b.r.Notes, "Runtime local env source: "+req.EnvFile+" within the destination scope; credential values are resolved only at launch.")
	}
	if owner == nil && (req.Mode == "mirror" || req.Mode == "move") {
		owner, err = findMCPOwner(b, req, target)
		if err != nil {
			return err
		}
	}
	if owner != nil {
		if (owner.Request.From != req.From || owner.Request.Name != req.Name) && !req.Adopt {
			return errors.New("destination has another managed source; explicit --adopt is required to change that relationship")
		}
		b.r.Parent = owner.ID
	}
	if d.Cwd != "" {
		if filepath.IsAbs(d.Cwd) {
			if req.From.Root != req.To.Root {
				return errors.New("absolute source cwd is not portable across checkouts")
			}
		} else {
			if err = validLocation(Location{req.To.Root, d.Cwd}); err != nil {
				return err
			}
			d.Cwd = filepath.Join(req.To.Root, d.Cwd)
		}
	}
	if req.From.Root != req.To.Root && d.Transport == "stdio" {
		if strings.ContainsAny(d.Command, "/\\") && !filepath.IsAbs(d.Command) {
			return errors.New("relative MCP executable needs an explicit destination-local declaration")
		}
		for _, arg := range d.Args {
			if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
				return errors.New("relative server assets must be provisioned and declared in the destination")
			}
		}
	}
	if req.EnvFile != "" || req.Launcher != "" {
		d, err = bindLauncher(b, d, owner)
		if err != nil {
			return err
		}
	}
	rendered, err := renderMCP(req.To.Agent, d)
	if err != nil {
		return err
	}
	targetImage, targetBytes, err := b.read(target)
	if err != nil {
		return err
	}
	if targetImage.Kind != "file" && targetImage.Kind != "absent" {
		return conflict("MCP target")
	}
	existing, exists, err := mcpFields(targetBytes, req.To.Agent, req.TargetName)
	if err != nil {
		return err
	}
	owned := false
	if owner != nil {
		for _, edit := range owner.MCPEdits {
			if edit.Location == target && edit.Name == req.TargetName {
				owned = true
			}
		}
		for _, e := range owner.Effects {
			if e.Location == target && e.Post != nil {
				owned = true
			}
		}
	}
	if exists && !fieldEquivalent(existing, rendered) && !owned {
		return conflict("same-name foreign MCP declaration")
	}
	var patched []byte
	if exists && fieldEquivalent(existing, rendered) {
		patched = targetBytes
	} else {
		patched, err = patchMCP(targetBytes, req.To.Agent, req.TargetName, rendered, false)
		if err != nil {
			return err
		}
	}
	if req.Mode == "move" && source == target {
		patched, err = patchMCP(patched, req.From.Agent, req.Name, nil, true)
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(patched, targetBytes) {
		if err = b.write(target, patched, 0o600, true); err != nil {
			return err
		}
	}
	if !bytes.Equal(patched, targetBytes) || req.Mode == "move" || req.Mode == "mirror" && (req.Adopt || owner != nil && owner.Request.Mode != "mirror") {
		b.r.MCPEdits = append(b.r.MCPEdits, mcpEdit{Location: target, Agent: req.To.Agent, Name: req.TargetName, Before: existing, After: rawFields(rendered)})
	}
	if req.Mode == "move" {
		b.r.MCPEdits = append(b.r.MCPEdits, mcpEdit{Location: source, Agent: req.From.Agent, Name: req.Name, Before: fields, After: nil})
	}
	if req.Mode == "move" && source != target {
		removed, err := patchMCP(data, req.From.Agent, req.Name, nil, true)
		if err != nil {
			return err
		}
		if err = b.write(source, removed, img.Mode, true); err != nil {
			return err
		}
		if len(b.r.Effects) > 0 && b.r.Effects[len(b.r.Effects)-1].Location == source {
			b.r.Effects[len(b.r.Effects)-1].Retirement = true
		}
	}
	b.r.Notes = append(b.r.Notes, "Configuration only: reload the destination client and retain its normal trust/approval checks. No MCP connection was attempted.")
	b.r.Notes = append(b.r.Notes, "Selected "+req.From.Agent+"/"+req.Name+" -> "+req.To.Agent+"/"+req.TargetName+" ("+d.Transport+").")
	if req.Adopt && req.Mode == "mirror" {
		b.r.Notes = append(b.r.Notes, "Adopt ownership of this equivalent selected stanza; unrelated settings remain user-owned.")
	}
	if d.Transport == "streamable-http" && len(d.Headers) > 0 {
		b.r.Notes = append(b.r.Notes, "HTTP credentials must be available in the destination client environment before it starts; a stdio launcher cannot inject them.")
	}
	return nil
}

func bindDefinition(d *mcpDefinition, req TransferRequest) error {
	bindings := map[string]string{}
	for _, ref := range req.Bindings {
		if !envName.MatchString(ref.Name) || !envName.MatchString(ref.Variable) {
			return errors.New("binding requires environment variable names, never values")
		}
		if _, ok := bindings[ref.Name]; ok {
			return errors.New("duplicate environment binding")
		}
		bindings[ref.Name] = ref.Variable
	}
	used := map[string]bool{}
	cross := req.From.Root != req.To.Root || req.From.Scope != req.To.Scope
	for name, value := range d.Env {
		if value.Variable == "" {
			continue
		}
		selected := name
		if _, ok := bindings[selected]; !ok {
			selected = value.Variable
		}
		if variable, ok := bindings[selected]; ok {
			value.Variable = variable
			used[selected] = true
			d.Env[name] = value
		} else if cross && sensitiveName(name) {
			return errors.New("cross-scope credential use requires an explicit --bind SERVER_ENV=PROCESS_ENV")
		}
	}
	for name, value := range d.Headers {
		if value.Variable == "" {
			continue
		}
		if variable, ok := bindings[value.Variable]; ok {
			used[value.Variable] = true
			value.Variable = variable
			d.Headers[name] = value
		} else if cross {
			return errors.New("cross-scope HTTP credentials require explicit --bind SOURCE_ENV=DEST_ENV")
		}
	}
	for name := range bindings {
		if !used[name] {
			return errors.New("binding is not used by the selected server")
		}
	}
	return nil
}
