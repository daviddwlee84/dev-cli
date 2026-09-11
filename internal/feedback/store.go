// Package feedback owns durable local reports, reviewed publication receipts,
// and immutable repair plans. Agent execution remains an explicit handoff.
package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

const MaxReportBytes = 1 << 20

var ErrStale = errors.New("feedback content or authority changed; preview again")
var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,100}$`)
var safeFilename = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,150}$`)

type Store struct{ Dir string }
type Facts struct {
	Version      string `json:"version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Installation string `json:"installation"`
}
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	CreatedAt     time.Time      `json:"created_at"`
	Title         string         `json:"title"`
	Facts         Facts          `json:"facts"`
	DraftPath     string         `json:"draft_path"`
	ContextPath   string         `json:"context_path"`
	Repair        *RepairBinding `json:"repair,omitempty"`
}
type DraftRequest struct {
	Title, Body string
	Diagnostic  []byte
	Facts       Facts
}
type DraftResult struct {
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	Report        Report   `json:"report"`
	Next          []string `json:"next"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s Store) reportDir(id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", errors.New("invalid feedback ID")
	}
	return filepath.Join(s.Dir, id), nil
}
func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		if _, e := os.Stat(parent); errors.Is(e, fs.ErrNotExist) {
			if e = ensurePrivateDir(parent); e != nil {
				return e
			}
		}
		if err = makePrivateDir(path); err != nil && !errors.Is(err, fs.ErrExist) {
			return errors.New("cannot create private feedback directory")
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe feedback directory")
	}
	return checkPrivate(path, info, true)
}
func (s Store) open(id string) (*os.Root, fs.FileInfo, string, error) {
	path, err := s.reportDir(id)
	if err != nil {
		return nil, nil, "", err
	}
	root, info, err := safefile.OpenRoot(path)
	if err != nil {
		return nil, nil, "", errors.New("feedback report not found or unsafe")
	}
	if err = checkPrivate(path, info, true); err != nil {
		root.Close()
		return nil, nil, "", err
	}
	return root, info, path, nil
}
func (s Store) read(ctx context.Context, id, name string) ([]byte, error) {
	if !safeFilename.MatchString(name) {
		return nil, errors.New("invalid feedback filename")
	}
	root, identity, path, err := s.open(id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, info, err := safefile.ReadStableRegular(ctx, root, name, nil, MaxReportBytes)
	if err != nil {
		return nil, err
	}
	if err = checkPrivate(filepath.Join(path, name), info, false); err != nil {
		return nil, err
	}
	if err = safefile.VerifyRoot(path, identity); err != nil {
		return nil, ErrStale
	}
	return data, nil
}
func (s Store) write(ctx context.Context, id, name string, data []byte, replace bool) error {
	if len(data) > MaxReportBytes || !safeFilename.MatchString(name) {
		return errors.New("invalid or oversized feedback artifact")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, identity, path, err := s.open(id)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = fleet.WritePrivateConfigFile(filepath.Join(path, name), data, replace); err != nil {
		return errors.New("cannot save private feedback artifact; retained report may require inspection")
	}
	return safefile.VerifyRoot(path, identity)
}
func (s Store) writeJSON(ctx context.Context, id, name string, value any, replace bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return s.write(ctx, id, name, append(data, '\n'), replace)
}
func (s Store) withLock(ctx context.Context, id string, fn func() error) error {
	_, err := s.reportDir(id)
	if err != nil {
		return err
	}
	root, info, err := safefile.OpenRoot(s.Dir)
	if err != nil {
		return errors.New("feedback store unavailable")
	}
	defer root.Close()
	if err = checkPrivate(s.Dir, info, true); err != nil {
		return err
	}
	return lockx.WithFile(ctx, filepath.Join(s.Dir, "."+id+".lock"), "feedback report", func() error {
		if err := safefile.VerifyRoot(s.Dir, info); err != nil {
			return ErrStale
		}
		return fn()
	})
}
func (s Store) Load(ctx context.Context, id string) (Report, error) {
	var r Report
	data, err := s.read(ctx, id, "report.json")
	if err != nil {
		return r, err
	}
	if json.Unmarshal(data, &r) != nil || r.SchemaVersion != 1 || r.ID != id {
		return r, errors.New("invalid feedback report")
	}
	path, _ := s.reportDir(id)
	r.DraftPath = filepath.Join(path, "public.md")
	r.ContextPath = filepath.Join(path, "context.json")
	return r, nil
}
func (s Store) Create(ctx context.Context, request DraftRequest) (DraftResult, error) {
	result := DraftResult{SchemaVersion: 1, Kind: "feedback_draft"}
	if len(request.Body) > MaxReportBytes/2 || len(request.Title) == 0 || len(request.Title) > 200 || Sanitize(request.Title) == "" || strings.ContainsAny(request.Title, "\r\n\t") {
		return result, errors.New("provide a title (1–200 bytes) and a report body no larger than 512 KiB")
	}
	body, err := publicBody(request)
	if err != nil {
		return result, err
	}
	private := struct {
		Body       string          `json:"user_supplied_body"`
		Diagnostic json.RawMessage `json:"diagnostic,omitempty"`
	}{request.Body, request.Diagnostic}
	privateData, err := json.MarshalIndent(private, "", "  ")
	if err != nil || len(privateData) > MaxReportBytes || len(body) > MaxReportBytes {
		return result, errors.New("combined feedback artifacts exceed the 1 MiB limit")
	}
	absolute, err := filepath.Abs(s.Dir)
	if err != nil {
		return result, err
	}
	s.Dir = absolute
	if err = ensurePrivateDir(s.Dir); err != nil {
		return result, err
	}
	id := uuid.NewString()
	path, _ := s.reportDir(id)
	if err = makePrivateDir(path); err != nil {
		return result, errors.New("cannot create feedback report directory")
	}
	r := Report{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC(), Title: Sanitize(request.Title), Facts: safeFacts(request.Facts), DraftPath: filepath.Join(path, "public.md"), ContextPath: filepath.Join(path, "context.json")}
	result.Report = r
	if err = s.write(ctx, id, "public.md", []byte(body), false); err != nil {
		return result, err
	}
	// Raw command logs/config/session history are never collected automatically.
	if err = s.write(ctx, id, "context.json", privateData, false); err != nil {
		return result, err
	}

	if err = s.writeJSON(ctx, id, "report.json", r, false); err != nil {
		return result, err
	}
	result.Next = []string{"dev feedback issue " + id, "dev feedback repair " + id}
	return result, nil
}
