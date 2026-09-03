package sshhost

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Effective asks the system OpenSSH client for evaluated user+system config.
// It intentionally supplies no -F override. Static discovery never calls this
// method; callers choose when user-authored Match exec/resolver behavior may run.
func (s *Service) Effective(ctx context.Context, alias string) (EffectiveConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateLookupAlias(alias); err != nil {
		return EffectiveConfig{}, err
	}
	result, err := s.runner.Run(ctx, RunRequest{
		Name: "ssh", Args: []string{"-G", alias}, Env: []string{"LC_ALL=C"}, Display: "ssh -G <alias>",
	})
	if err != nil {
		return EffectiveConfig{}, fmt.Errorf("evaluate SSH config: %w", err)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(string(result.Stderr))
		if message == "" {
			message = fmt.Sprintf("exit status %d", result.ExitCode)
		}
		return EffectiveConfig{}, fmt.Errorf("evaluate SSH config: %s", message)
	}
	return ParseEffective(alias, result.Stdout)
}

// VerifyManaged evaluates the ordinary user+system SSH configuration with
// plain ssh -G and checks that every scalar emitted by the managed fragment is
// effective. IdentityFile is additive, so managed expectations must appear in
// order but unrelated system/default identities may also be present.
func (s *Service) VerifyManaged(ctx context.Context, definition ManagedDefinition) (EffectiveConfig, error) {
	if err := ValidateManagedDefinition(definition); err != nil {
		return EffectiveConfig{}, err
	}
	effective, err := s.Effective(ctx, definition.Alias)
	if err != nil {
		return EffectiveConfig{}, err
	}
	if err := VerifyManagedEffective(definition, effective); err != nil {
		return EffectiveConfig{}, err
	}
	return effective, nil
}

// VerifyManagedEffective compares a managed definition with parsed ssh -G
// output. Error text names mismatched fields without echoing configuration
// values that may contain command-like user input.
func VerifyManagedEffective(definition ManagedDefinition, effective EffectiveConfig) error {
	if err := ValidateManagedDefinition(definition); err != nil {
		return err
	}
	var mismatches []string
	if !equalAlias(definition.Alias, effective.Alias) {
		mismatches = append(mismatches, "Alias")
	}
	if definition.HostName != effective.HostName {
		mismatches = append(mismatches, "HostName")
	}
	if definition.User != "" && definition.User != effective.User {
		mismatches = append(mismatches, "User")
	}
	if definition.Port != 0 && definition.Port != effective.Port {
		mismatches = append(mismatches, "Port")
	}
	if definition.ProxyJump != "" && definition.ProxyJump != effective.ProxyJump {
		mismatches = append(mismatches, "ProxyJump")
	}
	if definition.IdentitiesOnly != nil && (effective.IdentitiesOnly == nil || *definition.IdentitiesOnly != *effective.IdentitiesOnly) {
		mismatches = append(mismatches, "IdentitiesOnly")
	}
	if definition.IdentityFile != "" && !containsOrderedValues(effective.IdentityFiles, []string{definition.IdentityFile}) {
		mismatches = append(mismatches, "IdentityFile")
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("managed SSH config does not control effective fields: %s", strings.Join(mismatches, ", "))
	}
	return nil
}

func (s *Service) verifyManagedEffective(ctx context.Context, definition ManagedDefinition) error {
	_, err := s.VerifyManaged(ctx, definition)
	return err
}

func containsOrderedValues(actual, expected []string) bool {
	next := 0
	for _, value := range actual {
		if next < len(expected) && value == expected[next] {
			next++
		}
	}
	return next == len(expected)
}

// ValidateLookupAlias prevents option injection while retaining foreign exact
// aliases outside the narrower managed grammar.
func ValidateLookupAlias(alias string) error {
	if alias == "" || len(alias) > 255 || !utf8.ValidString(alias) {
		return fmt.Errorf("invalid SSH alias %q", alias)
	}
	if strings.HasPrefix(alias, "-") || strings.ContainsAny(alias, "*?[]!") {
		return fmt.Errorf("invalid exact SSH alias %q", alias)
	}
	for _, r := range alias {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid exact SSH alias %q", alias)
		}
	}
	return nil
}

// ParseEffective parses ssh -G's stable key/value output. Unknown keys remain
// in Values for future domain consumers without becoming a public CLI schema.
func ParseEffective(alias string, output []byte) (EffectiveConfig, error) {
	config := EffectiveConfig{Alias: alias, Values: make(map[string][]string)}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		separator := strings.IndexFunc(line, unicode.IsSpace)
		if separator < 1 {
			return EffectiveConfig{}, fmt.Errorf("parse ssh -G output: malformed line %q", line)
		}
		key := strings.ToLower(line[:separator])
		value := strings.TrimSpace(line[separator:])
		if value == "" {
			return EffectiveConfig{}, fmt.Errorf("parse ssh -G output: %s has no value", key)
		}
		config.Values[key] = append(config.Values[key], value)
	}
	if err := scanner.Err(); err != nil {
		return EffectiveConfig{}, fmt.Errorf("parse ssh -G output: %w", err)
	}
	first := func(key string) string {
		if values := config.Values[key]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	config.HostName = first("hostname")
	config.User = first("user")
	config.ProxyJump = first("proxyjump")
	config.IdentityFiles = append([]string(nil), config.Values["identityfile"]...)
	if value := first("port"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return EffectiveConfig{}, fmt.Errorf("parse ssh -G output: invalid port %q", value)
		}
		config.Port = port
	}
	if value := first("identitiesonly"); value != "" {
		var enabled bool
		switch strings.ToLower(value) {
		case "yes", "true":
			enabled = true
		case "no", "false":
			enabled = false
		default:
			return EffectiveConfig{}, fmt.Errorf("parse ssh -G output: invalid identitiesonly %q", value)
		}
		config.IdentitiesOnly = &enabled
	}
	return config, nil
}
