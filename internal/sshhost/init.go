package sshhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

type initPlanState struct {
	serviceID uint64
	action    PlanAction
	path      string
	source    fileSnapshot
	desired   []byte
	writeRoot bool
}

// PlanInit prepares, but never applies, the one top-level managed Include. It
// also proves that an existing root file's security metadata is representable
// by the platform backend. Any blocked result is produced without filesystem
// writes, directory creation, or lock-file creation.
func (s *Service) PlanInit(ctx context.Context) (InitPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return InitPlan{}, err
	}
	plan := InitPlan{
		Path: s.paths.RootConfig, ManagedDir: s.paths.ManagedDir, Include: ManagedInclude, Mode: 0o600,
	}
	block := func(code, message string) (InitPlan, error) {
		plan.Action = ActionBlocked
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Code: code, Message: message + "; place " + rootIncludeLine() + " before the first Host or Match directive manually",
			Path: s.paths.RootConfig, BlocksMutation: true,
		})
		return plan, nil
	}
	if err := inspectExistingTree(s.paths); err != nil {
		return block("unsafe_tree", err.Error())
	}
	if diagnostics := s.inspectManagedNamespace(); len(diagnostics) > 0 {
		plan.Action = ActionBlocked
		plan.Diagnostics = append(plan.Diagnostics, diagnostics...)
		return plan, nil
	}
	managedDirExists := false
	if _, err := os.Lstat(s.paths.ManagedDir); err == nil {
		managedDirExists = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return block("managed_namespace_unreadable", err.Error())
	}

	source, err := readSecureFileIfExists(s.paths.RootConfig, true)
	if err != nil {
		return block("root_metadata_unsafe", err.Error())
	}
	plan.BeforeDigest = source.digest
	plan.Mode = 0o600
	if source.info != nil {
		plan.Mode = source.info.Mode().Perm()
	}
	desired, offset, newline, bom, present, err := insertRootInclude(source.data)
	if err != nil {
		return block("root_parse_unsafe", err.Error())
	}
	plan.InsertOffset = offset
	plan.Newline = newline
	plan.BOM = bom
	plan.AfterDigest = digestBytes(desired)
	state := &initPlanState{
		serviceID: s.id, source: source, desired: append([]byte(nil), desired...), writeRoot: !present,
	}
	switch {
	case present && managedDirExists:
		plan.Action = ActionNoop
	case present:
		plan.Action = ActionCreate // create only the secure managed directory
	case !source.exists:
		plan.Action = ActionCreate
	default:
		plan.Action = ActionUpdate
	}
	state.action = plan.Action
	state.path = plan.Path
	plan.state = state
	return plan, nil
}

// ApplyInit applies a ready PlanInit. No API removes a successful Include.
func (s *Service) ApplyInit(ctx context.Context, plan InitPlan) (InitResult, error) {
	result := InitResult{Action: plan.Action, Path: plan.Path}
	if plan.Action == ActionBlocked || plan.state == nil {
		return result, ErrBlocked
	}
	if plan.state.serviceID != s.id || plan.Path != s.paths.RootConfig || plan.Path != plan.state.path || plan.Action != plan.state.action {
		return result, errors.New("init plan was not produced by this service or its public fields were modified")
	}
	if _, err := snapshotStillCurrent(plan.state.source); err != nil {
		current, currentErr := s.PlanInit(ctx)
		if currentErr == nil && current.state != nil && !current.state.writeRoot {
			return s.ApplyInit(ctx, current)
		}
		return result, fmt.Errorf("revalidate root config before init: %w", ErrSourceChanged)
	}
	if plan.Action == ActionNoop {
		current, err := s.PlanInit(ctx)
		if err != nil {
			return result, err
		}
		if !current.Ready() {
			return result, fmt.Errorf("revalidate initialized SSH config: %w", ErrSourceChanged)
		}
		if current.Action != ActionNoop {
			return s.ApplyInit(ctx, current)
		}
		result.Digest = current.AfterDigest
		return result, nil
	}
	managedExisted := true
	if _, err := os.Lstat(s.paths.ManagedDir); errors.Is(err, fs.ErrNotExist) {
		managedExisted = false
	}
	if err := ensureMutationTree(s.paths); err != nil {
		return result, err
	}
	err := lockx.WithDir(ctx, s.paths.ManagedDir, "SSH root config", func() error {
		if diagnostics := s.inspectManagedNamespace(); len(diagnostics) > 0 {
			return fmt.Errorf("managed namespace changed: %s: %w", diagnostics[0].Message, ErrSourceChanged)
		}
		if !plan.state.writeRoot {
			if _, err := snapshotStillCurrent(plan.state.source); err != nil {
				return fmt.Errorf("root config changed before directory initialization: %w", ErrSourceChanged)
			}
			result.Action = ActionCreate
			result.Changed = !managedExisted
			if !result.Changed {
				result.Action = ActionNoop
			}
			result.Digest = plan.AfterDigest
			return nil
		}
		if current, ok := bytesAlreadyDesired(plan.Path, plan.state.desired); ok {
			result.Action = ActionNoop
			result.Digest = current.digest
			return nil
		}
		if _, err := snapshotStillCurrent(plan.state.source); err != nil {
			return fmt.Errorf("root config changed before staging: %w", ErrSourceChanged)
		}
		var metadata *fileMetadata
		if plan.state.source.hasMeta {
			metadata = &plan.state.source.metadata
		}
		staged, err := createStagedFile(s.paths.SSHDir, plan.state.desired, metadata)
		if err != nil {
			return err
		}
		defer staged.discard()
		if s.beforeInitCommit != nil {
			s.beforeInitCommit()
		}
		if _, err := snapshotStillCurrent(plan.state.source); err != nil {
			return fmt.Errorf("root config changed before publication: %w", ErrSourceChanged)
		}
		if plan.state.source.exists {
			err = commitReplace(staged, plan.Path, plan.state.source)
		} else {
			err = commitNoReplace(staged, plan.Path, plan.state.source)
		}
		if err != nil {
			return err
		}
		written, err := readSecureFile(plan.Path, true)
		if err != nil {
			return fmt.Errorf("verify root config publication: %w", err)
		}
		if !bytes.Equal(written.data, plan.state.desired) {
			return errors.New("root config publication did not retain desired bytes")
		}
		result.Action = plan.Action
		result.Changed = true
		result.Digest = written.digest
		return nil
	})
	return result, err
}

