package lease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
)

const stateFilename = "authority.json"

type Authority struct {
	root  string
	clock func() time.Time
}

func DefaultRoot() string {
	return filepath.Join(devconfig.DataHome(), "dev", "leases", "v1")
}

func New(root string) *Authority {
	if root == "" {
		root = DefaultRoot()
	}
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	return &Authority{root: filepath.Clean(root), clock: time.Now}
}

func (a *Authority) Root() string {
	if a == nil {
		return ""
	}
	return a.root
}

// Reserve creates a blocking pre-publication claim. Repeating the exact
// operation and key set is idempotent; reusing an operation ID with a different
// digest is a conflict.
func (a *Authority) Reserve(ctx context.Context, keys []Key, request Request) (Token, error) {
	return a.activate(ctx, keys, request, false)
}

// Fence drains existing cooperative guards and durably prevents new ordinary
// mutations. It can promote an exact reservation or create a direct fence.
func (a *Authority) Fence(ctx context.Context, keys []Key, request Request) (Token, error) {
	return a.activate(ctx, keys, request, true)
}

func (a *Authority) activate(ctx context.Context, keys []Key, request Request, fence bool) (Token, error) {
	if a == nil {
		return Token{}, errors.New("use nil operation lease authority")
	}
	if err := request.Validate(); err != nil {
		return Token{}, err
	}
	claimed, err := CanonicalKeys(keys)
	if err != nil {
		return Token{}, err
	}
	var result Token
	err = a.withKeyLocks(ctx, mutationKeys(claimed), func([]Key) error {
		return a.updateState(ctx, func(state *Snapshot) (bool, error) {
			if err := checkOperationReuse(*state, claimed, request); err != nil {
				return false, err
			}
			bindingAdded := ensureBinding(state, request)
			if err := checkHierarchyClaims(*state, claimed, request); err != nil {
				return false, err
			}
			canonical := claimed

			claims := make([]*Claim, len(canonical))
			claimKinds := make([]string, len(canonical))
			for index, key := range canonical {
				record := findRecord(state.Records, key)
				if record == nil {
					continue
				}
				switch {
				case record.Reservation != nil:
					claims[index], claimKinds[index] = record.Reservation, "reservation"
				case record.Fence != nil:
					claims[index], claimKinds[index] = record.Fence, "fence"
				}
			}

			allSameKind := true
			var existing Token
			for index, claim := range claims {
				wantedKind := "reservation"
				if fence {
					wantedKind = "fence"
				}
				if claim == nil || claimKinds[index] != wantedKind || !claim.Token.sameOperation(request) || claim.MachineID != request.MachineID {
					allSameKind = false
					break
				}
				if index == 0 {
					existing = claim.Token
				} else if !existing.matches(claim.Token) {
					return false, fmt.Errorf("operation %s has inconsistent epochs: %w", request.OperationID, ErrConflict)
				}
			}
			if allSameKind {
				result = existing
				return bindingAdded, nil
			}

			promoteReservation := fence
			anyClaim := false
			for index, claim := range claims {
				if claim == nil {
					promoteReservation = false
					continue
				}
				anyClaim = true
				if claimKinds[index] != "reservation" || !claim.Token.sameOperation(request) || claim.MachineID != request.MachineID {
					return false, blockedOrConflict(canonical[index], claimKinds[index], *claim, request)
				}
			}
			if anyClaim && !promoteReservation {
				return false, fmt.Errorf("operation %s only owns part of the requested key set: %w", request.OperationID, ErrConflict)
			}

			epoch, err := allocateEpoch(state)
			if err != nil {
				return false, err
			}
			result = Token{OperationID: request.OperationID, Digest: request.Digest, Epoch: epoch}
			now := canonicalTime(a.clock())
			for _, key := range canonical {
				record := ensureRecord(state, key)
				record.Epoch = epoch
				claim := &Claim{Token: result, MachineID: request.MachineID, CreatedAt: now}
				if fence {
					record.Reservation = nil
					record.Fence = claim
				} else {
					record.Reservation = claim
					record.Fence = nil
				}
			}
			return true, nil
		})
	})
	return result, err
}

