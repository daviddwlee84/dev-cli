package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// Source policy is checked at both plan and apply time, but approval is never
// copied to the destination. Credential fields in mixed settings are ignored.
func validateMCPPolicy(ctx context.Context, req TransferRequest) error {
	if req.Kind != "mcp" {
		return nil
	}
	ref, err := resolveMCP(req.From)
	if err != nil {
		return err
	}
	paths := []string{ref.Path}
	if ref.Agent == "claude-code" && ref.Scope == "project" {
		paths = append(paths, filepath.Join(".claude", "settings.json"), filepath.Join(".claude", "settings.local.json"))
	}
	root, _, err := safefile.OpenRoot(ref.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, p := range paths {
		if ref.Agent == "codex" && p == ref.Path {
			continue
		}
		img, data, e := snapshot(ctx, root, p, nil)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return errors.New("source policy could not be safely observed")
		}
		if img.Kind == "absent" {
			continue
		}
		if img.Kind != "file" {
			return errors.New("source policy is not a regular file")
		}
		v, e := jsonDocument(data, ref.Agent == "opencode")
		if e != nil {
			return e
		}
		fields, e := jsonMap(&v)
		if e != nil {
			return e
		}
		for _, key := range []string{"disabledMcpjsonServers", "disabledMcpServers"} {
			var names []string
			if raw := fields[key]; len(raw) > 0 {
				if json.Unmarshal(raw, &names) != nil {
					return errors.New("unsupported source approval policy")
				}
				for _, name := range names {
					if name == req.Name {
						return errors.New("selected MCP server is disabled by source policy")
					}
				}
			}
		}
		if ref.Agent == "gemini-cli" {
			if node := v.Find("/mcp"); node != nil {
				policy, e := jsonMap(node)
				if e != nil {
					return e
				}
				var allowed, excluded []string
				for key, dst := range map[string]*[]string{"allowed": &allowed, "excluded": &excluded} {
					if raw := policy[key]; len(raw) > 0 && json.Unmarshal(raw, dst) != nil {
						return errors.New("unsupported Gemini MCP policy")
					}
				}
				found := len(allowed) == 0
				for _, name := range allowed {
					if name == req.Name {
						found = true
					}
				}
				for _, name := range excluded {
					if name == req.Name {
						found = false
					}
				}
				if !found {
					return errors.New("selected MCP server is excluded by source policy")
				}
			}
		}
		for _, key := range []string{"permissions", "permission", "tools"} {
			raw := fields[key]
			if len(raw) == 0 {
				continue
			}
			text := strings.ToLower(string(raw))
			if strings.Contains(text, "mcp__") || strings.Contains(text, strings.ToLower(req.Name)+"_") || strings.Contains(text, `"*"`) {
				return errors.New("MCP-related source tool policy requires native client handling")
			}
		}
	}
	return nil
}