func rootIncludeLine() string { return "Include " + ManagedInclude }

func insertRootInclude(data []byte) (desired []byte, offset int, newline string, bom, present bool, err error) {
	const bomSize = 3
	bom = bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	contentOffset := 0
	if bom {
		contentOffset = bomSize
	}
	if !utf8.Valid(data[contentOffset:]) {
		return nil, 0, "", bom, false, errors.New("root config is not valid UTF-8")
	}
	if bytes.IndexByte(data[contentOffset:], 0) >= 0 {
		return nil, 0, "", bom, false, errors.New("root config contains NUL")
	}
	newline = detectNewline(data[contentOffset:])
	firstGuard := -1
	for lineStart := contentOffset; lineStart <= len(data); {
		lineEnd := bytes.IndexByte(data[lineStart:], '\n')
		next := len(data) + 1
		if lineEnd < 0 {
			lineEnd = len(data)
		} else {
			lineEnd += lineStart
			next = lineEnd + 1
		}
		line := data[lineStart:lineEnd]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		directive, arguments, empty, parseErr := parseConfigLine(string(line))
		if parseErr != nil {
			return nil, 0, newline, bom, false, fmt.Errorf("malformed root line at byte %d: %w", lineStart, parseErr)
		}
		if !empty {
			if strings.EqualFold(directive, "host") || strings.EqualFold(directive, "match") {
				if firstGuard < 0 {
					firstGuard = lineStart
				}
			} else if firstGuard < 0 && strings.EqualFold(directive, "include") {
				for _, argument := range arguments {
					if argument == ManagedInclude {
						return append([]byte(nil), data...), lineStart, newline, bom, true, nil
					}
				}
				// A prior Include may leave a Host/Match guard active in the static
				// inline model, so install the dedicated Include before it.
				firstGuard = lineStart
			}
		}
		if next > len(data) {
			break
		}
		lineStart = next
	}
	line := []byte(rootIncludeLine() + newline)
	if firstGuard >= 0 {
		desired = make([]byte, 0, len(data)+len(line))
		desired = append(desired, data[:firstGuard]...)
		desired = append(desired, line...)
		desired = append(desired, data[firstGuard:]...)
		return desired, firstGuard, newline, bom, false, nil
	}
	offset = len(data)
	desired = append([]byte(nil), data...)
	if len(data) > contentOffset && !bytes.HasSuffix(data, []byte{'\n'}) {
		desired = append(desired, newline...)
	}
	desired = append(desired, line...)
	return desired, offset, newline, bom, false, nil
}

func detectNewline(data []byte) string {
	if index := bytes.IndexByte(data, '\n'); index >= 0 && index > 0 && data[index-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}
