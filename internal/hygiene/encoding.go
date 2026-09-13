package hygiene

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// EncodingIssue describes bytes without disclosing their contents. Positions
// refer to the original input: byte offsets are zero-based, lines one-based.
type EncodingIssue struct {
	Reason           string `json:"reason"`
	ByteOffset       int    `json:"byte_offset"`
	Line             int    `json:"line"`
	InvalidBytes     int    `json:"invalid_bytes"`
	InvalidSequences int    `json:"invalid_sequences"`
	NULBytes         int    `json:"nul_bytes,omitempty"`
}

func inspectEncoding(data []byte) *EncodingIssue {
	if utf8.Valid(data) && !bytes.Contains(data, []byte{0}) {
		return nil
	}
	issue := &EncodingIssue{ByteOffset: -1}
	line, inInvalid := 1, false
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		invalid := r == utf8.RuneError && size == 1
		if invalid || data[i] == 0 {
			if issue.ByteOffset < 0 {
				issue.ByteOffset, issue.Line = i, line
				issue.Reason = "invalid_utf8"
				if data[i] == 0 {
					issue.Reason = "nul_byte"
				}
			}
			if invalid {
				issue.InvalidBytes++
				if !inInvalid {
					issue.InvalidSequences++
				}
			} else {
				issue.NULBytes++
			}
		}
		inInvalid = invalid
		if data[i] == '\n' {
			line++
		}
		i += size
	}
	// Recognize BOMs rather than guessing how legacy encodings should decode.
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}), bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff}):
		issue.Reason = "utf32_bom"
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		issue.Reason = "utf16_bom"
	}
	return issue
}

func binaryPath(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".woff", ".woff2", ".ttf", ".zip", ".gz", ".exe", ".dll", ".so", ".db", ".sqlite":
		return true
	}
	return false
}

func validRepairPath(file string) bool {
	if !validRelative(file) || strings.ContainsAny(file, `\:`) {
		return false
	}
	for _, component := range strings.Split(file, "/") {
		// Git metadata is never an encoding-repair target, including nested
		// repositories and case/Win32-normalization aliases of .git.
		if strings.EqualFold(strings.TrimRight(component, " ."), ".git") {
			return false
		}
	}
	return true
}

// repairUTF8 replaces each contiguous run of undecodable bytes once. A valid
// encoded RuneError is ordinary content, and remove must produce a non-nil
// empty slice so the transaction cannot interpret it as file deletion.
func repairUTF8(ctx context.Context, data []byte, issue *EncodingIssue, mode string, limit int) ([]byte, error) {
	if mode != "replace" && mode != "remove" {
		return nil, errors.New("invalid-byte policy must be replace or remove")
	}
	if len(data) > limit {
		return nil, errors.New("encoding repair exceeds file limit")
	}
	if issue == nil {
		return data, nil
	}
	if issue.NULBytes > 0 || issue.Reason == "utf16_bom" || issue.Reason == "utf32_bom" {
		return nil, errors.New("encoding repair requires UTF-8 text; NUL bytes and UTF-16/32 need explicit encoding review")
	}
	replacement := []byte("�")
	if mode == "remove" {
		replacement = nil
	}
	size := len(data) - issue.InvalidBytes + issue.InvalidSequences*len(replacement)
	if size > limit {
		return nil, errors.New("encoding repair output exceeds file limit")
	}
	out := make([]byte, 0, size)
	start, nextCheck := 0, 0
	for i := 0; i < len(data); {
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			nextCheck = i + (1 << 20)
		}
		r, n := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || n != 1 {
			i += n
			continue
		}
		out = append(out, data[start:i]...)
		out = append(out, replacement...)
		for i < len(data) {
			if i >= nextCheck {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				nextCheck = i + (1 << 20)
			}
			r, n = utf8.DecodeRune(data[i:])
			if r != utf8.RuneError || n != 1 {
				break
			}
			i++
		}
		start = i
	}
	out = append(out, data[start:]...)
	return out, ctx.Err()
}

// PreviewRepairEncoding only selects explicit working files. Index contents
// never authorize a working-file replacement and are never staged by Apply.
func (s *Service) PreviewRepairEncoding(ctx context.Context, files []string, mode string) (Plan, error) {
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "remove" {
		return Plan{}, errors.New("invalid-byte policy must be replace or remove")
	}
	if len(files) == 0 || len(files) > 256 {
		return Plan{}, errors.New("select between 1 and 256 files with --file")
	}
	selected := append([]string(nil), files...)
	sort.Strings(selected)
	for i, file := range selected {
		if !validRepairPath(file) || (i > 0 && file == selected[i-1]) {
			return Plan{}, errors.New("select distinct repository-relative files outside .git")
		}
		if binaryPath(file) {
			return Plan{}, errors.New("selected file has a recognized binary extension; encoding repair requires UTF-8 text")
		}
	}
	p, err := s.newPlan(ctx, "hygiene_repair_encoding")
	if err != nil {
		return Plan{}, err
	}
	p.Plan.Notices = []string{"Invalid bytes: " + mode + ". Working files only; the Git index is unchanged. Review and stage selected changes, then rerun hygiene scan. Encoding repair is not a secret scan."}
	totalDesired := 0
	for _, file := range selected {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		path := filepath.Join(s.Root, filepath.FromSlash(file))
		data, err := safefile.ReadStablePath(ctx, path, MaxFileBytes)
		if err != nil {
			return Plan{}, errors.New("selected file cannot be read safely within the 128 MiB limit")
		}
		issue := inspectEncoding(data)
		after, err := repairUTF8(ctx, data, issue, mode, MaxFileBytes)
		if err != nil {
			return Plan{}, err
		}
		if issue == nil {
			p.Plan.Notices = append(p.Plan.Notices, fmt.Sprintf("%s: valid UTF-8; no repair needed.", s.displayPath(file)))
			continue
		}
		// Bound retained proposals before loading hundreds of large selections.
		// Leave room for base64 encoding and plan/review metadata on disk.
		totalDesired += len(after)
		if totalDesired > MaxRecordBytes/2 {
			return Plan{}, errors.New("encoding repair plan exceeds the 192 MiB proposal budget; select fewer files")
		}
		if err = s.addChange(ctx, &p, file, path, after, issue.InvalidSequences); err != nil {
			return Plan{}, err
		}
		// addChange independently observes the source. Reject a change between
		// analysis and that observation instead of binding stale desired bytes.
		if len(p.Changes) == 0 || p.Changes[len(p.Changes)-1].Path != path || p.Changes[len(p.Changes)-1].BeforeDigest != digest(data) {
			return Plan{}, ErrStale
		}
		p.Plan.Files[len(p.Plan.Files)-1].Encoding = issue
		if isArtifact(file) {
			p.Plan.RequiresWriterStopped = true
		}
	}
	if err = s.savePlan(ctx, &p); err != nil {
		return Plan{}, err
	}
	return p.Plan, nil
}
