package selfupdate

import (
	"encoding/json"
	"testing"
)

func releaseJSON(tag string, assets ...string) []byte {
	rows := make([]map[string]string, 0, len(assets))
	for _, name := range assets {
		rows = append(rows, map[string]string{"name": name})
	}
	data, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": rows})
	return data
}

func TestSelectReleasePlatform(t *testing.T) {
	const tag = "v0.2.34"
	metadata := releaseJSON(tag, "dev-cli_"+tag+"_linux_arm64.tar.gz", "dev-cli_"+tag+"_windows_arm64.zip", "SHA256SUMS")
	for _, tc := range []struct {
		goos, asset string
		source      bool
	}{
		{"android", "dev-cli_v0.2.34_android_arm64.tar.gz", true},
		{"linux", "dev-cli_v0.2.34_linux_arm64.tar.gz", false},
		{"windows", "dev-cli_v0.2.34_windows_arm64.zip", false},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			plan, err := Select(tag, tc.goos, "arm64", metadata)
			if err != nil || plan.Source != tc.source || plan.Asset != tc.asset {
				t.Fatalf("plan = %+v, err = %v", plan, err)
			}
		})
	}
	// Future Android releases automatically take the verified binary path.
	plan, err := Select(tag, "android", "arm64", releaseJSON(tag, "dev-cli_v0.2.34_android_arm64.tar.gz", "SHA256SUMS"))
	if err != nil || plan.Source {
		t.Fatalf("published Android binary: %+v, %v", plan, err)
	}
	plan, err = Select(tag, "android", "arm64", releaseJSON(tag, "dev-cli_v0.2.34_source.tar.gz", "SHA256SUMS"))
	if err != nil || !plan.Source || plan.SourceAsset != "dev-cli_v0.2.34_source.tar.gz" {
		t.Fatalf("compact source archive: %+v, %v", plan, err)
	}
}

func TestSelectRejectsIncompleteReleaseEvidence(t *testing.T) {
	const tag = "v0.2.34"
	for name, data := range map[string][]byte{
		"missing checksums": releaseJSON(tag, "dev-cli_v0.2.34_android_arm64.tar.gz"),
		"source checksums":  releaseJSON(tag, "dev-cli_v0.2.34_source.tar.gz"),
		"different tag":     releaseJSON("v0.2.33"),
		"missing assets":    []byte(`{"tag_name":"v0.2.34"}`),
		"draft":             []byte(`{"tag_name":"v0.2.34","draft":true,"assets":[]}`),
		"prerelease":        []byte(`{"tag_name":"v0.2.34","prerelease":true,"assets":[]}`),
		"duplicate":         releaseJSON(tag, "SHA256SUMS", "SHA256SUMS"),
		"invalid JSON":      []byte(`{"assets":`),
	} {
		t.Run(name, func(t *testing.T) {
			if plan, err := Select(tag, "android", "arm64", data); err == nil {
				t.Fatalf("must not infer absent platform from bad evidence: %+v", plan)
			}
		})
	}
	for _, tag := range []string{"latest", "../v0.2.34", "v0.2.34-rc.1", "v01.2.34", "v0.2.34?x=y"} {
		if _, err := Select(tag, "android", "arm64", releaseJSON(tag)); err == nil {
			t.Errorf("accepted invalid tag %q", tag)
		}
	}
}
