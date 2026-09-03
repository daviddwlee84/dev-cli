package agentmcp

import (
	"encoding/json"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maximumNameBytes      = 256
	maximumReferenceBytes = 128
)

var (
	environmentNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNamePattern       = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	schemePattern           = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)
	dollarBracePattern      = regexp.MustCompile(`\$\{(?:env:)?([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}`)
	plainDollarBracePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}`)
	cursorEnvPattern        = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	dollarPattern           = regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])\$([A-Za-z_][A-Za-z0-9_]*)`)
	percentPattern          = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
	openCodeEnvPattern      = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	openCodeFilePattern     = regexp.MustCompile(`\{file:[^}]*\}`)
	executableNamePattern   = regexp.MustCompile(`^[A-Za-z0-9._+@-]+$`)
)

func decodeJSON(data []byte, target any) DiagnosticCode {
	if !utf8.Valid(data) {
		return DiagnosticMalformed
	}
	if strings.TrimSpace(string(data)) == "null" {
		return DiagnosticInvalidDeclaration
	}
	if err := json.Unmarshal(data, target); err != nil {
		return DiagnosticMalformed
	}
	if containsControl(target) {
		return DiagnosticControlCharacter
	}
	return ""
}

func containsControl(value any) bool {
	return reflectContainsControl(reflect.ValueOf(value))
}

func reflectContainsControl(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		return reflectContainsControl(value.Elem())
	}
	switch value.Kind() {
	case reflect.String:
		return stringContainsControl(value.String())
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if reflectContainsControl(iter.Key()) || reflectContainsControl(iter.Value()) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if reflectContainsControl(value.Index(index)) {
				return true
			}
		}
	case reflect.Struct:
		for index := range value.NumField() {
			if reflectContainsControl(value.Field(index)) {
				return true
			}
		}
	}
	return false
}

func stringContainsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}

func normalizedName(value string) (string, bool) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximumNameBytes ||
		!utf8.ValidString(value) || stringContainsControl(value) || looksSecretString(value) {
		return "", false
	}
	return value, true
}

func normalizedPlugin(value string) (string, bool) {
	return normalizedName(value)
}

func looksSecretString(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "sentinel") && (strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password")) {
		return true
	}
	if (strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "ghp_") || strings.HasPrefix(lower, "github_pat_")) && len(value) >= 16 {
		return true
	}
	if len(value) >= 64 {
		letters := 0
		for _, r := range value {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-/+=", r) {
				letters++
			}
		}
		if letters == len([]rune(value)) {
			return true
		}
	}
	return false
}

func newDeclaration(spec sourceSpec, rawName string) (Declaration, bool) {
	name, ok := normalizedName(rawName)
	if !ok {
		return Declaration{}, false
	}
	return Declaration{
		Name: name, Agent: spec.agent, Scope: spec.scope,
		Repository: spec.repository, RepositoryPath: spec.repositoryPath, Checkout: spec.checkout,
		LocalProjectPath: spec.localProjectPath, ConfigPath: spec.path,
		Source: spec.source, Transport: TransportUnknown,
	}, true
}

func inspectArguments(row *Declaration, arguments []string) {
	row.ArgumentCount = len(arguments)
	if len(arguments) == 0 {
		return
	}
	addRedaction(row, RedactionArguments)
	for _, argument := range arguments {
		inspectReferences(row, argument)
		if openCodeFilePattern.MatchString(argument) {
			addCredential(row, CredentialReference{Kind: CredentialFile})
			addRedaction(row, RedactionFileReference)
		}
	}
}

func inspectEnvironment(row *Declaration, environment map[string]string) {
	if len(environment) == 0 {
		return
	}
	addRedaction(row, RedactionEnvironmentValue)
	for name, value := range environment {
		if validEnvironmentName(name) {
			addCredential(row, CredentialReference{Kind: CredentialEnvironment, Name: name})
		} else {
			addCoverage(row, DiagnosticInvalidReference)
		}
		inspectReferences(row, value)
		inspectFileReference(row, value)
	}
}