// ReleaseReservation clears an exact reservation and advances the authority
// epoch. It is non-terminal; AbortReservation should be used for a terminal
// pre-fence cancellation.
func (a *Authority) ReleaseReservation(ctx context.Context, keys []Key, token Token) (uint64, error) {
	return a.finishClaim(ctx, keys, token, "reservation", "")
}

// AbortReservation clears an exact reservation and retains a permanent
// terminal tombstone so delayed prepare replays cannot recreate it.
func (a *Authority) AbortReservation(ctx context.Context, keys []Key, token Token) (uint64, error) {
	return a.finishClaim(ctx, keys, token, "reservation", TombstoneAborted)
}

// Return clears an exact fence and retains a permanent terminal tombstone so
// delayed accepts carrying the old epoch fail closed.
func (a *Authority) Return(ctx context.Context, keys []Key, token Token) (uint64, error) {
	return a.finishClaim(ctx, keys, token, "fence", TombstoneReturned)
}

func (a *Authority) finishClaim(ctx context.Context, keys []Key, token Token, claimKind string, terminal TombstoneKind) (uint64, error) {
	if a == nil {
		return 0, errors.New("use nil operation lease authority")
	}
	if err := token.Validate(); err != nil {
		return 0, err
	}
	claimed, err := CanonicalKeys(keys)
	if err != nil {
		return 0, err
	}
	var advanced uint64
	err = a.withKeyLocks(ctx, mutationKeys(claimed), func([]Key) error {
		return a.updateState(ctx, func(state *Snapshot) (bool, error) {
			canonical := claimed
			if terminal != "" {
				if epoch, found, err := matchingTerminal(*state, canonical, token, terminal); err != nil {
					return false, err
				} else if found {
					advanced = epoch
					return false, nil
				}
			}
			if err := validateClaimKeySet(*state, canonical, token, claimKind); err != nil {
				return false, err
			}
			for _, key := range canonical {
				record := findRecord(state.Records, key)
				if record == nil {
					return false, fmt.Errorf("%s has no active %s: %w", key.subject(), claimKind, ErrEpochMismatch)
				}
				var claim *Claim
				if claimKind == "reservation" {
					claim = record.Reservation
				} else {
					claim = record.Fence
				}
				if claim == nil || !claim.Token.matches(token) {
					return false, fmt.Errorf("%s does not hold operation %s epoch %d: %w", key.subject(), token.OperationID, token.Epoch, ErrEpochMismatch)
				}
			}
			epoch, err := allocateEpoch(state)
			if err != nil {
				return false, err
			}
			advanced = epoch
			now := canonicalTime(a.clock())
			for _, key := range canonical {
				record := findRecord(state.Records, key)
				record.Epoch = epoch
				if claimKind == "reservation" {
					record.Reservation = nil
				} else {
					record.Fence = nil
				}
				if terminal != "" {
					record.Tombstones = append(record.Tombstones, Tombstone{Kind: terminal, Token: token, AdvancedEpoch: epoch, CreatedAt: now})
				}
			}
			return true, nil
		})
	})
	return advanced, err
}

// WithMutation runs an ordinary mutator only when no requested key is reserved
// or fenced. A branch key automatically includes its repository ancestor so a
// repository fence covers every branch. The locks remain held until operation
// returns.
func (a *Authority) WithMutation(ctx context.Context, keys []Key, operation func() error) error {
	return a.WithGuard(ctx, keys, nil, operation)
}

