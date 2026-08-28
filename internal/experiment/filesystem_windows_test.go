//go:build windows

package experiment

import "testing"

func TestSameWindowsVolumePath(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "same drive ignoring case", left: `C:\`, right: `c:\`, want: true},
		{name: "different drives", left: `C:\`, right: `D:\`, want: false},
		{name: "same UNC share ignoring case", left: `\\server\share\`, right: `\\SERVER\SHARE\`, want: true},
		{name: "different UNC shares", left: `\\server\one\`, right: `\\server\two\`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameWindowsVolumePath(test.left, test.right); got != test.want {
				t.Errorf("sameWindowsVolumePath(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}
