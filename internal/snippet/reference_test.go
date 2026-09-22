package snippet

import "testing"

func TestParseReferenceBindsConfiguredEndpointAndScope(t *testing.T) {
	hosts := map[Kind]string{GitHub: "github.com", GitLab: "gitlab.example.com"}
	for _, tc := range []struct {
		input       string
		kind        Kind
		id, project string
	}{
		{"github:abc", GitHub, "abc", ""},
		{"https://gist.github.com/alice/abc", GitHub, "abc", ""},
		{"gitlab:12", GitLab, "12", ""},
		{"https://gitlab.example.com/-/snippets/12", GitLab, "12", ""},
		{"https://gitlab.example.com/group/repo/-/snippets/12", GitLab, "12", "group/repo"},
		{"https://gitlab.example.com/group/repo/snippets/12", GitLab, "12", "group/repo"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			id, err := ParseReference(tc.input, "", hosts)
			if err != nil || id.Forge != tc.kind || id.ID != tc.id || id.Project != tc.project {
				t.Fatalf("id=%+v err=%v", id, err)
			}
		})
	}
}

func TestParseReferenceRejectsProviderGuessingAndURLConfusion(t *testing.T) {
	hosts := map[Kind]string{GitHub: "github.com", GitLab: "gitlab.com"}
	for _, input := range []string{"123", "https://gist.github.com.attacker.test/alice/abc", "https://gitlab.com@attacker.test/-/snippets/1", "http://gitlab.com/-/snippets/1", "https://gitlab.com/-/snippets/1?x=1", "https://gitlab.com/a/../snippets/1", "https://gist.github.com/alice/abc/extra", "gitlab:-1", "github:../secret"} {
		if id, err := ParseReference(input, "", hosts); err == nil {
			t.Fatalf("accepted %s => %+v", input, id)
		}
	}
	if _, err := ParseReference("gitlab:123", GitHub, hosts); err == nil {
		t.Fatal("GitHub shortcut accepted GitLab reference")
	}
	if id, err := ParseReference("123", GitHub, hosts); err != nil || id.Forge != GitHub {
		t.Fatalf("explicit GitHub bare ID: %+v %v", id, err)
	}
}

func TestValidateProjectRejectsNativeEndpointPlaceholders(t *testing.T) {
	for _, project := range []string{":id", "group/:repo", "group:namespace/project"} {
		if err := ValidateProject(project); err == nil {
			t.Fatalf("accepted native endpoint placeholder %q", project)
		}
	}
}

func TestParseReferenceEnterpriseGistWithoutSubdomainIsolation(t *testing.T) {
	hosts := map[Kind]string{GitHub: "code.example.com", GitLab: "code.example.com"}
	id, err := ParseReference("https://code.example.com/gist/alice/abc123", "", hosts)
	if err != nil || id.Forge != GitHub || id.Host != "code.example.com" || id.ID != "abc123" {
		t.Fatalf("enterprise gist identity=%+v err=%v", id, err)
	}
	for _, raw := range []string{"https://other.example.com/gist/alice/abc123", "https://code.example.com/gist/abc123", "https://code.example.com/gist/alice/abc123/extra", "https://github.com/gist/alice/abc123"} {
		if parsed, err := ParseReference(raw, "", hosts); err == nil {
			t.Fatalf("accepted noncanonical enterprise gist %s => %+v", raw, parsed)
		}
	}
	if _, err := ParseReference("https://github.com/gist/alice/abc123", "", map[Kind]string{GitHub: "github.com"}); err == nil {
		t.Fatal("GitHub.com accepted Enterprise-only gist path")
	}
}
