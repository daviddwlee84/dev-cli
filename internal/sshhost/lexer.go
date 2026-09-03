package sshhost

import (
	"fmt"
	"strings"
	"unicode"
)

// parseConfigLine implements the lexical portion shared by discovery, managed
// ownership, and init placement. It deliberately stops before OpenSSH option
// semantics: comments and quotes are removed, escapes are decoded, and both
// "Keyword value" and "Keyword=value" forms are accepted.
func parseConfigLine(line string) (string, []string, bool, error) {
	tokens, err := lexConfigLine(line)
	if err != nil {
		return "", nil, false, err
	}
	if len(tokens) == 0 {
		return "", nil, true, nil
	}
	directive := tokens[0]
	arguments := append([]string(nil), tokens[1:]...)
	if equal := strings.IndexByte(directive, '='); equal >= 0 {
		value := directive[equal+1:]
		directive = directive[:equal]
		if value != "" {
			arguments = append([]string{value}, arguments...)
		}
	} else if len(arguments) > 0 && arguments[0] == "=" {
		arguments = arguments[1:]
	}
	if directive == "" {
		return "", nil, false, fmt.Errorf("empty directive")
	}
	return directive, arguments, false, nil
}

func lexConfigLine(line string) ([]string, error) {
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if !started {
			return
		}
		tokens = append(tokens, token.String())
		token.Reset()
		started = false
	}
	for _, r := range line {
		if r == 0 {
			return nil, fmt.Errorf("NUL byte in configuration line")
		}
		if escaped {
			token.WriteRune(r)
			started = true
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				started = true
				continue
			}
			token.WriteRune(r)
			started = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == '#':
			flush()
			return tokens, nil
		case unicode.IsSpace(r):
			flush()
		default:
			token.WriteRune(r)
			started = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("trailing escape in configuration line")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %q quote", quote)
	}
	flush()
	return tokens, nil
}
