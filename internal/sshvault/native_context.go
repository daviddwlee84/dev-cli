package sshvault

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

type nativeContextProof struct {
	environment [32]byte
	cwd         nativePathRef
	profile     nativePathRef
	dataACL     nativeACLObservation
	portable    string
	portableSet bool
	tool        nativeTool
	view        NativeProfile
}

func (*nativeContextProof) String() string {
	return "sshvault native context (private comparison material omitted)"
}
func (p *nativeContextProof) GoString() string { return p.String() }

type nativeExecution struct {
	proof       nativeContextProof
	environment []string
	runner      sshcredential.FrozenCommandRunner
}

func (*nativeExecution) String() string     { return "sshvault frozen execution context (redacted)" }
func (e *nativeExecution) GoString() string { return e.String() }
func (e *nativeExecution) release() {
	for i := range e.environment {
		e.environment[i] = ""
	}
	e.environment = nil
}

func captureEnvironment() (map[string]string, [32]byte, error) {
	entries := os.Environ()
	defer func() {
		for i := range entries {
			entries[i] = ""
		}
	}()
	values := make(map[string]string, len(entries))
	total := 0
	for _, entry := range entries {
		total += len(entry)
		name, value, valid := strings.Cut(entry, "=")
		if !valid || name == "" || strings.ContainsRune(entry, 0) || total > 1<<20 {
			return nil, [32]byte{}, ErrNativeContext
		}
		values[name] = value
	}
	if session := values["BW_SESSION"]; session == "" || len(session) > 4096 {
		return nil, [32]byte{}, ErrLocked
	}
	// Loading extra interpreter/native code is outside the bound entrypoint
	// contract. Merely hashing these variables would not attest that code.
	for name, value := range values {
		if value != "" && (name == "NODE_OPTIONS" || name == "NODE_PATH" || name == "LD_PRELOAD" || name == "LD_LIBRARY_PATH" || strings.HasPrefix(name, "DYLD_")) {
			return nil, [32]byte{}, ErrNativeContext
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{'='})
		_, _ = hash.Write([]byte(values[key]))
		_, _ = hash.Write([]byte{0})
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return values, digest, nil
}

func nativeProfilePath(goos string, environment map[string]string, executable string) (string, string, bool, error) {
	portable := filepath.Join(filepath.Dir(executable), "bw-data")
	if _, err := os.Lstat(portable); err == nil {
		return portable, portable, true, nil
	} else if !os.IsNotExist(err) {
		return "", "", false, ErrNativeContext
	}
	var selected string
	if explicit := environment["BITWARDENCLI_APPDATA_DIR"]; explicit != "" {
		selected = explicit
	} else {
		switch goos {
		case "darwin":
			if !filepath.IsAbs(environment["HOME"]) {
				return "", "", false, ErrNativeContext
			}
			selected = filepath.Join(environment["HOME"], "Library", "Application Support", "Bitwarden CLI")
		case "linux":
			if xdg := environment["XDG_CONFIG_HOME"]; xdg != "" {
				if !filepath.IsAbs(xdg) {
					return "", "", false, ErrNativeContext
				}
				selected = filepath.Join(xdg, "Bitwarden CLI")
			} else {
				if !filepath.IsAbs(environment["HOME"]) {
					return "", "", false, ErrNativeContext
				}
				selected = filepath.Join(environment["HOME"], ".config", "Bitwarden CLI")
			}
		default:
			return "", "", false, ErrNativeContext
		}
	}
	if !filepath.IsAbs(selected) || filepath.Clean(selected) != selected {
		return "", "", false, ErrNativeContext
	}
	return selected, portable, false, nil
}

func checkNativeProfileData(profile string) (nativeACLObservation, error) {
	// Native initialization would create missing data.json. Require an existing
	// private regular file, but never read its vault contents. Native internal
	// configuration/content changes are the explicitly delegated residual scope.
	path := filepath.Join(profile, "data.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !nativeOwned(info, true) {
		return nativeACLObservation{}, ErrNativeContext
	}
	acl, err := captureNativeACL(path, info)
	if err != nil || !acl.safe {
		return nativeACLObservation{}, ErrNativeContext
	}
	return acl, nil
}

func (s *Service) captureNativeContext(ctx context.Context) (*nativeExecution, error) {
	if s == nil || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, ErrNativeContext
	}
	runner, ok := s.runner.(sshcredential.FrozenCommandRunner)
	if !ok {
		return nil, ErrNativeContext
	}
	environment, digest, err := captureEnvironment()
	if err != nil {
		return nil, err
	}
	defer clear(environment)
	directory, err := os.Getwd()
	if err != nil {
		return nil, ErrNativeContext
	}
	cwd, err := captureNativePath(directory, true, false)
	if err != nil {
		return nil, err
	}
	tool, err := captureNativeTool(ctx, environment["PATH"], cwd.canonical)
	if err != nil {
		return nil, err
	}
	selected, portable, portableSet, err := nativeProfilePath(runtime.GOOS, environment, tool.executable())
	if err != nil {
		return nil, err
	}
	profile, err := captureNativePath(selected, true, true)
	if err != nil {
		return nil, ErrNativeContext
	}
	dataACL, err := checkNativeProfileData(profile.canonical)
	if err != nil {
		return nil, ErrNativeContext
	}
	view := NativeProfile{Path: profile.canonical, RequestedPath: selected, Entrypoint: tool.entry.path.canonical, CWD: cwd.canonical}
	if tool.runtime != nil {
		view.Runtime = tool.runtime.path.canonical
	}
	if tool.packageRoot != nil {
		view.PackageRoot = tool.packageRoot.canonical
	}
	proof := nativeContextProof{environment: digest, cwd: cwd, profile: profile, dataACL: dataACL, portable: portable, portableSet: portableSet, tool: tool, view: view}
	// Pin the selected canonical directory and noninteraction in the child only.
	// Portable bw-data is checked separately because native precedence is higher.
	environment["BITWARDENCLI_APPDATA_DIR"] = profile.canonical
	environment["BW_NOINTERACTION"] = "true"
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	frozen := make([]string, 0, len(keys))
	for _, key := range keys {
		frozen = append(frozen, key+"="+environment[key])
	}
	return &nativeExecution{proof: proof, environment: frozen, runner: runner}, nil
}

