package sshvault

import (
	"errors"
	"testing"
)

func TestNativeACLReadRequiresTwoEqualSafeObservations(t *testing.T) {
	first := nativeACLObservation{safe: true, digest: [32]byte{1}}
	changed := nativeACLObservation{safe: true, digest: [32]byte{2}}
	for _, test := range []struct {
		name         string
		observations []nativeACLObservation
		failure      error
		want         error
	}{
		{"stable", []nativeACLObservation{first, first}, nil, nil},
		{"changed", []nativeACLObservation{first, changed}, nil, ErrStale},
		{"unknown-second", []nativeACLObservation{first, {}}, nil, ErrNativeContext},
		{"unknown-first", []nativeACLObservation{{}}, nil, ErrNativeContext},
		{"failed-read", []nativeACLObservation{first}, ErrUnavailable, ErrNativeContext},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			result, err := readStableNativeACL(func() (nativeACLObservation, error) {
				if reads >= len(test.observations) {
					t.Fatal("unexpected metadata retry")
				}
				value := test.observations[reads]
				reads++
				return value, test.failure
			})
			if !errors.Is(err, test.want) || reads != len(test.observations) {
				t.Fatal("ACL observation stability was not enforced")
			}
			if err == nil && result != first || err != nil && result.safe {
				t.Fatal("unstable or unknown ACL became a safe observation")
			}
		})
	}
}
