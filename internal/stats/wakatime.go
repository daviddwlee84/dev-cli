package stats

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// WakaTime imports durations from the WakaTime API.
//
// It covers the time dev's own sampler cannot see: hours spent in an editor
// rather than a terminal session. The two are stored under separate sources so
// a report can choose either, or both, knowing they overlap.
type WakaTime struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

// DefaultWakaTimeURL is the public API root.
const DefaultWakaTimeURL = "https://wakatime.com/api/v1"

// APIKeyFromConfig reads api_key out of a ~/.wakatime.cfg ini file, which is
// where the editor plugins already put it — asking the user to configure the
// key a second time would be the wrong kind of setup.
func APIKeyFromConfig(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "api_key" {
			return strings.TrimSpace(val), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no api_key in %s", path)
}

type summariesResponse struct {
	Data []struct {
		Range struct {
			Date string `json:"date"`
		} `json:"range"`
		Projects []struct {
			Name         string  `json:"name"`
			TotalSeconds float64 `json:"total_seconds"`
		} `json:"projects"`
	} `json:"data"`
}

// Import fetches per-project daily totals for a date range and stores them.
//
// WakaTime's summaries endpoint caps a single request at roughly a year, and
// re-importing a day returns the same total, so entries are written with Set
// rather than accumulated.
func (w *WakaTime) Import(ctx context.Context, s *Store, since, until time.Time) (int, error) {
	if w.APIKey == "" {
		return 0, fmt.Errorf("no WakaTime API key")
	}
	base := w.BaseURL
	if base == "" {
		base = DefaultWakaTimeURL
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	url := fmt.Sprintf("%s/users/current/summaries?start=%s&end=%s",
		base, since.Format(dayFormat), until.Format(dayFormat))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	// WakaTime authenticates with the base64 of the key as a Basic credential.
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(w.APIKey)))

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("wakatime request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("wakatime returned %s", resp.Status)
	}

	var body summariesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode wakatime response: %w", err)
	}

	var entries []Entry
	for _, day := range body.Data {
		d, err := time.ParseInLocation(dayFormat, day.Range.Date, time.Local)
		if err != nil {
			continue
		}
		for _, p := range day.Projects {
			if p.TotalSeconds <= 0 {
				continue
			}
			entries = append(entries, Entry{
				Day: d, Repo: p.Name, Source: SourceWakaTime,
				Seconds: int(p.TotalSeconds),
			})
		}
	}
	if err := s.Set(entries...); err != nil {
		return 0, err
	}
	return len(entries), s.MarkCollected("wakatime", time.Now())
}
