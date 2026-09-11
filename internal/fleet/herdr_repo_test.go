package fleet

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func herdrRepoRequest() HerdrRepoRequest {
	return HerdrRepoRequest{SchemaVersion: 1, Phase: "check", Session: "agents", Repository: OpenRequest{Path: "/src/repo", RemoteIdentity: "github.com/acme/repo"}}
}

func TestHerdrRepoProtocolBindsExplicitSessionAndRepository(t *testing.T) {
	request := herdrRepoRequest()
	result := HerdrRepoResult{SchemaVersion: 1, Phase: "check", Session: "agents", Path: request.Repository.Path, RemoteIdentity: request.Repository.RemoteIdentity, RepositoryIdentity: strings.Repeat("a", 64), RuntimeState: "needs-server", RuntimeReason: "session-unavailable"}
	if err := result.Validate(request); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*HerdrRepoResult)
	}{
		{"wrong-session", func(r *HerdrRepoResult) { r.Session = "default" }},
		{"wrong-path", func(r *HerdrRepoResult) { r.Path = "/another" }},
		{"wrong-remote", func(r *HerdrRepoResult) { r.RemoteIdentity = "github.com/other/repo" }},
		{"missing-identity", func(r *HerdrRepoResult) { r.RepositoryIdentity = "" }},
		{"unknown-runtime", func(r *HerdrRepoResult) { r.RuntimeState = "maybe" }},
		{"control-reason", func(r *HerdrRepoResult) { r.RuntimeReason = "\x1b[2J" }},
		{"check-created", func(r *HerdrRepoResult) { r.Created = true }},
		{"check-opened", func(r *HerdrRepoResult) { r.Workspace = "w1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := result
			test.mutate(&changed)
			if err := changed.Validate(request); err == nil {
				t.Fatal("accepted unbound response")
			}
		})
	}
	request.Phase, request.ExpectedIdentity = "prepare", result.RepositoryIdentity
	result.Phase, result.RuntimeState, result.RuntimeReason = "prepare", "ready", ""
	result.Workspace, result.Surface, result.Created = "w7", "worktree", true
	if err := result.Validate(request); err != nil {
		t.Fatal(err)
	}
	result.RepositoryIdentity = strings.Repeat("b", 64)
	if err := result.Validate(request); err == nil {
		t.Fatal("prepare accepted replacement repository")
	}
}

func TestHerdrRepoRequestsRejectAmbientSessionAndUnboundPrepare(t *testing.T) {
	for _, session := range []string{"0", "-agents", "Project.A_1", strings.Repeat("a", 64)} {
		request := herdrRepoRequest()
		request.Session = session
		if err := request.Validate(); err != nil {
			t.Fatalf("rejected native-valid session %q: %v", session, err)
		}
	}
	for _, session := range []string{"", ".", "..", "../../default", "two sessions", strings.Repeat("a", 65)} {
		request := herdrRepoRequest()
		request.Session = session
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted session %q", session)
		}
	}
	request := herdrRepoRequest()
	request.Phase = "prepare"
	if err := request.Validate(); err == nil {
		t.Fatal("prepare did not require prior identity")
	}
	if HerdrRepoHelper == "_open-herdr" {
		t.Fatal("new session contract reused an old permissive decoder")
	}
	if _, err := checkedRemoteCommand(Host{RemoteOS: RemoteOSWindows}, []string{"fleet", HerdrRepoHelper}); err == nil {
		t.Fatal("native Windows preparation was enabled")
	}
}

func TestHerdrRepositoryIdentityDetectsSamePathReplacement(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX Herdr targets")
	}
	root := t.TempDir()
	checkout, gitDir := filepath.Join(root, "repo"), filepath.Join(root, "repo", ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := HerdrRepositoryIdentity(checkout, gitDir, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(checkout, filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := HerdrRepositoryIdentity(checkout, gitDir, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("same path replacement reused check identity")
	}
}
