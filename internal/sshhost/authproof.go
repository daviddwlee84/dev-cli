package sshhost

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	modernAuthentication = regexp.MustCompile(`^Authenticated to .+ using "([a-z][a-z0-9_-]*)"\.$`)
	legacyAuthentication = regexp.MustCompile(`^debug1: Authentication succeeded \(([a-z][a-z0-9_-]*)\)\.$`)
)

// selectedKeyAuthentication consumes only a bounded private OpenSSH -E log,
// never stdout/stderr (which can contain remote banners). Even when the client
// allows only publickey, its initial SSH "none" request can succeed on a keyless
// server. Exit status alone therefore cannot prove the selected key was used.
func selectedKeyAuthentication(data []byte, exitCode int) (bool, error) {
	events := 0
	method := ""
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		line := strings.TrimSuffix(string(raw), "\r")
		if strings.Contains(line, "remote software version Tailscale") {
			return false, ErrUnprovenAuthentication
		}
		match := modernAuthentication.FindStringSubmatch(line)
		if match == nil {
			match = legacyAuthentication.FindStringSubmatch(line)
		}
		if match != nil {
			events++
			method = match[1]
		}
	}
	if events == 0 && exitCode != 0 {
		return false, nil
	}
	if events != 1 || method != "publickey" || exitCode != 0 {
		return false, ErrUnprovenAuthentication
	}
	return true, nil
}
