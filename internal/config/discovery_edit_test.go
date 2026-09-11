package config

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestPatchDiscoveryPathPreservesDocument(t *testing.T) {
	for _, array := range []string{
		`['/one']`, `['/one', ]`, `[]`,
		"[\n  '/one' # keep on one\n]",
		"[\n  '/one', # keep on one\n  # end comment\n]",
		"[ # empty comment with ]\n]",
		"[\n'''/one[bracket]'''\n]",
	} {
		t.Run(array, func(t *testing.T) {
			for _, nl := range []string{"\n", "\r\n"} {
				raw := strings.ReplaceAll("# header\n[paths]\n# exact repos\nrepo_paths = "+array+" # tail\nproject_root = '/projects'\n\n# runtime\n[runtime]\nbackend = 'none'\n", "\n", nl)
				out, err := PatchDiscoveryPath([]byte(raw), "repo_paths", `/new "quoted" repo`)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := Parse([]byte(raw))
				after, err := Parse(out)
				if err != nil {
					t.Fatal(err)
				}
				want := append(append([]string(nil), before.Paths.RepoPaths...), `/new "quoted" repo`)
				if !reflect.DeepEqual(after.Paths.RepoPaths, want) {
					t.Fatal(after.Paths.RepoPaths)
				}
				for _, preserved := range []string{"# header", "# exact repos", "# tail", "project_root = '/projects'", "# runtime" + nl + "[runtime]" + nl + "backend = 'none'"} {
					if !bytes.Contains(out, []byte(preserved)) {
						t.Fatalf("lost %q in %s", preserved, out)
					}
				}
				if strings.Contains(raw, "# keep on one") && !bytes.Contains(out, []byte("'/one', # keep on one")) {
					t.Fatalf("moved comment: %s", out)
				}
				if nl == "\r\n" && bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
					t.Fatal("mixed newlines")
				}
				again, err := PatchDiscoveryPath(out, "repo_paths", `/new "quoted" repo`)
				if err != nil || !bytes.Equal(out, again) {
					t.Fatal("duplicate edit changed config", err)
				}
			}
		})
	}
}

func TestPatchDiscoveryMissingKeysRetainsDefaults(t *testing.T) {
	for _, raw := range []string{"", "# comment\n", "[runtime]\nbackend = 'none'\n", "[paths]", "[\"paths\"] # comment\nproject_root = '/work'\n[runtime]\nbackend = 'none'\n"} {
		for _, key := range []string{"scan_roots", "repo_paths"} {
			out, err := PatchDiscoveryPath([]byte(raw), key, "/new")
			if err != nil {
				t.Fatalf("%q: %v", raw, err)
			}
			cfg, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			if key == "scan_roots" && !reflect.DeepEqual(cfg.Paths.ScanRoots, append(Default().Paths.ScanRoots, "/new")) {
				t.Fatal(cfg.Paths.ScanRoots)
			}
		}
	}
}

func TestPatchDiscoveryRejectsUnsupportedLayoutsWithoutMutation(t *testing.T) {
	for _, raw := range []string{"paths = { repo_paths = [] }\n", "paths.repo_paths = []\n", "[paths]\nrepo_paths = 'wrong'\n", "[paths]\nrepo_paths = ["} {
		data := []byte(raw)
		if _, err := PatchDiscoveryPath(data, "repo_paths", "/new"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
		if string(data) != raw {
			t.Fatal("mutated source")
		}
	}
}