func inspectHeaders(row *Declaration, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	addRedaction(row, RedactionHeaderValue)
	for name, value := range headers {
		if validHeaderName(name) {
			addCredential(row, CredentialReference{Kind: CredentialHeader, Name: strings.ToLower(name)})
		} else {
			addCoverage(row, DiagnosticInvalidReference)
		}
		inspectReferences(row, value)
		inspectFileReference(row, value)
	}
}

func inspectBearerEnvironment(row *Declaration, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if validEnvironmentName(name) {
		addCredential(row, CredentialReference{Kind: CredentialBearerEnvironment, Name: name})
	} else {
		addCoverage(row, DiagnosticInvalidReference)
	}
}

func inspectEnvironmentHeaders(row *Declaration, headers map[string]string) {
	for header, environment := range headers {
		if validHeaderName(header) {
			addCredential(row, CredentialReference{Kind: CredentialHeader, Name: strings.ToLower(header)})
		} else {
			addCoverage(row, DiagnosticInvalidReference)
		}
		if validEnvironmentName(environment) {
			addCredential(row, CredentialReference{Kind: CredentialEnvironmentReference, Name: environment})
		} else {
			addCoverage(row, DiagnosticInvalidReference)
		}
	}
}

func inspectOAuthValue(row *Declaration, field, value string) {
	if value == "" {
		return
	}
	addCredential(row, CredentialReference{Kind: CredentialOAuth, Name: field})
	addRedaction(row, RedactionOAuthValue)
	inspectReferences(row, value)
	inspectFileReference(row, value)
}

func inspectHelper(row *Declaration, configured bool) {
	if !configured {
		return
	}
	addCredential(row, CredentialReference{Kind: CredentialHelper})
	addRedaction(row, RedactionHelperCommand)
}

func inspectFileReference(row *Declaration, value string) {
	if !openCodeFilePattern.MatchString(value) {
		return
	}
	addCredential(row, CredentialReference{Kind: CredentialFile})
	addRedaction(row, RedactionFileReference)
}

func inspectReferences(row *Declaration, value string) {
	value = strings.TrimSpace(value)
	var patterns []*regexp.Regexp
	switch row.Agent {
	case AgentClaudeCode:
		patterns = []*regexp.Regexp{plainDollarBracePattern}
	case AgentCursor:
		patterns = []*regexp.Regexp{cursorEnvPattern}
	case AgentGeminiCLI:
		patterns = []*regexp.Regexp{plainDollarBracePattern, dollarPattern, percentPattern}
	case AgentOpenCode:
		patterns = []*regexp.Regexp{openCodeEnvPattern}
	}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(value, -1) {
			if len(match) != 2 || !validEnvironmentName(match[1]) {
				continue
			}
			addCredential(row, CredentialReference{Kind: CredentialEnvironmentReference, Name: match[1]})
		}
	}
}

func validEnvironmentName(value string) bool {
	return len(value) <= maximumReferenceBytes && environmentNamePattern.MatchString(value) && !looksSecretString(value)
}

func validHeaderName(value string) bool {
	return len(value) <= maximumReferenceBytes && headerNamePattern.MatchString(value) && !looksSecretString(value)
}

