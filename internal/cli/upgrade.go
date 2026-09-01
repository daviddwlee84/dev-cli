package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const releaseDownloadBase = "https://github.com/daviddwlee84/dev-cli/releases/download"

type installMethod int

const (
	methodStandalone installMethod = iota
	methodHomebrew
	methodScoop
	methodGo
)

type detectedInstall struct {
	Path     string
	Resolved string
	Method   installMethod
}

func (m installMethod) label() string {
	switch m {
	case methodHomebrew:
		return "Homebrew"
	case methodScoop:
		return "Scoop"
	case methodGo:
		return "go install"
	}
	return "a manual install"
}

func (m installMethod) command() string {
	name, args := m.commandArgs()
	if name == "" {
		return ""
	}
	return strings.Join(append([]string{name}, args...), " ")
}

func (m installMethod) commandArgs() (string, []string) {
	switch m {
	case methodHomebrew:
		return "brew", []string{"upgrade", "dev-cli"}
	case methodScoop:
		return "scoop", []string{"update", "dev-cli"}
	case methodGo:
		return "go", []string{"install", "github.com/daviddwlee84/dev-cli/cmd/dev@latest"}
	}
	return "", nil
}

// detectInstallMethod guesses who owns the binary at exe. A package manager's
// copy must be updated through that manager, not replaced underneath it.
func detectInstallMethod(exe string) installMethod {
	lower := strings.ToLower(filepath.ToSlash(exe))
	switch {
	case strings.Contains(lower, "/cellar/dev-cli/"),
		strings.Contains(lower, "/homebrew/"),
		strings.Contains(lower, "/linuxbrew/"):
		return methodHomebrew
	case strings.Contains(lower, "/scoop/apps/dev-cli/"),
		strings.Contains(lower, "/scoop/shims/"):
		return methodScoop
	}
	for _, dir := range goBinDirs() {
		if dir != "" && underDir(exe, dir) {
			return methodGo
		}
	}
	return methodStandalone
}

func detectInstall(exe string) detectedInstall {
	path := exe
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	resolved := path
	if target, err := filepath.EvalSymlinks(path); err == nil {
		resolved = target
	}
	return detectedInstall{Path: path, Resolved: resolved, Method: detectInstallMethod(resolved)}
}

func runningInstall() (detectedInstall, error) {
	self, err := os.Executable()
	if err != nil {
		return detectedInstall{}, err
	}
	return detectInstall(self), nil
}

func devInstallsOnPath() []detectedInstall {
	name := "dev"
	if goruntime.GOOS == "windows" {
		name = "dev.exe"
	}
	seen := map[string]bool{}
	var installs []detectedInstall
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || (goruntime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
			continue
		}
		install := detectInstall(candidate)
		key := install.Resolved
		if goruntime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		installs = append(installs, install)
	}
	return installs
}

func otherDevInstalls(current detectedInstall, installs []detectedInstall) []detectedInstall {
	currentKey := current.Resolved
	if goruntime.GOOS == "windows" {
		currentKey = strings.ToLower(currentKey)
	}
	var others []detectedInstall
	for _, install := range installs {
		key := install.Resolved
		if goruntime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if key != currentKey {
			others = append(others, install)
		}
	}
	return others
}

