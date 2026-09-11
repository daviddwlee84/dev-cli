package sshhost

import (
	"fmt"
	"strings"
)

// ValidateSession checks settings that cannot be faithfully replayed by the
// operation's private connection config. Call before collecting any password.
// Bootstrap's effect-free proofs do not need session/forwarding settings.
func (op *AuthenticationOperation) ValidateSession(options ConnectionOptions) error {
	if op == nil || op.service == nil || len(op.route.Hops) == 0 {
		return ErrSourceChanged
	}
	_, err := appendSessionPolicy(nil, op.route.state.hops[len(op.route.state.hops)-1].effective, options)
	return err
}

func appendSessionPolicy(content []byte, effective EffectiveConfig, options ConnectionOptions) ([]byte, error) {
	unsupported := func(name string) ([]byte, error) {
		return nil, fmt.Errorf("configured %s cannot be replayed by this managed SSH session; use native ssh or adjust this profile: %w", name, ErrUnsupportedRoute)
	}
	if !options.SuppressForwarding {
		for _, key := range []string{"localforward", "remoteforward", "dynamicforward"} {
			if len(effective.Values[key]) > 0 {
				return unsupported(key)
			}
		}
		if strings.EqualFold(firstEffectiveValue(effective, "permitlocalcommand"), "yes") && firstEffectiveValue(effective, "localcommand") != "" && firstEffectiveValue(effective, "localcommand") != "none" {
			return unsupported("LocalCommand")
		}
	}
	if value := firstEffectiveValue(effective, "setenv"); value != "" && value != "none" {
		return unsupported("SetEnv")
	}
	var body strings.Builder
	body.Write(content)
	for _, key := range []string{"requesttty", "sessiontype", "escapechar", "enableescapecommandline", "stdinnull"} {
		if value := firstEffectiveValue(effective, key); value != "" {
			if !validUTF8NoControl(value) {
				return unsupported(key)
			}
			writeConfigDirective(&body, key, value)
		}
	}
	if value := firstEffectiveValue(effective, "remotecommand"); value != "" && value != "none" {
		if len(options.Args) > 0 || !validUTF8NoControl(value) || strings.Contains(value, "%") {
			return unsupported("RemoteCommand")
		}
		// Like ProxyCommand, RemoteCommand is a verbatim remainder, not one
		// quoted scalar token. The source is the reviewed native configuration.
		body.WriteString("    RemoteCommand " + value + "\n")
	}
	for _, value := range effective.Values["sendenv"] {
		fields := strings.Fields(value)
		for _, field := range fields {
			if !validUTF8NoControl(field) || strings.ContainsAny(field, "\"'\\=") {
				return unsupported("SendEnv")
			}
		}
		if len(fields) > 0 {
			body.WriteString("    SendEnv " + strings.Join(fields, " ") + "\n")
		}
	}
	if !options.SuppressForwarding {
		for _, key := range []string{"forwardx11", "forwardx11trusted", "forwardx11timeout", "exitonforwardfailure"} {
			if value := firstEffectiveValue(effective, key); value != "" {
				if !validUTF8NoControl(value) {
					return unsupported(key)
				}
				writeConfigDirective(&body, key, value)
			}
		}
		if !options.ForwardAgentNo {
			if value := firstEffectiveValue(effective, "forwardagent"); value != "" {
				if !validUTF8NoControl(value) || strings.Contains(value, "%") {
					return unsupported("ForwardAgent")
				}
				writeConfigDirective(&body, "ForwardAgent", value)
			}
		}
	}
	return []byte(body.String()), nil
}
