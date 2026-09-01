package forge

import (
	"errors"
	"strings"
)

// ErrAuth reports that a forge CLI is installed but not signed in, or that its
// stored credential was rejected.
//
// The provider CLIs answer an expired token with their own transport-level
// text — "HTTP 401", "Bad credentials" — which `run` then prefixes with the
// whole argv. That is the right thing to keep for diagnosis and the wrong
// thing to show someone who simply has not run `gh auth login` yet, so the
// headline becomes the remediation and the original stays in the Unwrap chain.
type ErrAuth struct {
	Kind Kind
	Bin  string
	// Action is the remediation, taken verbatim from authProbe so that the
	// login instruction has exactly one definition.
	Action string
	cause  error
}

func (e *ErrAuth) Error() string {
	if e.Action == "" {
		return e.Bin + " is signed out"
	}
	return e.Bin + " is signed out — " + e.Action
}

func (e *ErrAuth) Unwrap() error { return e.cause }

// authSignals are the substrings the provider CLIs use for a missing or
// rejected credential. They are matched case-insensitively against the whole
// wrapped error, which already contains the CLI's stderr.
//
// 403 is deliberately absent. Signing out produces 401; a 403 means a rate
// limit or a token whose scopes are too narrow, and telling someone to run
// `gh auth login` for either would send them down the wrong path. Those keep
// today's full-argv message, which is what you actually want for the rarer
// case that needs diagnosis.
var authSignals = []string{
	"http 401",
	"401 unauthorized",
	"bad credentials",
	"not logged in",
	"not logged into",
	"authentication required",
	"requires authentication",
	"auth login",
	"token is invalid",
	"no token provided",
}

// notAuthSignals win over authSignals, so that a rate limit or a dead network
// is never reported as a credential problem.
var notAuthSignals = []string{
	"rate limit",
	"secondary rate",
	"no such host",
	"connection refused",
	"i/o timeout",
	"context deadline exceeded",
	"tls handshake",
}

// classifyAuth returns an *ErrAuth when err looks like an authentication
// failure for kind, and err unchanged otherwise. A nil error stays nil.
func classifyAuth(kind Kind, bin string, err error) error {
	if err == nil || !isAuthFailure(err) {
		return err
	}
	var already *ErrAuth
	if errors.As(err, &already) {
		return err
	}
	_, action, _ := authProbe(kind)
	return &ErrAuth{Kind: kind, Bin: bin, Action: action, cause: err}
}

func isAuthFailure(err error) bool {
	message := strings.ToLower(err.Error())
	for _, signal := range notAuthSignals {
		if strings.Contains(message, signal) {
			return false
		}
	}
	for _, signal := range authSignals {
		if strings.Contains(message, signal) {
			return true
		}
	}
	return false
}

// IsAuth reports whether err is, or wraps, an authentication failure. Callers
// use it to distinguish "sign in" from "this genuinely broke".
func IsAuth(err error) bool {
	var authErr *ErrAuth
	return errors.As(err, &authErr)
}
