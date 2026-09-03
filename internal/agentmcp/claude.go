package agentmcp

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type claudeDocument struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]json.RawMessage `json:"projects"`
}

type claudeProject struct {
	MCPServers         map[string]json.RawMessage `json:"mcpServers"`
	EnabledMCPServers  []string                   `json:"enabledMcpServers"`
	DisabledMCPServers []string                   `json:"disabledMcpServers"`
}

type claudeProjectApproval struct {
	Enabled   []string `json:"enabledMcpjsonServers"`
	Disabled  []string `json:"disabledMcpjsonServers"`
	EnableAll *bool    `json:"enableAllProjectMcpServers"`
}

func parseClaudeProjectApproval(data []byte) (map[string]bool, map[string]bool, *bool, DiagnosticCode) {
	var approval claudeProjectApproval
	if code := decodeJSON(data, &approval); code != "" {
		return nil, nil, nil, code
	}
	return normalizeNameSet(approval.Enabled), normalizeNameSet(approval.Disabled), copyBool(approval.EnableAll), ""
}

type claudeProjectApprovalState struct {
	enabled   map[string]bool
	disabled  map[string]bool
	enableAll *bool
}

func (s *claudeProjectApprovalState) merge(enabled, disabled map[string]bool, enableAll *bool) {
	for name := range enabled {
		s.enabled[name] = true
	}
	for name := range disabled {
		s.disabled[name] = true
	}
	if enableAll != nil {
		s.enableAll = copyBool(enableAll)
	}
}

func applyClaudeProjectApproval(rows []Declaration, enabled, disabled map[string]bool, enableAll *bool) {
	for index := range rows {
		explicitEnabled, isDisabled := enabled[rows[index].Name], disabled[rows[index].Name]
		allEnabled := enableAll != nil && *enableAll
		switch {
		case explicitEnabled && isDisabled:
			addPolicy(&rows[index], PolicyAllowed, "declared", 0)
			addPolicy(&rows[index], PolicyExcluded, "declared", 0)
			addCoverage(&rows[index], DiagnosticInvalidDeclaration)
		case isDisabled:
			value := false
			rows[index].Enabled = &value
			addPolicy(&rows[index], PolicyExcluded, "declared", 0)
		case explicitEnabled || allEnabled:
			value := true
			rows[index].Enabled = &value
			addPolicy(&rows[index], PolicyAllowed, "declared", 0)
		}
		finalizeDeclaration(&rows[index])
	}
}

type claudeServer struct {
	Type          string            `json:"type"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers"`
	HeadersHelper string            `json:"headersHelper"`
	AlwaysLoad    *bool             `json:"alwaysLoad"`
	OAuth         *claudeOAuth      `json:"oauth"`
}

type claudeOAuth struct {
	ClientID              string `json:"clientId"`
	AuthServerMetadataURL string `json:"authServerMetadataUrl"`
	Scopes                string `json:"scopes"`
}

func parseClaudeProject(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	var document claudeDocument
	if code := decodeJSON(data, &document); code != "" {
		return nil, []DiagnosticCode{code}
	}
	return convertClaudeServers(document.MCPServers, spec, nil)
}

func parseClaudeUser(data []byte, spec sourceSpec, targets []Target) ([]Declaration, []DiagnosticCode, []Diagnostic) {
	var document claudeDocument
	if code := decodeJSON(data, &document); code != "" {
		var localDiagnostics []Diagnostic
		if len(targets) > 0 {
			localSpec := spec
			localSpec.scope = ScopeLocal
			localDiagnostics = append(localDiagnostics, diagnosticFor(localSpec, code))
		}
		return nil, []DiagnosticCode{code}, localDiagnostics
	}
	rows, codes := convertClaudeServers(document.MCPServers, spec, nil)
	if len(document.Projects) == 0 || len(targets) == 0 {
		return rows, codes, nil
	}
	var diagnostics []Diagnostic

	projectKeys := make([]string, 0, len(document.Projects))
	for projectPath := range document.Projects {
		projectKeys = append(projectKeys, projectPath)
	}
	sort.Strings(projectKeys)
	for _, projectPath := range projectKeys {
		target, ok := matchClaudeTarget(projectPath, targets)
		if !ok {
			continue
		}
		localSpec := spec
		localSpec.scope = ScopeLocal
		localSpec.repository = target.RepoDisplay
		localSpec.repositoryPath = target.RepoPath
		localSpec.checkout = target.CheckoutRoot
		localSpec.localProjectPath = filepath.Clean(projectPath)
		localSpec.projectRoot = ""
		var project claudeProject
		if code := decodeJSON(document.Projects[projectPath], &project); code != "" {
			diagnostics = append(diagnostics, diagnosticFor(localSpec, code))
			continue
		}
		state := &claudeProjectState{
			enabled:  normalizeNameSet(project.EnabledMCPServers),
			disabled: normalizeNameSet(project.DisabledMCPServers),
		}
		localRows, localCodes := convertClaudeServers(project.MCPServers, localSpec, state)
		rows = append(rows, localRows...)
		for _, code := range localCodes {
			diagnostics = append(diagnostics, diagnosticFor(localSpec, code))
		}
	}
	return rows, codes, diagnostics
}