func setCommand(row *Declaration, command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	inspectReferences(row, command)
	if openCodeFilePattern.MatchString(command) {
		inspectFileReference(row, command)
		row.Command = "[dynamic]"
		addRedaction(row, RedactionDynamicCommand)
		return
	}
	if strings.Contains(command, "${") || strings.Contains(command, "{env:") ||
		dollarPattern.MatchString(command) || percentPattern.MatchString(command) {
		row.Command = "[dynamic]"
		addRedaction(row, RedactionDynamicCommand)
		return
	}
	normalized := strings.ReplaceAll(command, `\`, "/")
	basename := path.Base(normalized)
	if basename == "." || basename == "/" || basename == "" || len(basename) > maximumNameBytes ||
		!executableNamePattern.MatchString(basename) || strings.ContainsFunc(command, unicode.IsSpace) ||
		looksSecretString(basename) {
		row.Command = "[redacted]"
		addRedaction(row, RedactionDynamicCommand)
		addRedaction(row, RedactionArguments)
		return
	}
	row.Command = basename
	if basename != command {
		addRedaction(row, RedactionCommandPath)
	}
}

func setEndpoint(row *Declaration, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	inspectReferences(row, raw)
	if openCodeFilePattern.MatchString(raw) {
		inspectFileReference(row, raw)
		addRedaction(row, RedactionEndpointTemplate)
		row.Endpoint = "[dynamic]"
		return
	}
	if dollarBracePattern.MatchString(raw) || dollarPattern.MatchString(raw) || percentPattern.MatchString(raw) || openCodeEnvPattern.MatchString(raw) {
		addRedaction(row, RedactionEndpointTemplate)
		row.Endpoint = "[dynamic]"
		return
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Scheme == "" || parsed.Host == "" || !schemePattern.MatchString(parsed.Scheme) || stringContainsControl(parsed.Host) || looksSecretString(parsed.Host) {
		addCoverage(row, DiagnosticInvalidEndpoint)
		return
	}
	if parsed.User != nil {
		addRedaction(row, RedactionURLUserinfo)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		addRedaction(row, RedactionURLQuery)
	}
	if parsed.Fragment != "" {
		addRedaction(row, RedactionURLFragment)
	}
	endpoint := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		endpoint += "/[path]"
		addRedaction(row, RedactionURLPath)
	} else if parsed.EscapedPath() == "/" {
		endpoint += "/"
	}
	row.Endpoint = endpoint
}

func addCredential(row *Declaration, credential CredentialReference) {
	if credential.Name != "" && (!utf8.ValidString(credential.Name) || stringContainsControl(credential.Name) || len(credential.Name) > maximumReferenceBytes) {
		addCoverage(row, DiagnosticInvalidReference)
		return
	}
	for _, existing := range row.Credentials {
		if existing == credential {
			return
		}
	}
	row.Credentials = append(row.Credentials, credential)
}

func addRedaction(row *Declaration, redaction Redaction) {
	for _, existing := range row.Redactions {
		if existing == redaction {
			return
		}
	}
	row.Redactions = append(row.Redactions, redaction)
}

func addCoverage(row *Declaration, code DiagnosticCode) {
	for _, existing := range row.Coverage {
		if existing.Code == code {
			return
		}
	}
	row.Coverage = append(row.Coverage, coverage(code))
}

func addPolicy(row *Declaration, kind PolicyKind, value string, count int) {
	fact := PolicyFact{Kind: kind, Value: value, Count: count}
	for _, existing := range row.Policies {
		if existing == fact {
			return
		}
	}
	row.Policies = append(row.Policies, fact)
}

func addBooleanPolicy(row *Declaration, kind PolicyKind, value bool) {
	addPolicy(row, kind, strconv.FormatBool(value), 0)
}

func finalizeDeclaration(row *Declaration) {
	sort.Slice(row.Credentials, func(i, j int) bool {
		if row.Credentials[i].Kind != row.Credentials[j].Kind {
			return row.Credentials[i].Kind < row.Credentials[j].Kind
		}
		return row.Credentials[i].Name < row.Credentials[j].Name
	})
	sort.Slice(row.Redactions, func(i, j int) bool { return row.Redactions[i] < row.Redactions[j] })
	sort.Slice(row.Policies, func(i, j int) bool {
		if row.Policies[i].Kind != row.Policies[j].Kind {
			return row.Policies[i].Kind < row.Policies[j].Kind
		}
		if row.Policies[i].Value != row.Policies[j].Value {
			return row.Policies[i].Value < row.Policies[j].Value
		}
		return row.Policies[i].Count < row.Policies[j].Count
	})
	sort.Slice(row.Coverage, func(i, j int) bool { return row.Coverage[i].Code < row.Coverage[j].Code })
}
