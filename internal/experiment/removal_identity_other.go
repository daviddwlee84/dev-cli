//go:build !darwin && !linux && !windows

package experiment

import "errors"

func removalIdentity(string) (string, string, error) {
	return "", "", errors.New("verified removal identity is unsupported on this platform")
}
