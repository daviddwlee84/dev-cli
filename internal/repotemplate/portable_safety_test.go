package repotemplate_test

import (
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/repotemplate"
)

func TestSnapshotApplyRejectsExpandedPortablePathHazards(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	for _, test := range []struct {
		name  string
		files []repotemplate.File
		want  string
	}{
		{
			name: "Windows reserved device",
			files: []repotemplate.File{
				{Path: "NUL.txt", Mode: 0o600},
			},
			want: "reserved Windows device name",
		},
		{
			name: "Unicode normalization collision",
			files: []repotemplate.File{
				{Path: "café.env", Mode: 0o600},
				{Path: "café.env", Mode: 0o600},
			},
			want: "Unicode-normalizing",
		},
		{
			name: "full Unicode case-fold collision",
			files: []repotemplate.File{
				{Path: "Straße.env", Mode: 0o600},
				{Path: "STRASSE.env", Mode: 0o600},
			},
			want: "case-insensitive",
		},
		{
			name: "invalid UTF-8",
			files: []repotemplate.File{
				{Path: invalidUTF8, Mode: 0o600},
			},
			want: "invalid UTF-8",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (repotemplate.Snapshot{Files: test.files}).Apply(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Apply error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPrepareRejectsBackslashSubdirectoryRatherThanReinterpretingIt(t *testing.T) {
	_, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: t.TempDir(), Subdir: `nested\template`,
	})
	if err == nil || !strings.Contains(err.Error(), "backslash") {
		t.Fatalf("Prepare error = %v", err)
	}
}
