package pathx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

func TestValidatePortableSlashPath(t *testing.T) {
	limits := pathx.PortablePathLimits{MaxPathBytes: 128, MaxComponentBytes: 64, MaxDepth: 4}
	for _, value := range []string{
		".env", "nested/group/file.txt", "name with spaces/資料.env",
	} {
		if err := pathx.ValidatePortableSlashPath(value, limits); err != nil {
			t.Errorf("ValidatePortableSlashPath(%q): %v", value, err)
		}
	}

	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	for _, value := range []string{
		"", ".", "..", "../escape", "a/../escape", "a//b", "a/", "/absolute",
		"C:/absolute", "C:relative", "//server/share", `server\share`,
		".git/config", ".GIT/config", ".git./config", ".git /config",
		"bad:name", `bad"name`, "bad<name", "bad>name", "bad|name", "bad?name", "bad*name",
		"trailing./name", "trailing /name", "CON", "nul.txt", "NUL .txt", "aux.json", "PRN", "COM1.log", "LPT9", "COM¹.txt",
		"control\x1b/name", "bidi" + string(rune(0x202e)) + "/name", invalidUTF8,
	} {
		if err := pathx.ValidatePortableSlashPath(value, limits); err == nil {
			t.Errorf("ValidatePortableSlashPath(%q) unexpectedly succeeded", value)
		}
	}
}

func TestValidatePortableSlashPathLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  string
		limits pathx.PortablePathLimits
	}{
		{"path", "abc/def", pathx.PortablePathLimits{MaxPathBytes: 6}},
		{"component", "abcdef", pathx.PortablePathLimits{MaxComponentBytes: 5}},
		{"depth", "a/b/c", pathx.PortablePathLimits{MaxDepth: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := pathx.ValidatePortableSlashPath(test.value, test.limits)
			if !errors.Is(err, pathx.ErrPathLimit) {
				t.Fatalf("error = %v, want ErrPathLimit", err)
			}
		})
	}
}

func TestValidatePortablePathSetRejectsCaseAndNormalizationCollisions(t *testing.T) {
	for _, paths := range [][]string{
		{"README.md", "readme.md"},
		{"caf\u00e9/config", "cafe\u0301/config"},
		{"Parent/one", "parent/two"},
		{"Straße/file", "STRASSE/file"},
	} {
		err := pathx.ValidatePortablePathSet(paths, pathx.PortablePathLimits{})
		if !errors.Is(err, pathx.ErrPathCollision) {
			t.Errorf("ValidatePortablePathSet(%q) = %v, want ErrPathCollision", paths, err)
		}
	}
	if err := pathx.ValidatePortablePathSet([]string{"group/one", "group/two", "group/two"}, pathx.PortablePathLimits{}); err != nil {
		t.Fatalf("exact duplicate cardinality belongs to caller: %v", err)
	}
}

func TestValidatePortableSlashPathRejectsOversizedUnicodeByEncodedBytes(t *testing.T) {
	value := strings.Repeat("界", 3)
	err := pathx.ValidatePortableSlashPath(value, pathx.PortablePathLimits{MaxComponentBytes: 8})
	if !errors.Is(err, pathx.ErrPathLimit) {
		t.Fatalf("error = %v, want byte-oriented component limit", err)
	}
}