type claudeProjectState struct {
	enabled  map[string]bool
	disabled map[string]bool
}

func convertClaudeServers(raw map[string]json.RawMessage, spec sourceSpec, state *claudeProjectState) ([]Declaration, []DiagnosticCode) {
	var rows []Declaration
	var codes []DiagnosticCode
	for _, rawName := range sortedRawNames(raw) {
		row, ok := newDeclaration(spec, rawName)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		var server claudeServer
		if code := decodeJSON(raw[rawName], &server); code != "" {
			codes = append(codes, code)
			continue
		}
		convertClaudeServer(&row, server)
		if state != nil {
			enabled, disabled := state.enabled[row.Name], state.disabled[row.Name]
			switch {
			case enabled && disabled:
				addPolicy(&row, PolicyAllowed, "declared", 0)
				addPolicy(&row, PolicyExcluded, "declared", 0)
				addCoverage(&row, DiagnosticInvalidDeclaration)
			case enabled:
				value := true
				row.Enabled = &value
				addPolicy(&row, PolicyAllowed, "declared", 0)
			case disabled:
				value := false
				row.Enabled = &value
				addPolicy(&row, PolicyExcluded, "declared", 0)
			}
		}
		finalizeDeclaration(&row)
		rows = append(rows, row)
	}
	return rows, codes
}

func convertClaudeServer(row *Declaration, server claudeServer) {
	setCommand(row, server.Command)
	inspectArguments(row, server.Args)
	inspectEnvironment(row, server.Env)
	setEndpoint(row, server.URL)
	inspectHeaders(row, server.Headers)
	inspectHelper(row, strings.TrimSpace(server.HeadersHelper) != "")
	if server.AlwaysLoad != nil {
		addBooleanPolicy(row, PolicyAlwaysLoad, *server.AlwaysLoad)
	}
	if server.OAuth != nil {
		addPolicy(row, PolicyOAuth, "configured", 0)
		inspectOAuthValue(row, "client-id", server.OAuth.ClientID)
		inspectOAuthValue(row, "metadata-url", server.OAuth.AuthServerMetadataURL)
		inspectOAuthValue(row, "scopes", server.OAuth.Scopes)
	}

	typeName := strings.ToLower(strings.TrimSpace(server.Type))
	hasCommand := strings.TrimSpace(server.Command) != ""
	hasURL := strings.TrimSpace(server.URL) != ""
	if hasCommand && hasURL {
		addCoverage(row, DiagnosticConflictingTransport)
	}
	switch typeName {
	case "stdio":
		if hasCommand {
			row.Transport = TransportStdio
		}
	case "http":
		if hasURL {
			row.Transport = TransportHTTP
		}
	case "streamable-http":
		if hasURL {
			row.Transport = TransportStreamableHTTP
		}
	case "sse":
		if hasURL {
			row.Transport = TransportSSE
		}
	case "ws", "websocket":
		if hasURL {
			row.Transport = TransportWebSocket
		}
	case "":
		if hasCommand && !hasURL {
			row.Transport = TransportStdio
		}
	}
	if row.Transport == TransportUnknown {
		addCoverage(row, DiagnosticUnknownTransport)
	}
}

func normalizeNameSet(names []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range names {
		if normalized, ok := normalizedName(name); ok {
			set[normalized] = true
		}
	}
	return set
}

func sortedRawNames(values map[string]json.RawMessage) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func matchClaudeTarget(projectPath string, targets []Target) (Target, bool) {
	if projectPath == "" || stringContainsControl(projectPath) {
		return Target{}, false
	}
	candidate, err := filepath.Abs(projectPath)
	if err != nil {
		return Target{}, false
	}
	candidate = filepath.Clean(candidate)
	candidateCanonical := candidate
	if resolved, err := pathx.Canonical(candidate); err == nil {
		candidateCanonical = resolved
	}

	for _, target := range targets {
		checkout := filepath.Clean(target.CheckoutRoot)
		checkoutCanonical := checkout
		if resolved, err := pathx.Canonical(checkout); err == nil {
			checkoutCanonical = resolved
		}
		if candidate == checkout || candidateCanonical == checkoutCanonical {
			return target, true
		}
	}

	bestIndex, bestDepth := -1, -1
	for index, target := range targets {
		inside, err := pathx.Contains(target.CheckoutRoot, candidate)
		if err != nil || !inside {
			continue
		}
		checkout := filepath.Clean(target.CheckoutRoot)
		if resolved, err := pathx.Canonical(checkout); err == nil {
			checkout = resolved
		}
		if depth := len(checkout); depth > bestDepth {
			bestIndex, bestDepth = index, depth
		}
	}
	if bestIndex >= 0 {
		return targets[bestIndex], true
	}
	return Target{}, false
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
