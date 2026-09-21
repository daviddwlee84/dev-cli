package claudeplan

import "testing"

func TestProjectPathEncodingHandlesNativeWindowsAndPOSIXSeparators(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{`C:\Users\me\project`, "C--Users-me-project"},
		{"C:/Users/me/project", "C--Users-me-project"},
		{"/home/me/project.name", "-home-me-project-name"},
	} {
		if got := encodeProjectPath(tc.path); got != tc.want {
			t.Errorf("encodeProjectPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
