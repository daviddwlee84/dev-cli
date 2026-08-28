package artifact

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var transcriptSessionLine = regexp.MustCompile(`^<!-- ([^-][^<]*) Session ([0-9a-fA-F-]+) \(`)

type Transcript struct {
	Path      string
	Provider  string
	SessionID string
}

type Snapshot struct {
	Size    int64
	ModTime time.Time
	SHA256  string
}

// FindTranscript selects exactly one SpecStory transcript by its anchored
// preamble session UUID. UUID mentions later in a transcript are never used.
func FindTranscript(worktree, provider, sessionID string) (Transcript, error) {
	history := filepath.Join(worktree, ".specstory", "history")
	entries, err := os.ReadDir(history)
	if err != nil {
		return Transcript{}, err
	}
	var matches []Transcript
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(history, entry.Name())
		inside, err := pathx.Contains(history, path)
		if err != nil || !inside {
			continue
		}
		gotProvider, gotSession, err := transcriptPreamble(path)
		if err != nil || gotSession != sessionID || (provider != "" && gotProvider != provider) {
			continue
		}
		matches = append(matches, Transcript{Path: path, Provider: gotProvider, SessionID: gotSession})
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Transcript{}, fmt.Errorf("no SpecStory transcript with preamble session %s:%s", provider, sessionID)
	default:
		return Transcript{}, fmt.Errorf("multiple SpecStory transcripts have preamble session %s:%s", provider, sessionID)
	}
}

func transcriptPreamble(path string) (provider, sessionID string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 16<<10))
	for line := 0; scanner.Scan() && line < 12; line++ {
		match := transcriptSessionLine.FindStringSubmatch(scanner.Text())
		if len(match) != 3 {
			continue
		}
		return normalizeProvider(match[1]), match[2], nil
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	return "", "", fmt.Errorf("SpecStory session preamble not found")
}

func normalizeProvider(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	for _, provider := range []string{"claude", "codex", "cursor", "gemini", "antigravity", "deepseek", "droid"} {
		if strings.Contains(label, provider) {
			return provider
		}
	}
	if first, _, ok := strings.Cut(label, " "); ok {
		return first
	}
	return label
}

// StableSnapshot requires the transcript bytes and metadata to remain unchanged
// across a settle interval.
func StableSnapshot(ctx context.Context, path string, settle time.Duration) (Snapshot, error) {
	if settle <= 0 {
		settle = 250 * time.Millisecond
	}
	first, err := snapshot(path)
	if err != nil {
		return Snapshot{}, err
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-time.After(settle):
	}
	second, err := snapshot(path)
	if err != nil {
		return Snapshot{}, err
	}
	if first.Size != second.Size || !first.ModTime.Equal(second.ModTime) || first.SHA256 != second.SHA256 {
		return Snapshot{}, fmt.Errorf("transcript is still changing")
	}
	return second, nil
}

func snapshot(path string) (Snapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("transcript is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Size: info.Size(), ModTime: info.ModTime(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