func goBinDirs() []string {
	var dirs []string
	if v := os.Getenv("GOBIN"); v != "" {
		dirs = append(dirs, v)
	}
	if v := os.Getenv("GOPATH"); v != "" {
		for _, p := range filepath.SplitList(v) {
			dirs = append(dirs, filepath.Join(p, "bin"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	return dirs
}

func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func newUpgradeCmd(app *App) *cobra.Command {
	var checkOnly, force, assumeYes bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update dev to the latest published release",
		Long: `Update the running dev installation to the newest published release.

dev asks GitHub for the latest release tag, compares it to this build, verifies
the downloaded archive against the release's SHA256SUMS, and swaps the binary
atomically. If a package manager owns the install (Homebrew, Scoop, or
go install), dev runs that manager's upgrade command instead of touching the
file itself.

  dev upgrade            # update through the detected owner after confirmation
  dev upgrade --check    # only report whether a newer release exists
  dev upgrade --yes      # no prompt
  dev upgrade --force    # run the update path even when already current`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(app, checkOnly, force, assumeYes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report whether a newer release exists and exit")
	cmd.Flags().BoolVar(&force, "force", false, "run the update path even if this build is already current")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "do not prompt before updating")
	return cmd
}

func runUpgrade(app *App, checkOnly, force, assumeYes bool) error {
	style := app.outStyle()
	ctx, cancel := context.WithTimeout(ctxOf(), 60*time.Second)
	defer cancel()

	latest, err := latestRelease(ctx, true)
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	current := versionFromBuild()
	release, ahead, _ := buildDescription(current)

	fmt.Fprintf(app.Out, "current: %s\n", versionSummary())
	fmt.Fprintf(app.Out, "latest:  %s\n", latest)

	upToDate := release == latest && ahead == 0
	behind := semverLess(release, latest)

	switch {
	case upToDate && !force:
		fmt.Fprintln(app.Out, style.success("already on the latest release"))
		return nil
	case !behind && !force:
		fmt.Fprintln(app.Out, style.dim("this build is not behind "+latest+"; pass --force to reinstall"))
		return nil
	}

	if checkOnly {
		fmt.Fprintln(app.Out, style.warning("a newer release is available: "+latest))
		return nil
	}

	install, err := runningInstall()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	self := install.Resolved

	if method := install.Method; method != methodStandalone {
		if !assumeYes {
			if !confirm(app, bufio.NewReader(app.In), "run "+method.command()) {
				return errors.New("upgrade cancelled")
			}
		}
		return runManagedUpgrade(ctxOf(), app, method)
	}

	if !assumeYes {
		if !confirm(app, bufio.NewReader(app.In), "replace "+self+" with dev "+latest) {
			return errors.New("upgrade cancelled")
		}
	}

	newBin, err := downloadReleaseBinary(ctx, app, latest, filepath.Dir(self))
	if err != nil {
		return err
	}
	defer os.Remove(newBin)

	if err := replaceBinary(newBin, self); err != nil {
		return fmt.Errorf("replace %s: %w", self, err)
	}

	fmt.Fprintln(app.Out, style.success(fmt.Sprintf("dev %s → %s", current, latest)))
	if goruntime.GOOS == "windows" {
		fmt.Fprintln(app.Out, style.dim("the previous "+filepath.Base(self)+" is cleaned up on the next run"))
	}
	return nil
}

func runManagedUpgrade(ctx context.Context, app *App, method installMethod) error {
	name, args := method.commandArgs()
	if name == "" {
		return fmt.Errorf("%s has no upgrade command", method.label())
	}

	fmt.Fprintf(app.Out, "\n%s installed this binary; running:\n\n    %s\n\n",
		method.label(), method.command())
	process := exec.CommandContext(ctx, name, args...)
	process.Stdin = app.In
	process.Stdout = app.Out
	process.Stderr = app.Err
	if err := process.Run(); err != nil {
		return fmt.Errorf("run %s: %w", method.command(), err)
	}
	return nil
}

// releaseAssetName is the archive published for this platform by release.yml.
func releaseAssetName(tag string) string {
	ext := "tar.gz"
	if goruntime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("dev-cli_%s_%s_%s.%s", tag, goruntime.GOOS, goruntime.GOARCH, ext)
}

func downloadReleaseBinary(ctx context.Context, app *App, tag, destDir string) (string, error) {
	asset := releaseAssetName(tag)
	base := releaseDownloadBase + "/" + tag + "/"

	fmt.Fprintf(app.Out, "downloading %s ...\n", asset)
	archive, err := httpGetBytes(ctx, base+asset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := httpGetBytes(ctx, base+"SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("download SHA256SUMS: %w", err)
	}
	if err := verifyChecksum(archive, sums, asset); err != nil {
		return "", err
	}

	binName := "dev"
	if goruntime.GOOS == "windows" {
		binName = "dev.exe"
	}
	payload, err := extractBinary(archive, asset, binName)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(destDir, "dev-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("stage new binary next to the target: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20))
}

func verifyChecksum(archive, sums []byte, asset string) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s — refusing to install", asset)
	}
	return nil
}

func extractBinary(archive []byte, asset, binName string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		return extractFromZip(archive, binName)
	}
	return extractFromTarGz(archive, binName)
}

func extractFromTarGz(archive []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
	return nil, fmt.Errorf("archive did not contain %s", binName)
}

func extractFromZip(archive []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 256<<20))
	}
	return nil, fmt.Errorf("archive did not contain %s", binName)
}
