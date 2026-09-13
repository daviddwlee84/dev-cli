// Package selfupdate selects release artifacts and stages native source builds.
// The caller owns confirmation and replacement of the running executable.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Plan is selected from the asset inventory of an exact published release.
// A broken download or missing checksum is never evidence of an absent platform.
type Plan struct {
	Tag         string
	Asset       string
	Source      bool
	SourceAsset string
}

func ValidateTag(tag string) error {
	if !stableTag.MatchString(tag) {
		return fmt.Errorf("unsupported release tag %q: expected vMAJOR.MINOR.PATCH", tag)
	}
	return nil
}

func Select(tag, goos, goarch string, metadata []byte) (Plan, error) {
	if err := ValidateTag(tag); err != nil {
		return Plan{}, err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(metadata, &release); err != nil {
		return Plan{}, fmt.Errorf("read release assets: %w", err)
	}
	if release.Tag != tag || release.Draft || release.Prerelease || release.Assets == nil {
		return Plan{}, fmt.Errorf("invalid asset inventory for published release %s", tag)
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	plan := Plan{Tag: tag, Asset: fmt.Sprintf("dev-cli_%s_%s_%s.%s", tag, goos, goarch, ext), Source: true}
	seen := make(map[string]bool)
	for _, asset := range release.Assets {
		if asset.Name == "" || seen[asset.Name] {
			return Plan{}, fmt.Errorf("invalid or duplicate release asset %q", asset.Name)
		}
		seen[asset.Name] = true
		if asset.Name == plan.Asset {
			plan.Source = false
		}
	}
	if source := "dev-cli_" + tag + "_source.tar.gz"; plan.Source && seen[source] {
		plan.SourceAsset = source
	}
	if (!plan.Source || plan.SourceAsset != "") && !seen["SHA256SUMS"] {
		return Plan{}, fmt.Errorf("release %s has an archive but no SHA256SUMS; refusing to install", tag)
	}
	return plan, nil
}
