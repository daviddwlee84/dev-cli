package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	releasesURL      = "https://api.github.com/repos/daviddwlee84/dev-cli/releases/latest"
	releaseCheckTTL  = 24 * time.Hour
	releaseCheckFile = "release-check.json"
)

// buildDescription splits `git describe` output into the release it was built
// from and how far past it the build is. A binary reporting v0.2.0-12-gabc1234
// is twelve commits past a release, which is worth saying out loud: it is the
// difference between "I am on v0.2.0" and "I am on something that is not any
// published version".
func buildDescription(version string) (release string, ahead int, dev bool) {
	if version == "" || version == "dev" {
		return "", 0, true
	}
	trimmed := strings.TrimSuffix(strings.TrimSuffix(version, "+dirty"), "-dirty")
	parts := strings.Split(trimmed, "-")
	if len(parts) >= 3 {
		// git describe: v0.1.11-6-gda7d7ef — the -ldflags path, and what CI asserts.
		if n, err := strconv.Atoi(parts[len(parts)-2]); err == nil && strings.HasPrefix(parts[len(parts)-1], "g") {
			return strings.Join(parts[:len(parts)-2], "-"), n, true
		}
		// Go pseudo-version: v0.1.12-0.20260829042323-da7d7ef4d6ca, produced by
		// `go install ...@<commit>`. Its base version is synthesised by Go and
		// names no published release, so there is no useful distance to report.
		if isPseudoVersion(parts) {
			return "", 0, true
		}
	}
	return trimmed, 0, false
}

// isPseudoVersion recognises Go's untagged-commit version suffix: a 14-digit
// UTC timestamp followed by a 12-character commit prefix.
func isPseudoVersion(parts []string) bool {
	stamp := strings.TrimPrefix(parts[len(parts)-2], "0.")
	hash := parts[len(parts)-1]
	if len(stamp) != 14 || len(hash) != 12 {
		return false
	}
	if _, err := strconv.ParseUint(stamp, 10, 64); err != nil {
		return false
	}
	for _, r := range hash {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// versionSummary is the one-line description dev doctor shows. It never touches
// the network: a doctor run must stay usable offline and instant.
func versionSummary() string {
	version := versionFromBuild()
	release, ahead, dev := buildDescription(version)
	switch {
	case release == "":
		return version + " — built from source with no release tag in reach"
	case ahead > 0:
		return fmt.Sprintf("%s — %d commit(s) past %s, not a published release", version, ahead, release)
	case dev:
		return version + " — a development build"
	default:
		return version
	}
}

type releaseCheck struct {
	CheckedAt time.Time `json:"checked_at"`
	TagName   string    `json:"tag_name"`
}

func releaseCheckPath() string {
	return filepath.Join(config.CacheHome(), "dev", releaseCheckFile)
}

func readReleaseCheck() (releaseCheck, bool) {
	raw, err := os.ReadFile(releaseCheckPath())
	if err != nil {
		return releaseCheck{}, false
	}
	var cached releaseCheck
	if err := json.Unmarshal(raw, &cached); err != nil || cached.TagName == "" {
		return releaseCheck{}, false
	}
	return cached, time.Since(cached.CheckedAt) < releaseCheckTTL
}

func writeReleaseCheck(c releaseCheck) {
	path := releaseCheckPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if raw, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(path, raw, 0o644)
	}
}

// latestRelease asks GitHub for the newest published release, preferring a
// cached answer. It is only ever called from an explicit --check: a version
// string must never depend on the network being up.
func latestRelease(ctx context.Context, refresh bool) (string, error) {
	if !refresh {
		if cached, fresh := readReleaseCheck(); fresh {
			return cached.TagName, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("github returned no tag name")
	}
	writeReleaseCheck(releaseCheck{CheckedAt: time.Now(), TagName: payload.TagName})
	return payload.TagName, nil
}

func newVersionCmd(app *App) *cobra.Command {
	var check, refresh bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Report the running version, and optionally whether it is current",
		Long: `Print the version this binary was built as.

With --check, also ask GitHub for the newest published release and say whether
this build is behind it. That is the only part of dev that reaches the network
without being asked to, so it is opt-in: a bare 'dev version', 'dev --version'
and 'dev doctor' stay local and instant. The answer is cached for a day under
$XDG_CACHE_HOME/dev, and a machine that is offline reports what it knows rather
than failing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			style := app.outStyle()
			fmt.Fprintf(app.Out, "dev version %s\n", versionSummary())
			if !check {
				return nil
			}
			latest, err := latestRelease(ctxOf(), refresh)
			if err != nil {
				// Being unable to reach GitHub is not a failure of the command
				// the user ran; it just means the second line is unavailable.
				fmt.Fprintf(app.Out, "%s\n", style.dim("latest release unknown: "+err.Error()))
				return nil
			}
			release, ahead, _ := buildDescription(versionFromBuild())
			switch {
			case release == latest && ahead == 0:
				fmt.Fprintf(app.Out, "%s\n", style.success("this is the latest release"))
			case release == latest:
				fmt.Fprintf(app.Out, "latest release %s — this build is %d commit(s) past it\n",
					style.success(latest), ahead)
			default:
				fmt.Fprintf(app.Out, "latest release %s — %s\n", style.warning(latest),
					style.warning("this build is not it"))
				fmt.Fprintln(app.Out, style.dim("  brew upgrade dev-cli"))
				fmt.Fprintln(app.Out, style.dim("  go install github.com/daviddwlee84/dev-cli/cmd/dev@latest"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "ask GitHub for the newest published release")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ignore the cached answer from a previous --check")
	return cmd
}
