package sshhost

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCatalogPublicDiagnosticExplainsUnsafeParentMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-mode fixture")
	}
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "ssh-directory", true: "nested-directory"}[nested], func(t *testing.T) {
			paths := fixturePaths(t)
			parent := paths.SSHDir
			if nested {
				parent = filepath.Join(parent, "nested")
			}
			line := string(testPublicLine(0xa1, "public marker"))
			publicPath := filepath.Join(parent, "key.pub")
			writeFixture(t, publicPath, line+"\n")
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			s := newFixtureService(t, paths, DiscoverOptions{})
			catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
			if err != nil || catalog.Complete || len(catalog.Candidates) != 0 {
				t.Fatal(catalog, err)
			}
			diagnostic := requireKeyCatalogDiagnostic(t, catalog, "public_key_unreadable", publicPath)
			for _, text := range []string{"0755", "0700", "dev ssh key doctor"} {
				if !strings.Contains(diagnostic.Message, text) {
					t.Fatalf("missing %q in actionable diagnostic: %+v", text, diagnostic)
				}
			}
			assertKeyDiagnosticsRedacted(t, catalog, strings.Fields(line)[1], "public marker")
		})
	}
}

func TestCatalogPrivateDiagnosticExplainsExecutablePrivateMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-mode fixture")
	}
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "private")
	line := string(testPublicLine(0xa2, "pair"))
	writeFixture(t, identity, "PRIVATE CONTENT MUST NOT APPEAR")
	writeFixture(t, identity+".pub", line+"\n")
	if err := os.Chmod(identity, 0o700); err != nil {
		t.Fatal(err)
	}
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || !catalog.Complete || len(catalog.Candidates) != 1 || !catalog.Candidates[0].NeedsPermissionRepair {
		t.Fatal(catalog, err)
	}
	diagnostic := requireKeyCatalogDiagnostic(t, catalog, "private_key_permissions", identity)
	for _, text := range []string{"0700", "0600", "dev ssh key doctor"} {
		if !strings.Contains(diagnostic.Message, text) {
			t.Fatalf("missing %q in private-mode diagnostic: %+v", text, diagnostic)
		}
	}
	assertKeyDiagnosticsRedacted(t, catalog, "PRIVATE CONTENT MUST NOT APPEAR", strings.Fields(line)[1])
}

func TestCatalogPublicParseDiagnosticsAreSpecificAndRedacted(t *testing.T) {
	secretAlgorithm := "DO_NOT_ECHO_UNSUPPORTED_ALGORITHM"
	unsupportedBlob := base64.StdEncoding.EncodeToString(sshWireString([]byte(secretAlgorithm)))
	for _, test := range []struct {
		name, content, reason string
		secrets               []string
	}{
		{"malformed", "ssh-ed25519 DO_NOT_ECHO_INVALID_BLOB", "invalid base64 blob", []string{"DO_NOT_ECHO_INVALID_BLOB"}},
		{"unsupported", secretAlgorithm + " " + unsupportedBlob, "unsupported public-key or certificate algorithm", []string{secretAlgorithm, unsupportedBlob}},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := fixturePaths(t)
			publicPath := filepath.Join(paths.SSHDir, "key.pub")
			writeFixture(t, publicPath, test.content+"\n")
			s := newFixtureService(t, paths, DiscoverOptions{})
			catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
			if err != nil || catalog.Complete || len(catalog.Candidates) != 0 {
				t.Fatal(catalog, err)
			}
			diagnostic := requireKeyCatalogDiagnostic(t, catalog, "public_key_unreadable", publicPath)
			if !strings.Contains(diagnostic.Message, test.reason) || !strings.Contains(diagnostic.Message, "Check the .pub file format") {
				t.Fatalf("parse cause was lost: %+v", diagnostic)
			}
			if strings.Contains(diagnostic.Message, "doctor") {
				t.Fatalf("format diagnostic implies permission doctor can fix key contents: %+v", diagnostic)
			}
			assertKeyDiagnosticsRedacted(t, catalog, test.secrets...)
		})
	}
}

func TestCatalogMissingAliasPublicCompanionDoesNotInventPrivateKey(t *testing.T) {
	for _, privateExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "unused-default", true: "private-without-public"}[privateExists], func(t *testing.T) {
			paths := fixturePaths(t)
			identity := filepath.Join(paths.SSHDir, "id_ed25519")
			if privateExists {
				writeFixture(t, identity, "PRIVATE CONTENT MUST NOT BE READ")
			}
			s := newFixtureService(t, paths, DiscoverOptions{})
			catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{NoAgent: true,
				Effective: &EffectiveConfig{Alias: "target", IdentityFiles: []string{identity}},
			})
			if err != nil || !catalog.Complete || len(catalog.Candidates) != 0 || len(catalog.Diagnostics) != 1 {
				t.Fatal(catalog, err)
			}
			diagnostic := requireKeyCatalogDiagnostic(t, catalog, "public_key_companion_missing", identity+".pub")
			if diagnostic.Incomplete || !strings.Contains(diagnostic.Message, "Private-key availability has not been checked") {
				t.Fatalf("missing companion misrepresented: %+v", diagnostic)
			}
			local, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
			if err != nil || !local.Complete || len(local.Diagnostics) != 0 {
				t.Fatalf("local catalog invented alias defaults: %+v err=%v", local, err)
			}
		})
	}
}

func TestCatalogDeduplicatesUnreadableAliasAndTreePath(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "key")
	writeFixture(t, identity+".pub", "ssh-ed25519 NOT_BASE64")
	s := newFixtureService(t, paths, DiscoverOptions{})
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{NoAgent: true,
		Effective: &EffectiveConfig{Alias: "target", IdentityFiles: []string{identity, identity + ".pub"}},
	})
	if err != nil || catalog.Complete || len(catalog.Diagnostics) != 1 {
		t.Fatalf("duplicate unreadable source diagnostics: %+v err=%v", catalog, err)
	}
	requireKeyCatalogDiagnostic(t, catalog, "public_key_unreadable", identity+".pub")
}

func requireKeyCatalogDiagnostic(t *testing.T, catalog KeyCatalog, code, path string) Diagnostic {
	t.Helper()
	for _, diagnostic := range catalog.Diagnostics {
		if diagnostic.Code == code && diagnostic.Path == path {
			return diagnostic
		}
	}
	t.Fatalf("missing %s diagnostic for %s: %+v", code, path, catalog.Diagnostics)
	return Diagnostic{}
}

func assertKeyDiagnosticsRedacted(t *testing.T, catalog KeyCatalog, secrets ...string) {
	t.Helper()
	data, err := json.Marshal(catalog.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(data), secret) {
			t.Fatalf("diagnostic exposed key material: %s", data)
		}
	}
}