// WithGuard permits the exact owner of an active reservation or fence to work
// while rejecting stale, returned, and mismatched tokens. A nil token is an
// ordinary mutation guard and includes repository ancestors. It is
// non-reentrant: operation must not reacquire an overlapping key.
func (a *Authority) WithGuard(ctx context.Context, keys []Key, token *Token, operation func() error) error {
	if a == nil {
		return errors.New("use nil operation lease authority")
	}
	if operation == nil {
		return errors.New("operation lease guard requires an operation")
	}
	claimed, err := CanonicalKeys(keys)
	if err != nil {
		return err
	}
	repositoryWide := make(map[string]bool)
	if token != nil {
		if err := token.Validate(); err != nil {
			return err
		}
	} else {
		for _, key := range claimed {
			if key.Scope == ScopeRepository {
				repositoryWide[key.Repository] = true
			}
		}
	}
	return a.withKeyLocks(ctx, mutationKeys(claimed), func(locked []Key) error {
		checked := locked
		if token != nil {
			// Repository ancestors participate in draining but are not part of a
			// branch-scoped token's ownership claim.
			checked = claimed
		}
		if err := a.readState(ctx, func(state Snapshot) error {
			matched := 0
			for _, key := range checked {
				record := findRecord(state.Records, key)
				if record == nil {
					if token != nil {
						return fmt.Errorf("%s has no operation %s epoch %d: %w", key.subject(), token.OperationID, token.Epoch, ErrEpochMismatch)
					}
					continue
				}
				if token != nil && hasTerminal(*record, *token) {
					return fmt.Errorf("operation %s epoch %d is terminal on %s: %w", token.OperationID, token.Epoch, key.subject(), ErrReturned)
				}
				claim, kind := activeClaim(*record)
				if claim == nil {
					if token != nil {
						return fmt.Errorf("operation %s epoch %d is no longer active on %s: %w", token.OperationID, token.Epoch, key.subject(), ErrEpochMismatch)
					}
					continue
				}
				if token == nil || !claim.Token.matches(*token) {
					return &BlockedError{Key: key, Kind: kind, OperationID: claim.Token.OperationID}
				}
				matched++
			}
			if token != nil && matched != len(checked) {
				return fmt.Errorf("operation %s does not own the complete key set: %w", token.OperationID, ErrEpochMismatch)
			}
			if token != nil {
				if err := validateGuardKeySet(state, claimed, *token); err != nil {
					return err
				}
			}
			if token == nil {
				for _, record := range state.Records {
					if record.Key.Scope != ScopeBranch || !repositoryWide[record.Key.Repository] {
						continue
					}
					if claim, kind := activeClaim(record); claim != nil {
						return &BlockedError{Key: record.Key, Kind: kind, OperationID: claim.Token.OperationID}
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		return operation()
	})
}

// Inspect returns records for the requested keys while holding those key locks.
// Untouched keys are represented with Epoch zero and are never persisted.
func (a *Authority) Inspect(ctx context.Context, keys []Key) ([]Record, error) {
	if a == nil {
		return nil, errors.New("use nil operation lease authority")
	}
	var result []Record
	err := a.withKeyLocks(ctx, keys, func(canonical []Key) error {
		return a.readState(ctx, func(state Snapshot) error {
			result = make([]Record, 0, len(canonical))
			for _, key := range canonical {
				if record := findRecord(state.Records, key); record != nil {
					result = append(result, cloneRecord(*record))
				} else {
					result = append(result, Record{Key: key})
				}
			}
			return nil
		})
	})
	return result, err
}

func mutationKeys(keys []Key) []Key {
	expanded := append([]Key(nil), keys...)
	for _, key := range keys {
		if key.Scope == ScopeBranch {
			expanded = append(expanded, RepositoryKey(key.Repository))
		}
	}
	return expanded
}

func (a *Authority) withKeyLocks(ctx context.Context, keys []Key, operation func([]Key) error) error {
	canonical, err := CanonicalKeys(keys)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.ensureLayout(); err != nil {
		return err
	}
	var acquire func(int) error
	acquire = func(index int) error {
		if index == len(canonical) {
			return operation(canonical)
		}
		directory := a.keyLockDir(canonical[index])
		if err := ensurePrivateDir(directory); err != nil {
			return fmt.Errorf("prepare operation key lock: %w", err)
		}
		return lockx.WithDir(ctx, directory, "operation lease key", func() error { return acquire(index + 1) })
	}
	return acquire(0)
}

func (a *Authority) updateState(ctx context.Context, mutate func(*Snapshot) (bool, error)) error {
	return a.withStateLock(ctx, func() error {
		state, err := a.loadState()
		if err != nil {
			return err
		}
		changed, err := mutate(&state)
		if err != nil || !changed {
			return err
		}
		return a.writeState(state)
	})
}

func (a *Authority) readState(ctx context.Context, read func(Snapshot) error) error {
	return a.withStateLock(ctx, func() error {
		state, err := a.loadState()
		if err != nil {
			return err
		}
		return read(state)
	})
}

func (a *Authority) withStateLock(ctx context.Context, operation func() error) error {
	directory := filepath.Join(a.root, ".authority")
	if err := ensurePrivateDir(directory); err != nil {
		return fmt.Errorf("prepare operation authority lock: %w", err)
	}
	return lockx.WithDir(ctx, directory, "operation lease authority", operation)
}

func (a *Authority) ensureLayout() error {
	if err := ensurePrivateDir(a.root); err != nil {
		return fmt.Errorf("prepare operation lease root: %w", err)
	}
	if err := ensurePrivateDir(filepath.Join(a.root, ".keys")); err != nil {
		return fmt.Errorf("prepare operation lease key root: %w", err)
	}
	return nil
}

func (a *Authority) keyLockDir(key Key) string {
	digest := sha256.Sum256([]byte(key.canonical()))
	return filepath.Join(a.root, ".keys", hex.EncodeToString(digest[:]))
}

func (a *Authority) statePath() string { return filepath.Join(a.root, stateFilename) }

func (a *Authority) loadState() (Snapshot, error) {
	path := a.statePath()
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect operation lease state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, fmt.Errorf("operation lease state %s is not a regular file", path)
	}
	private, err := privateModeMatches(path, info.Mode(), 0o600)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect operation lease state permissions: %w", err)
	}
	if !private {
		return Snapshot{}, fmt.Errorf("operation lease state %s permissions are not private (want 0600 or a protected owner-only policy)", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open operation lease state: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state Snapshot
	if err := decoder.Decode(&state); err != nil {
		return Snapshot{}, fmt.Errorf("decode operation lease state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Snapshot{}, errors.New("decode operation lease state: multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode operation lease state trailing data: %w", err)
	}
	if err := validateSnapshot(state); err != nil {
		return Snapshot{}, err
	}
	canonical := canonicalSnapshot(state)
	encodedState, _ := json.Marshal(state)
	encodedCanonical, _ := json.Marshal(canonical)
	if string(encodedState) != string(encodedCanonical) {
		return Snapshot{}, errors.New("operation lease state is not in canonical order")
	}
	return state, nil
}

func (a *Authority) writeState(state Snapshot) error {
	state = canonicalSnapshot(state)
	if err := validateSnapshot(state); err != nil {
		return err
	}
	if err := ensurePrivateDir(a.root); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(a.root, ".authority-*.tmp")
	if err != nil {
		return fmt.Errorf("create operation lease state temp file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := setPrivateMode(name, 0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode operation lease state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync operation lease state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close operation lease state: %w", err)
	}
	if err := replaceFile(name, a.statePath()); err != nil {
		return fmt.Errorf("publish operation lease state: %w", err)
	}
	if err := syncDirectory(a.root); err != nil {
		return fmt.Errorf("sync operation lease directory: %w", err)
	}
	return nil
}

func validateSnapshot(state Snapshot) error {
	if state.SchemaVersion != SchemaVersion {
		return fmt.Errorf("operation lease schema_version %d: want %d", state.SchemaVersion, SchemaVersion)
	}
	bindings := make(map[string]string, len(state.Bindings))
	for _, binding := range state.Bindings {
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("operation lease binding: %w", err)
		}
		if _, exists := bindings[binding.OperationID]; exists {
			return fmt.Errorf("operation lease repeats binding %s", binding.OperationID)
		}
		bindings[binding.OperationID] = binding.Digest
	}
	type activeSignature struct {
		Token     Token
		MachineID string
		Kind      string
	}
	activeOperations := make(map[string]activeSignature)
	terminalOperations := make(map[string]Tombstone)
	seenKeys := make(map[string]struct{}, len(state.Records))
	var maxRecordEpoch uint64
	for _, record := range state.Records {
		if err := record.Key.Validate(); err != nil {
			return fmt.Errorf("operation lease record: %w", err)
		}
		canonicalKey := record.Key.canonical()
		if _, exists := seenKeys[canonicalKey]; exists {
			return fmt.Errorf("operation lease state repeats key %s", record.Key.subject())
		}
		seenKeys[canonicalKey] = struct{}{}
		if record.Epoch == 0 || record.Epoch > state.LastEpoch {
			return fmt.Errorf("operation lease key %s has invalid epoch %d", record.Key.subject(), record.Epoch)
		}
		if record.Epoch > maxRecordEpoch {
			maxRecordEpoch = record.Epoch
		}
		if record.Reservation != nil && record.Fence != nil {
			return fmt.Errorf("operation lease key %s has both reservation and fence", record.Key.subject())
		}
		for _, named := range []struct {
			name  string
			claim *Claim
		}{{"reservation", record.Reservation}, {"fence", record.Fence}} {
			if named.claim == nil {
				continue
			}
			if err := named.claim.Token.Validate(); err != nil {
				return fmt.Errorf("operation lease %s on %s: %w", named.name, record.Key.subject(), err)
			}
			if digest, exists := bindings[named.claim.Token.OperationID]; !exists || digest != named.claim.Token.Digest {
				return fmt.Errorf("operation lease %s on %s has no matching permanent binding", named.name, record.Key.subject())
			}
			if named.claim.Token.Epoch != record.Epoch {
				return fmt.Errorf("operation lease %s on %s has epoch %d, record has %d", named.name, record.Key.subject(), named.claim.Token.Epoch, record.Epoch)
			}
			if named.claim.CreatedAt.IsZero() {
				return fmt.Errorf("operation lease %s on %s has no created_at", named.name, record.Key.subject())
			}
			if named.claim.MachineID != "" {
				if err := machineid.Validate(named.claim.MachineID); err != nil {
					return err
				}
			}
			signature := activeSignature{Token: named.claim.Token, MachineID: named.claim.MachineID, Kind: named.name}
			if prior, exists := activeOperations[named.claim.Token.OperationID]; exists {
				if !prior.Token.matches(signature.Token) || prior.MachineID != signature.MachineID || prior.Kind != signature.Kind {
					return fmt.Errorf("operation lease %s has inconsistent active claims", named.claim.Token.OperationID)
				}
			} else {
				activeOperations[named.claim.Token.OperationID] = signature
			}
		}
		seenTombstones := make(map[string]struct{}, len(record.Tombstones))
		for _, tombstone := range record.Tombstones {
			switch tombstone.Kind {
			case TombstoneAborted, TombstoneReturned:
			default:
				return fmt.Errorf("operation lease key %s has unknown tombstone kind %q", record.Key.subject(), tombstone.Kind)
			}
			if err := tombstone.Token.Validate(); err != nil {
				return fmt.Errorf("operation lease tombstone on %s: %w", record.Key.subject(), err)
			}
			if digest, exists := bindings[tombstone.Token.OperationID]; !exists || digest != tombstone.Token.Digest {
				return fmt.Errorf("operation lease tombstone on %s has no matching permanent binding", record.Key.subject())
			}
			if tombstone.AdvancedEpoch <= tombstone.Token.Epoch || tombstone.AdvancedEpoch > record.Epoch || tombstone.AdvancedEpoch > state.LastEpoch {
				return fmt.Errorf("operation lease tombstone on %s has invalid advanced epoch %d", record.Key.subject(), tombstone.AdvancedEpoch)
			}
			if tombstone.CreatedAt.IsZero() {
				return fmt.Errorf("operation lease tombstone on %s has no created_at", record.Key.subject())
			}
			if prior, exists := terminalOperations[tombstone.Token.OperationID]; exists {
				if !prior.Token.matches(tombstone.Token) || prior.Kind != tombstone.Kind || prior.AdvancedEpoch != tombstone.AdvancedEpoch {
					return fmt.Errorf("operation lease %s has inconsistent terminal records", tombstone.Token.OperationID)
				}
			} else {
				terminalOperations[tombstone.Token.OperationID] = tombstone
			}
			identity := fmt.Sprintf("%s\x00%s\x00%d", tombstone.Token.OperationID, tombstone.Token.Digest, tombstone.Token.Epoch)
			if _, exists := seenTombstones[identity]; exists {
				return fmt.Errorf("operation lease key %s repeats a terminal token", record.Key.subject())
			}
			seenTombstones[identity] = struct{}{}
			if record.Reservation != nil && record.Reservation.Token.matches(tombstone.Token) || record.Fence != nil && record.Fence.Token.matches(tombstone.Token) {
				return fmt.Errorf("operation lease key %s has a terminal token still active", record.Key.subject())
			}
		}
	}
	if state.LastEpoch != maxRecordEpoch {
		return fmt.Errorf("operation lease last_epoch %d does not match latest record epoch %d", state.LastEpoch, maxRecordEpoch)
	}
	for operationID := range activeOperations {
		if _, terminal := terminalOperations[operationID]; terminal {
			return fmt.Errorf("operation lease %s is both active and terminal", operationID)
		}
	}
	return nil
}

func canonicalSnapshot(state Snapshot) Snapshot {
	result := Snapshot{
		SchemaVersion: state.SchemaVersion,
		LastEpoch:     state.LastEpoch,
		Bindings:      append([]Binding(nil), state.Bindings...),
		Records:       make([]Record, len(state.Records)),
	}
	sort.Slice(result.Bindings, func(i, j int) bool { return result.Bindings[i].OperationID < result.Bindings[j].OperationID })
	for index, record := range state.Records {
		result.Records[index] = cloneRecord(record)
		if result.Records[index].Reservation != nil {
			result.Records[index].Reservation.CreatedAt = canonicalTime(result.Records[index].Reservation.CreatedAt)
		}
		if result.Records[index].Fence != nil {
			result.Records[index].Fence.CreatedAt = canonicalTime(result.Records[index].Fence.CreatedAt)
		}
		for tombstoneIndex := range result.Records[index].Tombstones {
			result.Records[index].Tombstones[tombstoneIndex].CreatedAt = canonicalTime(result.Records[index].Tombstones[tombstoneIndex].CreatedAt)
		}
		sort.Slice(result.Records[index].Tombstones, func(i, j int) bool {
			left, right := result.Records[index].Tombstones[i], result.Records[index].Tombstones[j]
			if left.Token.Epoch != right.Token.Epoch {
				return left.Token.Epoch < right.Token.Epoch
			}
			if left.Token.OperationID != right.Token.OperationID {
				return left.Token.OperationID < right.Token.OperationID
			}
			if left.Token.Digest != right.Token.Digest {
				return left.Token.Digest < right.Token.Digest
			}
			return left.Kind < right.Kind
		})
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Key.canonical() < result.Records[j].Key.canonical() })
	return result
}

func cloneRecord(record Record) Record {
	result := record
	if record.Reservation != nil {
		copy := *record.Reservation
		result.Reservation = &copy
	}
	if record.Fence != nil {
		copy := *record.Fence
		result.Fence = &copy
	}
	result.Tombstones = append([]Tombstone(nil), record.Tombstones...)
	return result
}

func findRecord(records []Record, key Key) *Record {
	canonical := key.canonical()
	for index := range records {
		if records[index].Key.canonical() == canonical {
			return &records[index]
		}
	}
	return nil
}

func ensureRecord(state *Snapshot, key Key) *Record {
	if record := findRecord(state.Records, key); record != nil {
		return record
	}
	state.Records = append(state.Records, Record{Key: key})
	return &state.Records[len(state.Records)-1]
}

func allocateEpoch(state *Snapshot) (uint64, error) {
	if state.LastEpoch == math.MaxUint64 {
		return 0, errors.New("operation lease epoch exhausted")
	}
	state.LastEpoch++
	return state.LastEpoch, nil
}

func activeClaim(record Record) (*Claim, string) {
	if record.Reservation != nil {
		return record.Reservation, "reservation"
	}
	if record.Fence != nil {
		return record.Fence, "fence"
	}
	return nil, ""
}

func hasTerminal(record Record, token Token) bool {
	for _, tombstone := range record.Tombstones {
		if tombstone.Token.matches(token) {
			return true
		}
	}
	return false
}

func ensureBinding(state *Snapshot, request Request) bool {
	for _, binding := range state.Bindings {
		if binding.OperationID == request.OperationID {
			return false
		}
	}
	state.Bindings = append(state.Bindings, Binding{OperationID: request.OperationID, Digest: request.Digest})
	return true
}

func checkOperationReuse(state Snapshot, keys []Key, request Request) error {
	for _, binding := range state.Bindings {
		if binding.OperationID != request.OperationID {
			continue
		}
		if binding.Digest != request.Digest {
			return fmt.Errorf("operation id %s was permanently bound to another digest: %w", request.OperationID, ErrConflict)
		}
		break
	}
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		requested[key.canonical()] = struct{}{}
	}
	for _, record := range state.Records {
		for _, tombstone := range record.Tombstones {
			if tombstone.Token.OperationID != request.OperationID {
				continue
			}
			if tombstone.Token.Digest != request.Digest {
				return fmt.Errorf("operation id %s was used with another digest: %w", request.OperationID, ErrConflict)
			}
			return fmt.Errorf("operation %s is terminal: %w", request.OperationID, ErrReturned)
		}
		claim, _ := activeClaim(record)
		if claim == nil || claim.Token.OperationID != request.OperationID {
			continue
		}
		if claim.Token.Digest != request.Digest {
			return fmt.Errorf("operation id %s is active with another digest: %w", request.OperationID, ErrConflict)
		}
		if _, exists := requested[record.Key.canonical()]; !exists {
			return fmt.Errorf("operation %s is active on a different key set: %w", request.OperationID, ErrConflict)
		}
	}
	return nil
}

func checkHierarchyClaims(state Snapshot, keys []Key, request Request) error {
	requestedKeys := make(map[string]struct{}, len(keys))
	requestedRepositories := make(map[string]struct{}, len(keys))
	repositoryWide := make(map[string]bool, len(keys))
	for _, key := range keys {
		requestedKeys[key.canonical()] = struct{}{}
		requestedRepositories[key.Repository] = struct{}{}
		if key.Scope == ScopeRepository {
			repositoryWide[key.Repository] = true
		}
	}
	for _, record := range state.Records {
		claim, kind := activeClaim(record)
		if claim == nil {
			continue
		}
		if _, requested := requestedKeys[record.Key.canonical()]; requested {
			continue
		}
		if _, sameRepository := requestedRepositories[record.Key.Repository]; !sameRepository {
			continue
		}
		if record.Key.Scope != ScopeRepository && !repositoryWide[record.Key.Repository] {
			continue
		}
		return blockedOrConflict(record.Key, kind, *claim, request)
	}
	return nil
}

func blockedOrConflict(key Key, kind string, claim Claim, request Request) error {
	if claim.Token.OperationID == request.OperationID {
		return fmt.Errorf("operation id %s conflicts with its active %s: %w", request.OperationID, kind, ErrConflict)
	}
	return &BlockedError{Key: key, Kind: kind, OperationID: claim.Token.OperationID}
}

func validateGuardKeySet(state Snapshot, keys []Key, token Token) error {
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		requested[key.canonical()] = struct{}{}
	}
	matched := 0
	for _, record := range state.Records {
		claim, _ := activeClaim(record)
		if claim == nil || !claim.Token.matches(token) {
			continue
		}
		if _, included := requested[record.Key.canonical()]; !included {
			return fmt.Errorf("operation %s epoch %d owns a larger key set: %w", token.OperationID, token.Epoch, ErrConflict)
		}
		matched++
	}
	if matched != len(keys) {
		return fmt.Errorf("operation %s epoch %d does not own the exact guard key set: %w", token.OperationID, token.Epoch, ErrEpochMismatch)
	}
	return nil
}

func validateClaimKeySet(state Snapshot, keys []Key, token Token, claimKind string) error {
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		requested[key.canonical()] = struct{}{}
	}
	matched := 0
	for _, record := range state.Records {
		var claim *Claim
		if claimKind == "reservation" {
			claim = record.Reservation
		} else {
			claim = record.Fence
		}
		if claim == nil || !claim.Token.matches(token) {
			continue
		}
		if _, included := requested[record.Key.canonical()]; !included {
			return fmt.Errorf("operation %s epoch %d owns a larger key set: %w", token.OperationID, token.Epoch, ErrConflict)
		}
		matched++
	}
	if matched != len(keys) {
		return fmt.Errorf("operation %s epoch %d does not own the exact key set: %w", token.OperationID, token.Epoch, ErrEpochMismatch)
	}
	return nil
}

func matchingTerminal(state Snapshot, keys []Key, token Token, kind TombstoneKind) (uint64, bool, error) {
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		requested[key.canonical()] = struct{}{}
	}
	var epoch uint64
	matched := 0
	for _, record := range state.Records {
		for _, tombstone := range record.Tombstones {
			if !tombstone.Token.matches(token) {
				continue
			}
			if tombstone.Kind != kind {
				return 0, false, fmt.Errorf("operation %s epoch %d ended as %s: %w", token.OperationID, token.Epoch, tombstone.Kind, ErrReturned)
			}
			if _, included := requested[record.Key.canonical()]; !included {
				return 0, false, fmt.Errorf("operation %s epoch %d is terminal on a larger key set: %w", token.OperationID, token.Epoch, ErrConflict)
			}
			if epoch == 0 {
				epoch = tombstone.AdvancedEpoch
			} else if epoch != tombstone.AdvancedEpoch {
				return 0, false, fmt.Errorf("operation %s has inconsistent terminal epochs: %w", token.OperationID, ErrConflict)
			}
			matched++
			break
		}
	}
	if matched == 0 {
		return 0, false, nil
	}
	if matched != len(keys) {
		return 0, false, fmt.Errorf("operation %s is terminal on only part of the key set: %w", token.OperationID, ErrConflict)
	}
	return epoch, true, nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a private directory", path)
	}
	if err := setPrivateMode(path, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return err
	}
	private, err := privateModeMatches(path, info.Mode(), 0o700)
	if err != nil {
		return fmt.Errorf("inspect operation lease directory permissions: %w", err)
	}
	if !private {
		return fmt.Errorf("operation lease directory %s permissions are not private (want 0700 or a protected owner-only policy)", path)
	}
	return nil
}

func canonicalTime(value time.Time) time.Time {
	return value.Round(0).UTC()
}
