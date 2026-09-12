package dotfile

import (
	"os"
	"runtime"
	"strings"
)

type Recommendation struct {
	Preset       string `json:"preset"`
	RepoURL      string `json:"repo_url"`
	BootstrapURL string `json:"bootstrap_url"`
	Bootstrap    string `json:"bootstrap,omitempty"`
	Experimental bool   `json:"experimental"`
}

func Recommended(platform string) *Recommendation {
	repo := ""
	experimental := false
	switch platform {
	case "darwin", "linux", "wsl":
		repo = "dotfiles"
	case "windows":
		repo = "dotfiles-windows"
	case "termux":
		repo, experimental = "dotfiles-Termux", true
	case "ish":
		repo, experimental = "dotfiles-iSH", true
	case "openwrt":
		repo, experimental = "dotfiles-OpenWrt", true
	default:
		return nil
	}
	url := "https://github.com/daviddwlee84/" + repo
	r := &Recommendation{Preset: "david", RepoURL: url + ".git", BootstrapURL: url + "/blob/main/README.md", Experimental: experimental}
	if experimental {
		r.Bootstrap = "sh bootstrap.sh"
		if platform == "termux" {
			r.Bootstrap = "bash bootstrap.sh setup"
		}
	}
	return r
}

func DetectPlatform() string {
	osRelease, _ := readBounded("/etc/os-release", 64<<10)
	kernel, _ := readBounded("/proc/sys/kernel/osrelease", 4096)
	_, ishErr := os.Stat("/proc/ish")
	_, openwrtErr := os.Stat("/etc/openwrt_release")
	return platformFrom(runtime.GOOS, os.Getenv("PREFIX"), os.Getenv("TERMUX_VERSION"), string(osRelease), string(kernel), ishErr == nil, openwrtErr == nil)
}

func platformFrom(goos, prefix, termuxVersion, osRelease, kernel string, ish, openwrt bool) string {
	if goos != "linux" && goos != "android" {
		return goos
	}
	if ish || strings.Contains(strings.ToLower(kernel), "ish") {
		return "ish"
	}
	if openwrt || strings.Contains(strings.ToLower(osRelease), "id=openwrt") || strings.Contains(strings.ToLower(osRelease), `id="openwrt"`) {
		return "openwrt"
	}
	if strings.Contains(strings.ToLower(kernel), "microsoft") {
		return "wsl"
	}
	if termuxVersion != "" || strings.Contains(prefix, "/com.termux/") {
		// A distro release file with inherited Termux hints commonly describes
		// a PRoot guest. Do not offer native Android bootstrap to that guest.
		if strings.TrimSpace(osRelease) != "" {
			return "proot"
		}
		return "termux"
	}
	return goos
}
