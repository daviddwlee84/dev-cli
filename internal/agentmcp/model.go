// Package agentmcp inventories static MCP server declarations without loading
// credentials, starting servers, running helpers, or contacting endpoints.
package agentmcp

import (
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
)

// Agent identifies one supported agent configuration format.
type Agent string

const (
	AgentClaudeCode Agent = "claude-code"
	AgentCodex      Agent = "codex"
	AgentCursor     Agent = "cursor"
	AgentGeminiCLI  Agent = "gemini-cli"
	AgentOpenCode   Agent = "opencode"
)

// Scope is the declaration layer in which a server was found. Layers are kept
// separate deliberately; this package does not calculate an effective config.
type Scope string

const (
	ScopeProject        Scope = "project"
	ScopeLocal          Scope = "local"
	ScopeUser           Scope = "user"
	ScopeCustom         Scope = "custom"
	ScopeSystemDefaults Scope = "system-defaults"
	ScopeSystemOverride Scope = "system-override"
	ScopeManaged        Scope = "managed"
)

// DeclarationSource distinguishes ordinary declarations, plugin overrides,
// and administrator-managed declarations.
type DeclarationSource string

const (
	SourceDirect  DeclarationSource = "direct"
	SourcePlugin  DeclarationSource = "plugin"
	SourceManaged DeclarationSource = "managed"
)

// Transport records only what the declaration proves statically.
type Transport string

const (
	TransportUnknown        Transport = "unknown"
	TransportStdio          Transport = "stdio"
	TransportHTTP           Transport = "http"
	TransportStreamableHTTP Transport = "streamable-http"
	TransportSSE            Transport = "sse"
	TransportWebSocket      Transport = "ws"
	TransportRemote         Transport = "remote"
	TransportLocal          Transport = "local"
)

// CredentialKind identifies a credential-bearing declaration without retaining
// its value.
type CredentialKind string

const (
	CredentialEnvironment          CredentialKind = "environment"
	CredentialEnvironmentReference CredentialKind = "environment-reference"
	CredentialHeader               CredentialKind = "header"
	CredentialBearerEnvironment    CredentialKind = "bearer-environment"
	CredentialOAuth                CredentialKind = "oauth"
	CredentialFile                 CredentialKind = "file"
	CredentialHelper               CredentialKind = "helper"
)

// CredentialReference contains only a safe reference name. Literal values are
// represented by Redactions and are never copied into normalized results.
type CredentialReference struct {
	Kind CredentialKind `json:"kind"`
	Name string         `json:"name,omitempty"`
}

// Redaction describes a class of source material intentionally omitted.
type Redaction string

const (
	RedactionArguments        Redaction = "arguments"
	RedactionCommandPath      Redaction = "command-path"
	RedactionDynamicCommand   Redaction = "dynamic-command"
	RedactionEnvironmentValue Redaction = "environment-value"
	RedactionHeaderValue      Redaction = "header-value"
	RedactionOAuthValue       Redaction = "oauth-value"
	RedactionFileReference    Redaction = "file-reference"
	RedactionHelperCommand    Redaction = "helper-command"
	RedactionEndpointTemplate Redaction = "endpoint-template"
	RedactionURLUserinfo      Redaction = "url-userinfo"
	RedactionURLPath          Redaction = "url-path"
	RedactionURLQuery         Redaction = "url-query"
	RedactionURLFragment      Redaction = "url-fragment"
)

// PolicyKind is a declared policy fact, not a computed policy decision.
type PolicyKind string

const (
	PolicyAlwaysLoad     PolicyKind = "always-load"
	PolicyApprovalMode   PolicyKind = "approval-mode"
	PolicyAllowed        PolicyKind = "allowed"
	PolicyExcluded       PolicyKind = "excluded"
	PolicyEnabledTools   PolicyKind = "enabled-tools"
	PolicyDisabledTools  PolicyKind = "disabled-tools"
	PolicyPluginEnabled  PolicyKind = "plugin-enabled"
	PolicyOAuth          PolicyKind = "oauth"
	PolicyAuthentication PolicyKind = "authentication"
	PolicyIncludeTools   PolicyKind = "include-tools"
	PolicyExcludeTools   PolicyKind = "exclude-tools"
)

// PolicyFact retains only finite policy values and collection counts. It never
// exposes tool names, scopes, audiences, or other potentially sensitive text.
type PolicyFact struct {
	Kind  PolicyKind `json:"kind"`
	Value string     `json:"value,omitempty"`
	Count int        `json:"count,omitempty"`
}

// DiagnosticCode is stable machine-readable coverage or file status.
type DiagnosticCode string

