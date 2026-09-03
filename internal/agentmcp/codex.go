package agentmcp

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

func parseCodex(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	if !utf8.Valid(data) {
		return nil, []DiagnosticCode{DiagnosticMalformed}
	}
	var document map[string]any
	if _, err := toml.Decode(string(data), &document); err != nil {
		return nil, []DiagnosticCode{DiagnosticMalformed}
	}
	if containsControl(document) {
		return nil, []DiagnosticCode{DiagnosticControlCharacter}
	}

	var rows []Declaration
	var codes []DiagnosticCode
	for _, rawName := range sortedTableKeys(tableValue(document["mcp_servers"])) {
		row, ok := newDeclaration(spec, rawName)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		server, ok := tableValue(document["mcp_servers"])[rawName].(map[string]any)
		if !ok {
			codes = append(codes, DiagnosticInvalidDeclaration)
			continue
		}
		convertCodexServer(&row, server)
		finalizeDeclaration(&row)
		rows = append(rows, row)
	}

	plugins := tableValue(document["plugins"])
	for _, rawPlugin := range sortedTableKeys(plugins) {
		plugin, ok := normalizedPlugin(rawPlugin)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		pluginTable := tableValue(plugins[rawPlugin])
		pluginEnabled, hasPluginEnabled := boolField(pluginTable, "enabled")
		servers := tableValue(pluginTable["mcp_servers"])
		for _, rawName := range sortedTableKeys(servers) {
			rowSpec := spec
			rowSpec.source = SourcePlugin
			row, ok := newDeclaration(rowSpec, rawName)
			if !ok {
				codes = append(codes, DiagnosticInvalidName)
				continue
			}
			row.Plugin = plugin
			row.Transport = TransportUnknown
			addCoverage(&row, DiagnosticPluginDefinitionAbsent)
			if override, ok := servers[rawName].(map[string]any); ok {
				applyCodexPolicy(&row, override)
			} else {
				addCoverage(&row, DiagnosticInvalidDeclaration)
			}
			if hasPluginEnabled {
				addBooleanPolicy(&row, PolicyPluginEnabled, pluginEnabled)
				if !pluginEnabled || row.Enabled == nil {
					value := pluginEnabled
					row.Enabled = &value
				}
			}
			finalizeDeclaration(&row)
			rows = append(rows, row)
		}
	}
	return rows, codes
}

func convertCodexServer(row *Declaration, server map[string]any) {
	command, _ := stringField(server, "command")
	url, _ := stringField(server, "url")
	setCommand(row, command)
	inspectArguments(row, stringSlice(server["args"]))
	inspectEnvironment(row, stringMap(server["env"]))
	inspectCodexEnvVars(row, server["env_vars"])
	setEndpoint(row, url)
	inspectHeaders(row, stringMap(server["http_headers"]))
	inspectEnvironmentHeaders(row, stringMap(server["env_http_headers"]))
	if helper, ok := stringField(server, "http_headers_helper"); ok {
		inspectHelper(row, strings.TrimSpace(helper) != "")
	}
	if bearer, ok := stringField(server, "bearer_token_env_var"); ok {
		inspectBearerEnvironment(row, bearer)
	}
	if enabled, ok := boolField(server, "enabled"); ok {
		row.Enabled = &enabled
	}
	if required, ok := boolField(server, "required"); ok {
		row.Required = &required
	}
	applyCodexPolicy(row, server)

	hasCommand := strings.TrimSpace(command) != ""
	hasURL := strings.TrimSpace(url) != ""
	if hasCommand && hasURL {
		addCoverage(row, DiagnosticConflictingTransport)
	}
	switch {
	case hasURL:
		row.Transport = TransportStreamableHTTP
	case hasCommand:
		row.Transport = TransportStdio
	default:
		addCoverage(row, DiagnosticUnknownTransport)
	}

	if auth, ok := stringField(server, "auth"); ok && auth != "" {
		switch strings.ToLower(strings.TrimSpace(auth)) {
		case "oauth", "chatgpt":
			addPolicy(row, PolicyAuthentication, strings.ToLower(strings.TrimSpace(auth)), 0)
		default:
			addCoverage(row, DiagnosticInvalidDeclaration)
		}
	}
	if scopes := stringSlice(server["scopes"]); len(scopes) > 0 {
		addPolicy(row, PolicyOAuth, "scopes", len(scopes))
		addCredential(row, CredentialReference{Kind: CredentialOAuth, Name: "scopes"})
		addRedaction(row, RedactionOAuthValue)
	}
	if resource, ok := stringField(server, "oauth_resource"); ok {
		addPolicy(row, PolicyOAuth, "configured", 0)
		inspectOAuthValue(row, "resource", resource)
	}
	if oauth := tableValue(server["oauth"]); len(oauth) > 0 {
		addPolicy(row, PolicyOAuth, "configured", 0)
		for _, field := range []string{"client_id", "callback_url", "callback_port"} {
			if value, ok := scalarString(oauth[field]); ok {
				inspectOAuthValue(row, strings.ReplaceAll(field, "_", "-"), value)
			}
		}
	}
}

func applyCodexPolicy(row *Declaration, values map[string]any) {
	if enabled, ok := boolField(values, "enabled"); ok {
		row.Enabled = &enabled
		if row.Source == SourcePlugin {
			addBooleanPolicy(row, PolicyPluginEnabled, enabled)
		}
	}
	if tools := stringSlice(values["enabled_tools"]); len(tools) > 0 {
		addPolicy(row, PolicyEnabledTools, "", len(tools))
	}
	if tools := stringSlice(values["disabled_tools"]); len(tools) > 0 {
		addPolicy(row, PolicyDisabledTools, "", len(tools))
	}
	if mode, ok := stringField(values, "default_tools_approval_mode"); ok && mode != "" {
		mode = strings.ToLower(strings.TrimSpace(mode))
		switch mode {
		case "auto", "prompt", "writes", "approve":
			addPolicy(row, PolicyApprovalMode, mode, 0)
		default:
			addCoverage(row, DiagnosticInvalidDeclaration)
		}
	}
	if tools := tableValue(values["tools"]); len(tools) > 0 {
		addPolicy(row, PolicyApprovalMode, "per-tool", len(tools))
	}
}

func inspectCodexEnvVars(row *Declaration, value any) {
	items, ok := value.([]map[string]any)
	if ok {
		for _, item := range items {
			if name, ok := stringField(item, "name"); ok && validEnvironmentName(name) {
				addCredential(row, CredentialReference{Kind: CredentialEnvironmentReference, Name: name})
			} else {
				addCoverage(row, DiagnosticInvalidReference)
			}
		}
		return
	}
	for _, name := range stringSlice(value) {
		if validEnvironmentName(name) {
			addCredential(row, CredentialReference{Kind: CredentialEnvironmentReference, Name: name})
		} else {
			addCoverage(row, DiagnosticInvalidReference)
		}
	}
}

func tableValue(value any) map[string]any {
	if table, ok := value.(map[string]any); ok {
		return table
	}
	return nil
}

func sortedTableKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringField(table map[string]any, key string) (string, bool) {
	value, ok := table[key].(string)
	return value, ok
}

func boolField(table map[string]any, key string) (bool, bool) {
	value, ok := table[key].(bool)
	return value, ok
}

func scalarString(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case int64:
		return "configured", true
	default:
		return "", false
	}
}

func stringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMap(value any) map[string]string {
	table := tableValue(value)
	if len(table) == 0 {
		return nil
	}
	out := make(map[string]string, len(table))
	for key, value := range table {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}
