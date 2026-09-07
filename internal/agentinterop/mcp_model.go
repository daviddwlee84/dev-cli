package agentinterop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

type CapabilityProfile struct {
	Agent      string
	Format     string
	Transports []string
}

func MCPProfiles() []CapabilityProfile {
	return []CapabilityProfile{
		{"claude-code", "mcpServers JSON", []string{"stdio", "streamable-http"}},
		{"codex", "mcp_servers TOML", []string{"stdio", "streamable-http"}},
		{"cursor", "mcpServers JSON", []string{"stdio", "streamable-http"}},
		{"gemini-cli", "mcpServers JSON", []string{"stdio", "streamable-http"}},
		{"opencode", "mcp JSON/JSONC", []string{"stdio", "streamable-http"}},
	}
}

type envValue struct {
	Variable string
	Literal  string
	Fallback *string
	Prefix   string
}
type mcpDefinition struct {
	Transport string
	Command   string
	Args      []string
	Env       map[string]envValue
	URL       string
	Headers   map[string]envValue
	Cwd       string
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var claudeRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}$`)
var cursorRef = regexp.MustCompile(`^\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}$`)
var opencodeRef = regexp.MustCompile(`^\{env:([A-Za-z_][A-Za-z0-9_]*)\}$`)
var geminiRef = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, term := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "private_key", "credential", "authorization"} {
		if strings.Contains(n, term) {
			return true
		}
	}
	return false
}

func parseValue(agent, value string) (envValue, error) {
	v := envValue{}
	if !safeText(value) || knownKey.MatchString(value) {
		return v, ErrCredentials
	}
	if strings.Contains(value, "://") {
		if u, err := url.Parse(value); err == nil && (u.User != nil || u.RawQuery != "") {
			return v, ErrCredentials
		}
	}
	switch agent {
	case "claude-code", "gemini-cli":
		if m := claudeRef.FindStringSubmatch(value); m != nil {
			v.Variable = m[1]
			if strings.Contains(value, ":-") {
				fallback := m[2]
				v.Fallback = &fallback
			}
			return v, nil
		}
		if agent == "gemini-cli" {
			if m := geminiRef.FindStringSubmatch(value); m != nil {
				v.Variable = m[1]
				return v, nil
			}
		}
	case "cursor":
		if m := cursorRef.FindStringSubmatch(value); m != nil {
			v.Variable = m[1]
			return v, nil
		}
	case "opencode":
		if m := opencodeRef.FindStringSubmatch(value); m != nil {
			v.Variable = m[1]
			return v, nil
		}
	}
	if strings.ContainsAny(value, "${}") {
		return v, errors.New("unsupported environment interpolation; use an explicit local binding")
	}
	v.Literal = value
	return v, nil
}

func formatValue(agent string, v envValue) (string, error) {
	if v.Variable == "" {
		return v.Literal, nil
	}
	var ref string
	switch agent {
	case "claude-code", "gemini-cli":
		ref = "${" + v.Variable
		if v.Fallback != nil {
			ref += ":-" + *v.Fallback
		}
		ref += "}"
	case "cursor":
		if v.Fallback != nil {
			return "", ErrUnsupported
		}
		ref = "${env:" + v.Variable + "}"
	case "opencode":
		if v.Fallback != nil {
			return "", ErrUnsupported
		}
		ref = "{env:" + v.Variable + "}"
	default:
		return "", errors.New("destination cannot express this environment reference natively")
	}
	return v.Prefix + ref, nil
}

func selectedString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || !safeText(s) {
		return "", errors.New("invalid MCP string field")
	}
	return s, nil
}
func selectedStrings(fields map[string]json.RawMessage, key string) ([]string, error) {
	raw, ok := fields[key]
	if !ok {
		return nil, nil
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("invalid MCP argument/reference array")
	}
	for _, s := range values {
		if !safeText(s) {
			return nil, ErrCredentials
		}
	}
	return values, nil
}

func parseMCP(agent string, fields map[string]json.RawMessage, transport string) (mcpDefinition, error) {
	d := mcpDefinition{Env: map[string]envValue{}, Headers: map[string]envValue{}}
	allowed := map[string]bool{}
	keys := map[string][]string{
		"claude-code": {"type", "command", "args", "env", "url", "headers"},
		"codex":       {"command", "args", "env", "env_vars", "url", "http_headers", "env_http_headers", "bearer_token_env_var", "cwd", "enabled"},
		"cursor":      {"type", "command", "args", "env", "url", "headers"},
		"gemini-cli":  {"type", "command", "args", "env", "url", "httpUrl", "headers", "cwd"},
		"opencode":    {"type", "command", "environment", "url", "headers", "cwd", "enabled", "oauth"},
	}[agent]
	if len(keys) == 0 {
		return d, ErrUnsupported
	}
	for _, k := range keys {
		allowed[k] = true
	}
	for key := range fields {
		if !allowed[key] {
			return d, errors.New("selected MCP declaration contains unsupported policy, authentication, helper, or extension fields")
		}
	}
	if raw, ok := fields["enabled"]; ok {
		var enabled bool
		if json.Unmarshal(raw, &enabled) != nil || !enabled {
			return d, errors.New("disabled MCP declarations require native client handling")
		}
	}
	if raw, ok := fields["oauth"]; ok && !bytesEqualJSON(raw, []byte("false")) {
		return d, errors.New("OAuth configuration and login state are not transferable")
	}
	var err error
	if agent == "opencode" {
		command, e := selectedStrings(fields, "command")
		if e != nil {
			return d, e
		}
		if len(command) > 0 {
			d.Command, d.Args = command[0], command[1:]
		}
	} else {
		d.Command, err = selectedString(fields, "command")
		if err != nil {
			return d, err
		}
		d.Args, err = selectedStrings(fields, "args")
		if err != nil {
			return d, err
		}
	}
	d.URL, err = selectedString(fields, "url")
	if err != nil {
		return d, err
	}
	if httpURL, err := selectedString(fields, "httpUrl"); err != nil {
		return d, err
	} else if httpURL != "" {
		if d.URL != "" {
			return d, errors.New("ambiguous MCP transport fields")
		}
		d.URL = httpURL
		d.Transport = "streamable-http"
	}
	kind, err := selectedString(fields, "type")
	if err != nil {
		return d, err
	}
	if d.Command != "" && d.URL != "" {
		return d, errors.New("conflicting MCP command and URL")
	}
	if d.Command != "" {
		d.Transport = "stdio"
		if kind != "" && kind != "stdio" && !(agent == "opencode" && kind == "local") {
			return d, ErrUnsupported
		}
		if strings.ContainsAny(d.Command, "${}\r\n") || knownKey.MatchString(d.Command) {
			return d, ErrCredentials
		}
		for _, arg := range d.Args {
			if sensitiveName(arg) || knownKey.MatchString(arg) || strings.ContainsAny(arg, "${}") {
				return d, errors.New("credentials, shell templates, and dynamic arguments cannot be transferred through argv")
			}
		}
	} else if d.URL != "" {
		if agent == "opencode" && kind != "remote" {
			return d, errors.New("OpenCode remote declarations require type remote")
		}
		if kind == "remote" && agent != "opencode" {
			return d, ErrUnsupported
		}
		switch kind {
		case "http", "streamable-http":
			d.Transport = "streamable-http"
		case "sse":
			return d, errors.New("SSE conversion is unsupported")
		case "", "remote":
		default:
			return d, ErrUnsupported
		}
		if agent == "codex" {
			d.Transport = "streamable-http"
		}
		if d.Transport == "" {
			if transport != "streamable-http" {
				return d, errors.New("remote transport is ambiguous; select --transport streamable-http after checking the server")
			}
			d.Transport = transport
		}
		if agent == "gemini-cli" && kind == "" && len(fields["url"]) > 0 {
			return d, errors.New("Gemini url denotes SSE; use a native httpUrl declaration")
		}
		if err = validEndpoint(d.URL); err != nil {
			return d, err
		}
	} else {
		return d, errors.New("MCP declaration needs one command or endpoint")
	}
	d.Cwd, err = selectedString(fields, "cwd")
	if err != nil {
		return d, err
	}
	envKey := "env"
	if agent == "opencode" {
		envKey = "environment"
	}
	if raw := fields[envKey]; len(raw) > 0 {
		var values map[string]string
		if json.Unmarshal(raw, &values) != nil {
			return d, errors.New("invalid MCP environment object")
		}
		for name, value := range values {
			if !envName.MatchString(name) {
				return d, errors.New("invalid environment variable name")
			}
			v, e := parseValue(agent, value)
			if e != nil {
				return d, e
			}
			if sensitiveName(name) && (v.Variable == "" || v.Fallback != nil && *v.Fallback != "") {
				return d, ErrCredentials
			}
			d.Env[name] = v
		}
	}
	if agent == "codex" {
		names, e := selectedStrings(fields, "env_vars")
		if e != nil {
			return d, e
		}
		for _, name := range names {
			if !envName.MatchString(name) {
				return d, errors.New("invalid environment forwarding name")
			}
			if _, exists := d.Env[name]; exists {
				return d, errors.New("ambiguous literal and forwarded environment")
			}
			d.Env[name] = envValue{Variable: name}
		}
	}
	headerKey := "headers"
	if agent == "codex" {
		headerKey = "http_headers"
	}
	if raw := fields[headerKey]; len(raw) > 0 {
		var headers map[string]string
		if json.Unmarshal(raw, &headers) != nil {
			return d, errors.New("invalid MCP headers")
		}
		for name, value := range headers {
			if !safeText(name) {
				return d, ErrCredentials
			}
			prefix := ""
			if strings.EqualFold(name, "Authorization") && strings.HasPrefix(value, "Bearer ") {
				prefix = "Bearer "
				value = strings.TrimPrefix(value, prefix)
			}
			v, e := parseValue(agent, value)
			if e != nil {
				return d, e
			}
			v.Prefix = prefix
			if v.Variable == "" && name != "Accept" && name != "Content-Type" && name != "User-Agent" {
				return d, ErrCredentials
			}
			if v.Fallback != nil && *v.Fallback != "" {
				return d, ErrCredentials
			}
			d.Headers[name] = v
		}
	}
	if agent == "codex" {
		if raw := fields["env_http_headers"]; len(raw) > 0 {
			var headers map[string]string
			if json.Unmarshal(raw, &headers) != nil {
				return d, ErrUnsupported
			}
			for name, variable := range headers {
				if !envName.MatchString(variable) || !safeText(name) {
					return d, ErrCredentials
				}
				if _, ok := d.Headers[name]; ok {
					return d, ErrUnsupported
				}
				d.Headers[name] = envValue{Variable: variable}
			}
		}
		bearer, e := selectedString(fields, "bearer_token_env_var")
		if e != nil {
			return d, e
		}
		if bearer != "" {
			if !envName.MatchString(bearer) {
				return d, ErrCredentials
			}
			if _, ok := d.Headers["Authorization"]; ok {
				return d, ErrUnsupported
			}
			d.Headers["Authorization"] = envValue{Variable: bearer, Prefix: "Bearer "}
		}
	}
	if d.Transport == "stdio" && len(d.Headers) > 0 {
		return d, errors.New("HTTP header fields do not apply to a stdio server")
	}
	if d.Transport == "streamable-http" && (len(d.Args) > 0 || d.Cwd != "") {
		return d, errors.New("stdio arguments/cwd do not apply to an HTTP server")
	}
	return d, nil
}

func bytesEqualJSON(a, b []byte) bool { return strings.TrimSpace(string(a)) == string(b) }
func validEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(endpoint, "${}") || knownKey.MatchString(endpoint) {
		return errors.New("endpoint must be a credential-free static HTTP(S) URL without query or fragment")
	}
	return nil
}

func renderMCP(agent string, d mcpDefinition) (map[string]any, error) {
	out := map[string]any{}
	if d.Transport == "stdio" {
		if agent == "opencode" {
			out["type"] = "local"
			out["command"] = append([]string{d.Command}, d.Args...)
		} else {
			out["command"] = d.Command
			if len(d.Args) > 0 {
				out["args"] = d.Args
			}
		}
		if d.Cwd != "" {
			if agent != "codex" && agent != "gemini-cli" && agent != "opencode" {
				return nil, errors.New("destination has no native cwd field; use an explicit launcher")
			}
			out["cwd"] = d.Cwd
		}
		values := map[string]string{}
		forward := []string{}
		for name, v := range d.Env {
			if agent == "codex" && v.Variable != "" {
				if v.Variable != name || v.Fallback != nil || v.Prefix != "" {
					return nil, errors.New("Codex requires same-name env forwarding; use an explicit local launcher binding")
				}
				forward = append(forward, name)
				continue
			}
			if agent == "codex" {
				values[name] = v.Literal
			} else {
				value, err := formatValue(agent, v)
				if err != nil {
					return nil, err
				}
				values[name] = value
			}
		}
		if len(values) > 0 {
			key := "env"
			if agent == "opencode" {
				key = "environment"
			}
			out[key] = values
		}
		if len(forward) > 0 {
			sort.Strings(forward)
			out["env_vars"] = forward
		}
	} else {
		switch agent {
		case "claude-code":
			out["type"] = "http"
			out["url"] = d.URL
		case "gemini-cli":
			out["httpUrl"] = d.URL
		case "opencode":
			out["type"] = "remote"
			out["url"] = d.URL
			out["oauth"] = false
		default:
			out["url"] = d.URL
		}
		headers, envHeaders := map[string]string{}, map[string]string{}
		for name, v := range d.Headers {
			if agent == "codex" && v.Variable != "" {
				if v.Fallback != nil {
					return nil, ErrUnsupported
				}
				if strings.EqualFold(name, "Authorization") && v.Prefix == "Bearer " {
					out["bearer_token_env_var"] = v.Variable
				} else {
					if v.Prefix != "" {
						return nil, ErrUnsupported
					}
					envHeaders[name] = v.Variable
				}
				continue
			}
			if agent == "codex" {
				headers[name] = v.Literal
			} else {
				value, err := formatValue(agent, v)
				if err != nil {
					return nil, err
				}
				headers[name] = value
			}
		}
		if len(headers) > 0 {
			key := "headers"
			if agent == "codex" {
				key = "http_headers"
			}
			out[key] = headers
		}
		if len(envHeaders) > 0 {
			out["env_http_headers"] = envHeaders
		}
		if len(d.Env) > 0 {
			return nil, errors.New("HTTP clients cannot receive credentials from a stdio server environment")
		}
	}
	if agent == "" {
		return nil, fmt.Errorf("destination agent: %w", ErrUnsupported)
	}
	return out, nil
}