const (
	DiagnosticUnreadable             DiagnosticCode = "config_unreadable"
	DiagnosticTooLarge               DiagnosticCode = "config_too_large"
	DiagnosticNotRegular             DiagnosticCode = "config_not_regular"
	DiagnosticSymlinkLimit           DiagnosticCode = "config_symlink_limit"
	DiagnosticProjectSymlinkEscape   DiagnosticCode = "project_symlink_escape"
	DiagnosticMalformed              DiagnosticCode = "config_malformed"
	DiagnosticControlCharacter       DiagnosticCode = "control_character"
	DiagnosticInvalidDeclaration     DiagnosticCode = "invalid_declaration"
	DiagnosticInvalidName            DiagnosticCode = "invalid_server_name"
	DiagnosticUnknownTransport       DiagnosticCode = "transport_unknown"
	DiagnosticConflictingTransport   DiagnosticCode = "transport_conflict"
	DiagnosticInvalidEndpoint        DiagnosticCode = "endpoint_invalid"
	DiagnosticInvalidReference       DiagnosticCode = "credential_reference_invalid"
	DiagnosticPluginDefinitionAbsent DiagnosticCode = "plugin_definition_not_in_config"
	DiagnosticTransportPrecedence    DiagnosticCode = "lower_precedence_transport_ignored"
)

// CoverageDiagnostic explains a normalization limitation with fixed text that
// cannot echo configuration values.
type CoverageDiagnostic struct {
	Code    DiagnosticCode `json:"code"`
	Message string         `json:"message"`
}

// Diagnostic reports a source-level or declaration-level problem. Messages are
// fixed by Code; paths are the only source-derived values retained here.
type Diagnostic struct {
	Agent            Agent          `json:"agent"`
	Scope            Scope          `json:"scope"`
	Repository       string         `json:"repo,omitempty"`
	RepositoryPath   string         `json:"repo_path,omitempty"`
	Checkout         string         `json:"checkout,omitempty"`
	LocalProjectPath string         `json:"local_project_path,omitempty"`
	ConfigPath       string         `json:"config_path"`
	Code             DiagnosticCode `json:"code"`
	Message          string         `json:"message"`
}

// Declaration is one server declaration in one scope. Duplicate names in other
// scopes or files remain separate rows.
type Declaration struct {
	Name             string                `json:"name"`
	Agent            Agent                 `json:"agent"`
	Scope            Scope                 `json:"scope"`
	Repository       string                `json:"repo,omitempty"`
	RepositoryPath   string                `json:"repo_path,omitempty"`
	Checkout         string                `json:"checkout,omitempty"`
	LocalProjectPath string                `json:"local_project_path,omitempty"`
	ConfigPath       string                `json:"config_path"`
	Source           DeclarationSource     `json:"source"`
	Plugin           string                `json:"plugin,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	Required         *bool                 `json:"required,omitempty"`
	Trusted          *bool                 `json:"trusted,omitempty"`
	Policies         []PolicyFact          `json:"policies,omitempty"`
	Transport        Transport             `json:"transport"`
	Endpoint         string                `json:"endpoint,omitempty"`
	Command          string                `json:"command,omitempty"`
	ArgumentCount    int                   `json:"argument_count,omitempty"`
	Credentials      []CredentialReference `json:"credentials,omitempty"`
	Redactions       []Redaction           `json:"redactions,omitempty"`
	Coverage         []CoverageDiagnostic  `json:"coverage,omitempty"`
}

// IdentityKey identifies one declaration at one normalized source location.
// Runtime state and sanitized payload fields are intentionally excluded so a
// refresh can update them without making the row look like a different source.
func (d Declaration) IdentityKey() string {
	return strings.Join(declarationOrderingKey(d), "\x00")
}

func declarationOrderingKey(d Declaration) []string {
	return []string{
		string(d.Agent), scopeSortKey(d.Scope), strings.ToLower(d.Repository), d.RepositoryPath,
		d.Checkout, d.LocalProjectPath, d.ConfigPath, string(d.Source), strings.ToLower(d.Plugin),
		d.Plugin, strings.ToLower(d.Name), d.Name,
	}
}

// Target is shared with native skill inventory so repository and linked-
// checkout identity cannot drift between the two domains.
type Target = agenttarget.Target

// Coverage states the fixed limits of this static inventory. It is product
// metadata, not a claim about which agents are installed on the host.
type Coverage struct {
	Agents                 []Agent  `json:"agents"`
	StaticDeclarationsOnly bool     `json:"static_declarations_only"`
	OmittedSources         []string `json:"omitted_sources"`
}

// Result contains partial declarations and non-fatal per-source diagnostics.
type Result struct {
	Declarations []Declaration `json:"servers"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
	Coverage     Coverage      `json:"coverage"`
}

