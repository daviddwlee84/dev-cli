package agentmcp

import (
	"encoding/json"
	"strings"

	"github.com/tailscale/hujson"
	"howett.net/plist"
)

type openCodeDocument struct {
	MCP map[string]json.RawMessage `json:"mcp"`
}

type openCodeServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Enabled     *bool             `json:"enabled"`
	OAuth       json.RawMessage   `json:"oauth"`
}

type openCodeOAuth struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Scope        string `json:"scope"`
}

func parseOpenCode(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	standard, err := hujson.Standardize(data)
	if err != nil {
		return nil, []DiagnosticCode{DiagnosticMalformed}
	}
	var document openCodeDocument
	if code := decodeJSON(standard, &document); code != "" {
		return nil, []DiagnosticCode{code}
	}
	var rows []Declaration
	var codes []DiagnosticCode
	for _, rawName := range sortedRawNames(document.MCP) {
		row, ok := newDeclaration(spec, rawName)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		var server openCodeServer
		if code := decodeJSON(document.MCP[rawName], &server); code != "" {
			codes = append(codes, code)
			continue
		}
		convertOpenCodeServer(&row, server)
		finalizeDeclaration(&row)
		rows = append(rows, row)
	}
	return rows, codes
}

func parseOpenCodePlist(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	var document map[string]any
	if _, err := plist.Unmarshal(data, &document); err != nil {
		return nil, []DiagnosticCode{DiagnosticMalformed}
	}
	if containsControl(document) {
		return nil, []DiagnosticCode{DiagnosticControlCharacter}
	}
	for _, key := range []string{"PayloadDisplayName", "PayloadIdentifier", "PayloadType", "PayloadUUID", "PayloadVersion", "_manualProfile"} {
		delete(document, key)
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return nil, []DiagnosticCode{DiagnosticMalformed}
	}
	return parseOpenCode(normalized, spec)
}

func convertOpenCodeServer(row *Declaration, server openCodeServer) {
	row.Enabled = copyBool(server.Enabled)
	inspectEnvironment(row, server.Environment)
	inspectHeaders(row, server.Headers)

	typeName := strings.ToLower(strings.TrimSpace(server.Type))
	hasCommand := len(server.Command) > 0 && strings.TrimSpace(server.Command[0]) != ""
	hasURL := strings.TrimSpace(server.URL) != ""
	switch typeName {
	case "local":
		row.Transport = TransportLocal
		if hasCommand {
			setCommand(row, server.Command[0])
			inspectArguments(row, server.Command[1:])
		} else {
			addCoverage(row, DiagnosticInvalidDeclaration)
		}
		if hasURL {
			addCoverage(row, DiagnosticConflictingTransport)
			setEndpoint(row, server.URL)
		}
	case "remote":
		row.Transport = TransportRemote
		if hasURL {
			setEndpoint(row, server.URL)
		} else {
			addCoverage(row, DiagnosticInvalidDeclaration)
		}
		if hasCommand {
			addCoverage(row, DiagnosticConflictingTransport)
			setCommand(row, server.Command[0])
			inspectArguments(row, server.Command[1:])
		}
	default:
		addCoverage(row, DiagnosticUnknownTransport)
		if hasCommand {
			setCommand(row, server.Command[0])
			inspectArguments(row, server.Command[1:])
		}
		if hasURL {
			setEndpoint(row, server.URL)
		}
	}
	convertOpenCodeOAuth(row, server.OAuth)
}

func convertOpenCodeOAuth(row *Declaration, raw json.RawMessage) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return
	}
	if trimmed == "false" {
		addBooleanPolicy(row, PolicyOAuth, false)
		return
	}
	var oauth openCodeOAuth
	if code := decodeJSON(raw, &oauth); code != "" {
		addCoverage(row, DiagnosticInvalidDeclaration)
		return
	}
	addPolicy(row, PolicyOAuth, "configured", 0)
	inspectOAuthValue(row, "client-id", oauth.ClientID)
	inspectOAuthValue(row, "client-secret", oauth.ClientSecret)
	inspectOAuthValue(row, "scope", oauth.Scope)
}
