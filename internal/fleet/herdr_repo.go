package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const HerdrRepoSchemaVersion = 1
const HerdrRepoHelper = "_herdr-repo"
const MaxHerdrRepoBytes int64 = 64 << 10

// The distinct helper makes an older peer reject the operation before opening
// a workspace. Adding a session field to the legacy helper would be unsafe:
// older decoders ignore unknown JSON fields and act on their default session.
type HerdrRepoRequest struct {
	SchemaVersion    int         `json:"schema_version"`
	Phase            string      `json:"phase"`
	Session          string      `json:"session"`
	Repository       OpenRequest `json:"repository"`
	ExpectedIdentity string      `json:"expected_identity,omitempty"`
}

type HerdrRepoResult struct {
	SchemaVersion      int    `json:"schema_version"`
	Phase              string `json:"phase"`
	Session            string `json:"session"`
	Path               string `json:"path"`
	RemoteIdentity     string `json:"remote_identity,omitempty"`
	RepositoryIdentity string `json:"repository_identity"`
	RuntimeState       string `json:"runtime_state"`
	RuntimeReason      string `json:"runtime_reason,omitempty"`
	Workspace          string `json:"workspace,omitempty"`
	Surface            string `json:"surface,omitempty"`
	Created            bool   `json:"created"`
}

func (r HerdrRepoRequest) Validate() error {
	if r.SchemaVersion != HerdrRepoSchemaVersion || (r.Phase != "check" && r.Phase != "prepare") {
		return errors.New("unsupported Herdr repository request")
	}
	if !validHerdrSession(r.Session) {
		return errors.New("invalid explicit Herdr session")
	}
	if !validHerdrRepoText(r.Repository.Path, 16<<10, true) || !validHerdrRepoText(r.Repository.RemoteIdentity, 8<<10, false) || !validHerdrRepoText(r.Repository.Name, 2048, false) {
		return errors.New("invalid Herdr repository identity")
	}
	if r.Phase == "prepare" && !validHerdrRepoIdentity(r.ExpectedIdentity) || r.ExpectedIdentity != "" && !validHerdrRepoIdentity(r.ExpectedIdentity) {
		return errors.New("prepare requires a checked repository identity")
	}
	return nil
}

func (r HerdrRepoResult) Validate(request HerdrRepoRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if r.SchemaVersion != request.SchemaVersion || r.Phase != request.Phase || r.Session != request.Session || r.Path != request.Repository.Path || r.RemoteIdentity != request.Repository.RemoteIdentity || !validHerdrRepoIdentity(r.RepositoryIdentity) {
		return errors.New("Herdr repository response does not match the selected session and repository")
	}
	if request.ExpectedIdentity != "" && r.RepositoryIdentity != request.ExpectedIdentity {
		return errors.New("Herdr repository changed since checking")
	}
	if r.RuntimeState != "ready" && r.RuntimeState != "needs-server" && r.RuntimeState != "unavailable" {
		return errors.New("invalid Herdr runtime observation")
	}
	if !validHerdrRepoText(r.RuntimeReason, 128, false) {
		return errors.New("invalid Herdr runtime reason")
	}
	if request.Phase == "check" {
		if r.Workspace != "" || r.Surface != "" || r.Created {
			return errors.New("read-only Herdr check returned an opening effect")
		}
	} else if r.RuntimeState != "ready" || !validHerdrRepoText(r.Workspace, 1024, true) || r.Surface != "workspace" && r.Surface != "worktree" {
		return errors.New("Herdr preparation did not return an exact workspace")
	}
	return nil
}

func validHerdrSession(value string) bool {
	if len(value) == 0 || len(value) > 64 || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validHerdrRepoText(value string, max int, required bool) bool {
	return (!required || strings.TrimSpace(value) != "") && len(value) <= max && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validHerdrRepoIdentity(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// HerdrRepositoryIdentity binds a read-only check to the actual checkout and
// both Git administrative directories, including their filesystem identities.
// It creates no persisted token or machine record and exposes no file content.
func HerdrRepositoryIdentity(checkout, gitDir, commonDir string) (string, error) {
	var parts []string
	for _, path := range []string{checkout, gitDir, commonDir} {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return "", errors.New("repository directory is unavailable")
		}
		identity, err := herdrDirectoryIdentity(info)
		if err != nil {
			return "", err
		}
		parts = append(parts, canonical, identity)
	}
	encoded, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