func defaultCoverage() Coverage {
	return Coverage{
		Agents:                 []Agent{AgentClaudeCode, AgentCodex, AgentCursor, AgentGeminiCLI, AgentOpenCode},
		StaticDeclarationsOnly: true,
		OmittedSources: []string{
			"runtime health and command-line-only configuration",
			"plugin caches and hosted connectors",
			"remote organization configuration",
			"inline OPENCODE_CONFIG_CONTENT",
		},
	}
}

// Filter selects declarations for CLI or TUI consumers. Empty fields are
// wildcards; Enabled and HasCredentials distinguish an omitted filter from false.
type Filter struct {
	Agent          Agent
	Scope          Scope
	Source         DeclarationSource
	Transport      Transport
	Repository     string
	NameContains   string
	Enabled        *bool
	HasCredentials *bool
}

// Match reports whether a declaration satisfies f.
func (f Filter) Match(d Declaration) bool {
	if f.Agent != "" && d.Agent != f.Agent ||
		f.Scope != "" && d.Scope != f.Scope ||
		f.Source != "" && d.Source != f.Source ||
		f.Transport != "" && d.Transport != f.Transport ||
		f.Repository != "" && d.Repository != f.Repository {
		return false
	}
	if f.NameContains != "" && !strings.Contains(strings.ToLower(d.Name), strings.ToLower(strings.TrimSpace(f.NameContains))) {
		return false
	}
	if f.Enabled != nil && (d.Enabled == nil || *d.Enabled != *f.Enabled) {
		return false
	}
	if f.HasCredentials != nil && (len(d.Credentials) > 0) != *f.HasCredentials {
		return false
	}
	return true
}

// FilterDeclarations returns matching rows without changing their order.
func FilterDeclarations(rows []Declaration, f Filter) []Declaration {
	out := make([]Declaration, 0, len(rows))
	for _, row := range rows {
		if f.Match(row) {
			out = append(out, row)
		}
	}
	return out
}

// SortDeclarations establishes the stable order used by Scan.
func SortDeclarations(rows []Declaration) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ak := declarationOrderingKey(a)
		bk := declarationOrderingKey(b)
		for n := range ak {
			if ak[n] != bk[n] {
				return ak[n] < bk[n]
			}
		}
		return false
	})
}

func scopeSortKey(scope Scope) string {
	switch scope {
	case ScopeProject:
		return "0-project"
	case ScopeLocal:
		return "1-local"
	case ScopeUser:
		return "2-user"
	case ScopeCustom:
		return "3-custom"
	case ScopeSystemDefaults:
		return "4-system-defaults"
	case ScopeSystemOverride:
		return "5-system-override"
	case ScopeManaged:
		return "6-managed"
	default:
		return "9-" + string(scope)
	}
}

func diagnosticMessage(code DiagnosticCode) string {
	switch code {
	case DiagnosticUnreadable:
		return "configuration file could not be read"
	case DiagnosticTooLarge:
		return "configuration file exceeds the scan size limit"
	case DiagnosticNotRegular:
		return "configuration source is not a regular file"
	case DiagnosticSymlinkLimit:
		return "configuration symlink chain exceeds the scan limit"
	case DiagnosticProjectSymlinkEscape:
		return "project configuration resolves outside the checkout"
	case DiagnosticMalformed:
		return "configuration file could not be decoded"
	case DiagnosticControlCharacter:
		return "configuration contains an unsupported control character"
	case DiagnosticInvalidDeclaration:
		return "server declaration has an invalid field"
	case DiagnosticInvalidName:
		return "server declaration has an invalid name"
	case DiagnosticUnknownTransport:
		return "server transport cannot be determined statically"
	case DiagnosticConflictingTransport:
		return "server declaration contains conflicting transport fields"
	case DiagnosticInvalidEndpoint:
		return "server endpoint could not be represented safely"
	case DiagnosticInvalidReference:
		return "credential reference name is invalid"
	case DiagnosticPluginDefinitionAbsent:
		return "plugin override does not include the plugin server definition"
	case DiagnosticTransportPrecedence:
		return "lower-precedence transport fields were ignored"
	default:
		return "configuration could not be fully represented"
	}
}

func coverage(code DiagnosticCode) CoverageDiagnostic {
	return CoverageDiagnostic{Code: code, Message: diagnosticMessage(code)}
}