func sameNativeContext(a, b nativeContextProof) bool {
	return a.environment == b.environment && a.view == b.view && a.dataACL == b.dataACL && a.portable == b.portable && a.portableSet == b.portableSet && sameNativePath(a.cwd, b.cwd) && sameNativePath(a.profile, b.profile) && sameNativeTool(a.tool, b.tool)
}

func (e *nativeExecution) revalidate(ctx context.Context) error {
	cwd, err := captureNativePath(e.proof.cwd.requested, true, false)
	if err != nil || !sameNativePath(e.proof.cwd, cwd) {
		return ErrStale
	}
	profile, err := captureNativePath(e.proof.profile.requested, true, true)
	if err != nil || !sameNativePath(e.proof.profile, profile) {
		return ErrStale
	}
	dataACL, err := checkNativeProfileData(profile.canonical)
	if err != nil || dataACL != e.proof.dataACL {
		return ErrStale
	}
	_, portableErr := os.Lstat(e.proof.portable)
	if e.proof.portableSet && portableErr != nil || !e.proof.portableSet && !os.IsNotExist(portableErr) {
		return ErrStale
	}
	for _, ref := range []*nativeFileRef{&e.proof.tool.entry, e.proof.tool.runtime, e.proof.tool.manifest} {
		if ref == nil {
			continue
		}
		limit := int64(512 << 20)
		if ref == e.proof.tool.manifest {
			limit = 64 << 10
		}
		current, _, err := captureNativeFile(ctx, ref.path.requested, limit)
		if err != nil || !sameNativePath(ref.path, current.path) || ref.digest != current.digest {
			return ErrStale
		}
	}
	if e.proof.tool.bin != nil {
		current, err := captureNativePath(e.proof.tool.bin.requested, false, false)
		if err != nil || !sameNativePath(*e.proof.tool.bin, current) {
			return ErrStale
		}
	}
	return nil
}

func (e *nativeExecution) run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	if err := e.revalidate(ctx); err != nil {
		return nil, err
	}
	commandArgs := make([]string, 0, len(args)+2)
	if e.proof.tool.runtime != nil {
		commandArgs = append(commandArgs, e.proof.tool.entry.path.canonical)
	}
	commandArgs = append(commandArgs, "--nointeraction")
	commandArgs = append(commandArgs, args...)
	body, err := e.runner.RunWithEnvironment(ctx, e.proof.tool.executable(), commandArgs, input, e.environment, e.proof.cwd.canonical)
	if err != nil || len(body) > maxOutput {
		sshcredential.Wipe(body)
		return nil, ErrUnavailable
	}
	return body, nil
}

func advisoryServer(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || !validText(parsed.Host, 256) {
		return ""
	}
	// A base/display URL is not endpoint evidence. Strip userinfo, paths,
	// query strings and fragments so advisory labels cannot carry credentials.
	return parsed.Scheme + "://" + parsed.Host
}

func (e *nativeExecution) observe(ctx context.Context, request Request) (Destination, string, error) {
	body, err := e.run(ctx, []string{"--version"}, nil)
	defer sshcredential.Wipe(body)
	if err != nil {
		return Destination{}, "", err
	}
	if len(body) > 64 || strings.TrimSpace(string(body)) != BitwardenSchemaVersion {
		return Destination{}, "", ErrUnsupportedVersion
	}
	if e.proof.tool.version != "" && e.proof.tool.version != BitwardenSchemaVersion {
		return Destination{}, "", ErrUnsupportedVersion
	}
	statusBody, err := e.run(ctx, []string{"status"}, nil)
	defer sshcredential.Wipe(statusBody)
	if err != nil {
		return Destination{}, "", err
	}
	var status struct {
		Status    string `json:"status"`
		UserID    string `json:"userId"`
		Email     string `json:"userEmail"`
		ServerURL string `json:"serverUrl"`
	}
	if json.Unmarshal(statusBody, &status) != nil {
		return Destination{}, "", ErrUnavailable
	}
	if status.Status == "locked" || status.Status == "unauthenticated" {
		return Destination{}, "", ErrLocked
	}
	if status.Status != "unlocked" || !bwIDPattern.MatchString(status.UserID) {
		return Destination{}, "", ErrUnavailable
	}
	if request.AccountID != "" && request.AccountID != status.UserID {
		return Destination{}, "", ErrStale
	}
	label := ""
	if validText(status.Email, 256) {
		if address, err := mail.ParseAddress(status.Email); err == nil && address.Address == status.Email {
			label = address.Address
		}
	}
	return Destination{Provider: Bitwarden, AccountID: status.UserID, UserID: status.UserID, ServerURL: advisoryServer(status.ServerURL), VaultID: PersonalVault, VaultName: "Personal vault", AccountLabel: label, ProfilePath: e.proof.profile.canonical}, BitwardenSchemaVersion, nil
}
