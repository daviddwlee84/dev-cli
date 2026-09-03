package agentmcp

import (
	"encoding/json"
	"strings"
)

type geminiDocument struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	MCP        struct {
		Allowed  []string `json:"allowed"`
		Excluded []string `json:"excluded"`
	} `json:"mcp"`
}

type geminiServer struct {
	Type                 string            `json:"type"`
	Command              string            `json:"command"`
	Args                 []string          `json:"args"`
	Env                  map[string]string `json:"env"`
	URL                  string            `json:"url"`
	HTTPURL              string            `json:"httpUrl"`
	TCP                  string            `json:"tcp"`
	Headers              map[string]string `json:"headers"`
	Trust                *bool             `json:"trust"`
	IncludeTools         []string          `json:"includeTools"`
	ExcludeTools         []string          `json:"excludeTools"`
	AuthProviderType     string            `json:"authProviderType"`
	TargetAudience       string            `json:"targetAudience"`
	TargetServiceAccount string            `json:"targetServiceAccount"`
	OAuth                *geminiOAuth      `json:"oauth"`
}

type geminiOAuth struct {
	Enabled          *bool    `json:"enabled"`
	ClientID         string   `json:"clientId"`
	ClientSecret     string   `json:"clientSecret"`
	AuthorizationURL string   `json:"authorizationUrl"`
	TokenURL         string   `json:"tokenUrl"`
	Scopes           []string `json:"scopes"`
	RedirectURI      string   `json:"redirectUri"`
	TokenParamName   string   `json:"tokenParamName"`
	Audiences        []string `json:"audiences"`
}

func parseGemini(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	var document geminiDocument
	if code := decodeJSON(data, &document); code != "" {
		return nil, []DiagnosticCode{code}
	}
	allowed := normalizeNameSet(document.MCP.Allowed)
	excluded := normalizeNameSet(document.MCP.Excluded)
	var rows []Declaration
	var codes []DiagnosticCode
	for _, rawName := range sortedRawNames(document.MCPServers) {
		row, ok := newDeclaration(spec, rawName)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		var server geminiServer
		if code := decodeJSON(document.MCPServers[rawName], &server); code != "" {
			codes = append(codes, code)
			continue
		}
		convertGeminiServer(&row, server)
		isAllowed, isExcluded := allowed[row.Name], excluded[row.Name]
		if isAllowed {
			addPolicy(&row, PolicyAllowed, "declared", 0)
		}
		if isExcluded {
			addPolicy(&row, PolicyExcluded, "declared", 0)
		}
		if isAllowed && isExcluded {
			addCoverage(&row, DiagnosticInvalidDeclaration)
		}
		finalizeDeclaration(&row)
		rows = append(rows, row)
	}
	return rows, codes
}

func convertGeminiServer(row *Declaration, server geminiServer) {
	inspectArguments(row, server.Args)
	inspectEnvironment(row, server.Env)
	inspectHeaders(row, server.Headers)
	row.Trusted = copyBool(server.Trust)
	if len(server.IncludeTools) > 0 {
		addPolicy(row, PolicyIncludeTools, "", len(server.IncludeTools))
	}
	if len(server.ExcludeTools) > 0 {
		addPolicy(row, PolicyExcludeTools, "", len(server.ExcludeTools))
	}

	hasHTTP := strings.TrimSpace(server.HTTPURL) != ""
	hasURL := strings.TrimSpace(server.URL) != ""
	hasTCP := strings.TrimSpace(server.TCP) != ""
	hasCommand := strings.TrimSpace(server.Command) != ""
	transportFields := 0
	for _, present := range []bool{hasHTTP, hasURL, hasTCP, hasCommand} {
		if present {
			transportFields++
		}
	}
	if transportFields > 1 {
		addCoverage(row, DiagnosticTransportPrecedence)
	}
	switch {
	case hasHTTP:
		row.Transport = TransportStreamableHTTP
		setEndpoint(row, server.HTTPURL)
	case hasURL:
		typeName := strings.ToLower(strings.TrimSpace(server.Type))
		switch typeName {
		case "http", "streamable-http":
			row.Transport = TransportStreamableHTTP
		case "sse", "":
			row.Transport = TransportSSE
		default:
			row.Transport = TransportUnknown
			addCoverage(row, DiagnosticUnknownTransport)
		}
		setEndpoint(row, server.URL)
	case hasCommand:
		row.Transport = TransportStdio
		setCommand(row, server.Command)
	case hasTCP:
		row.Transport = TransportWebSocket
		endpoint := strings.TrimSpace(server.TCP)
		if !strings.Contains(endpoint, "://") {
			endpoint = "tcp://" + endpoint
		}
		setEndpoint(row, endpoint)
	default:
		addCoverage(row, DiagnosticUnknownTransport)
	}

	if server.AuthProviderType != "" {
		auth := strings.ToLower(strings.TrimSpace(server.AuthProviderType))
		switch auth {
		case "dynamic_discovery", "google_credentials", "service_account_impersonation":
			addPolicy(row, PolicyAuthentication, auth, 0)
		default:
			addCoverage(row, DiagnosticInvalidDeclaration)
		}
	}
	inspectOAuthValue(row, "target-audience", server.TargetAudience)
	inspectOAuthValue(row, "target-service-account", server.TargetServiceAccount)
	if server.OAuth != nil {
		if server.OAuth.Enabled != nil {
			addBooleanPolicy(row, PolicyOAuth, *server.OAuth.Enabled)
		} else {
			addPolicy(row, PolicyOAuth, "configured", 0)
		}
		inspectOAuthValue(row, "client-id", server.OAuth.ClientID)
		inspectOAuthValue(row, "client-secret", server.OAuth.ClientSecret)
		inspectOAuthValue(row, "authorization-url", server.OAuth.AuthorizationURL)
		inspectOAuthValue(row, "token-url", server.OAuth.TokenURL)
		inspectOAuthValue(row, "redirect-uri", server.OAuth.RedirectURI)
		inspectOAuthValue(row, "token-parameter", server.OAuth.TokenParamName)
		for _, scope := range server.OAuth.Scopes {
			inspectOAuthValue(row, "scope", scope)
		}
		for _, audience := range server.OAuth.Audiences {
			inspectOAuthValue(row, "audience", audience)
		}
	}
}
