package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticCoCommitIdentities() CoCommitBinding {
	digest := func(label string) string {
		sum := sha256.Sum256([]byte("synthetic noncredential identity: " + label))
		return hex.EncodeToString(sum[:])
	}
	return CoCommitBinding{
		HelperToken: digest("helper"), PythonToken: digest("python"),
		BashToken: digest("bash"), ArtifactPolicyToken: digest("policy"),
		HelperRevision: digest("helper revision"), RequestRevision: digest("request revision"),
	}
}

func TestCoCommitMetadataIdentitiesAreNotNamedCredentials(t *testing.T) {
	data, err := json.Marshal(syntheticCoCommitIdentities())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"helper_identity", "python_identity", "bash_identity", "artifact_policy_identity"} {
		if value, ok := fields[name].(string); !ok || !coCommitDigest.MatchString(value) {
			t.Fatalf("missing opaque identity field %s", name)
		}
	}
	for name := range fields {
		if strings.HasSuffix(name, "_token") {
			t.Fatalf("noncredential identity mislabeled as a credential: %s", name)
		}
	}
}

func TestCoCommitMetadataPassesInstalledScanner(t *testing.T) {
	scanner, err := exec.LookPath("gitleaks")
	if err != nil {
		t.Skip("optional installed scanner unavailable; JSON identity contract is tested separately")
	}
	root := t.TempDir()
	config := filepath.Join(root, "policy.toml")
	if err := os.WriteFile(config, []byte("title = 'synthetic metadata test'\n[extend]\nuseDefault = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(syntheticCoCommitIdentities())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), scanner, "stdin", "--no-banner", "--config", config, "--redact=100", "--report-format", "json", "--report-path", filepath.Join(root, "report.json"))
	cmd.Dir, cmd.Stdin = root, bytes.NewReader(data)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		// Do not echo scanner Match/Secret fields even for synthetic cases.
		t.Fatal("scanner rejected noncredential metadata or could not complete")
	}
}
