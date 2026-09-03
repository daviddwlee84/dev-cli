package agentmcp

import (
	"encoding/json"
	"strings"
)

type cursorDocument struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type cursorServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	EnvFile string            `json:"envFile"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Auth    *cursorAuth       `json:"auth"`
}

type cursorAuth struct {
	ClientID     string   `json:"CLIENT_ID"`
	ClientSecret string   `json:"CLIENT_SECRET"`
	Scopes       []string `json:"scopes"`
}

func parseCursor(data []byte, spec sourceSpec) ([]Declaration, []DiagnosticCode) {
	var document cursorDocument
	if code := decodeJSON(data, &document); code != "" {
		return nil, []DiagnosticCode{code}
	}
	var rows []Declaration
	var codes []DiagnosticCode
	for _, rawName := range sortedRawNames(document.MCPServers) {
		row, ok := newDeclaration(spec, rawName)
		if !ok {
			codes = append(codes, DiagnosticInvalidName)
			continue
		}
		var server cursorServer
		if code := decodeJSON(document.MCPServers[rawName], &server); code != "" {
			codes = append(codes, code)
			continue
		}
		setCommand(&row, server.Command)
		inspectArguments(&row, server.Args)
		inspectEnvironment(&row, server.Env)
		if strings.TrimSpace(server.EnvFile) != "" {
			addCredential(&row, CredentialReference{Kind: CredentialFile})
			addRedaction(&row, RedactionFileReference)
		}
		setEndpoint(&row, server.URL)
		inspectHeaders(&row, server.Headers)
		if server.Auth != nil {
			addPolicy(&row, PolicyOAuth, "configured", 0)
			inspectOAuthValue(&row, "client-id", server.Auth.ClientID)
			inspectOAuthValue(&row, "client-secret", server.Auth.ClientSecret)
			for _, scope := range server.Auth.Scopes {
				inspectOAuthValue(&row, "scope", scope)
			}
		}

		hasCommand := strings.TrimSpace(server.Command) != ""
		hasURL := strings.TrimSpace(server.URL) != ""
		if hasCommand && hasURL {
			addCoverage(&row, DiagnosticConflictingTransport)
		} else if hasURL {
			// Cursor's static URL form does not identify SSE versus streamable HTTP.
			row.Transport = TransportRemote
		} else if hasCommand {
			row.Transport = TransportStdio
		}
		if row.Transport == TransportUnknown {
			addCoverage(&row, DiagnosticUnknownTransport)
		}
		finalizeDeclaration(&row)
		rows = append(rows, row)
	}
	return rows, codes
}
