// Package lease provides cooperative, durable repository operation fencing.
// Its default authority lives in private XDG state, outside repository roots.
package lease

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
)

const SchemaVersion = 1

type Scope string

const (
	ScopeRepository Scope = "repository"
	ScopeBranch     Scope = "branch"
)

type Key struct {
	Repository string `json:"repository"`
	Scope      Scope  `json:"scope"`
	Branch     string `json:"branch,omitempty"`
}

type Request struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	MachineID   string `json:"machine_id,omitempty"`
}

// Binding permanently associates an idempotency ID with one manifest digest.
type Binding struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
}

type Token struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	Epoch       uint64 `json:"epoch"`
}

type Claim struct {
	Token     Token     `json:"token"`
	MachineID string    `json:"machine_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type TombstoneKind string

const (
	TombstoneAborted  TombstoneKind = "aborted"
	TombstoneReturned TombstoneKind = "returned"
)

type Tombstone struct {
	Kind          TombstoneKind `json:"kind"`
	Token         Token         `json:"token"`
	AdvancedEpoch uint64        `json:"advanced_epoch"`
	CreatedAt     time.Time     `json:"created_at"`
}

// Record is the durable state for one canonical operation key. At most one of
// Reservation and Fence can be active.
type Record struct {
	Key         Key         `json:"key"`
	Epoch       uint64      `json:"epoch"`
	Reservation *Claim      `json:"reservation,omitempty"`
	Fence       *Claim      `json:"fence,omitempty"`
	Tombstones  []Tombstone `json:"tombstones,omitempty"`
}

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	LastEpoch     uint64    `json:"last_epoch"`
	Bindings      []Binding `json:"bindings"`
	Records       []Record  `json:"records"`
}

var (
	ErrBlocked       = errors.New("operation lease blocked")
	ErrConflict      = errors.New("operation lease conflict")
	ErrEpochMismatch = errors.New("operation lease epoch mismatch")
	ErrReturned      = errors.New("operation lease token is terminal")

	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type BlockedError struct {
	Key         Key
	Kind        string
	OperationID string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("%s lease for %s is blocked by %s operation %s", e.Key.Scope, e.Key.subject(), e.Kind, e.OperationID)
}

func (e *BlockedError) Unwrap() error { return ErrBlocked }

func RepositoryKey(repository string) Key {
	return Key{Repository: repository, Scope: ScopeRepository}
}

func BranchKey(repository, branch string) Key {
	return Key{Repository: repository, Scope: ScopeBranch, Branch: branch}
}

// GitCommonDirIdentity is the host-local canonical lease identity shared by
// every remote alias and linked worktree of one clone.
func GitCommonDirIdentity(commonDir string) string {
	if strings.TrimSpace(commonDir) == "" {
		return ""
	}
	return "git-common-dir:" + filepath.ToSlash(filepath.Clean(commonDir))
}

func (k Key) Validate() error {
	if err := validateIdentity("repository identity", k.Repository); err != nil {
		return err
	}
	switch k.Scope {
	case ScopeRepository:
		if k.Branch != "" {
			return errors.New("repository lease key must not name a branch")
		}
	case ScopeBranch:
		if err := validateIdentity("branch", k.Branch); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown lease scope %q", k.Scope)
	}
	return nil
}

func (k Key) canonical() string {
	// Length prefixes keep arbitrary valid identities unambiguous.
	return fmt.Sprintf("%08x:%s:%s:%08x:%s", len(k.Repository), k.Repository, k.Scope, len(k.Branch), k.Branch)
}

func (k Key) subject() string {
	if k.Scope == ScopeBranch {
		return k.Repository + " branch " + k.Branch
	}
	return k.Repository
}

func (r Request) Validate() error {
	if !operationIDPattern.MatchString(r.OperationID) {
		return fmt.Errorf("invalid operation id %q", r.OperationID)
	}
	if !assessment.ValidFingerprint(r.Digest) {
		return fmt.Errorf("operation %s has invalid digest %q", r.OperationID, r.Digest)
	}
	if r.MachineID != "" {
		if err := machineid.Validate(r.MachineID); err != nil {
			return err
		}
	}
	return nil
}

func (b Binding) Validate() error {
	return (Request{OperationID: b.OperationID, Digest: b.Digest}).Validate()
}

func (t Token) Validate() error {
	if err := (Request{OperationID: t.OperationID, Digest: t.Digest}).Validate(); err != nil {
		return err
	}
	if t.Epoch == 0 {
		return fmt.Errorf("operation %s has zero lease epoch", t.OperationID)
	}
	return nil
}

func (t Token) matches(other Token) bool {
	return t.OperationID == other.OperationID && t.Digest == other.Digest && t.Epoch == other.Epoch
}

func (t Token) sameOperation(request Request) bool {
	return t.OperationID == request.OperationID && t.Digest == request.Digest
}

// CanonicalKeys validates, deduplicates, and sorts keys in the one order used
// by every multi-key acquisition.
func CanonicalKeys(keys []Key) ([]Key, error) {
	if len(keys) == 0 {
		return nil, errors.New("operation lease requires at least one key")
	}
	unique := make(map[string]Key, len(keys))
	for _, key := range keys {
		if err := key.Validate(); err != nil {
			return nil, err
		}
		unique[key.canonical()] = key
	}
	canonical := make([]Key, 0, len(unique))
	for _, key := range unique {
		canonical = append(canonical, key)
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].canonical() < canonical[j].canonical() })
	return canonical, nil
}

func validateIdentity(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s is required and must be trimmed", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}
