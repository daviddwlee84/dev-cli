package agentinterop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// Git administrative identity is additional scope authority, not transferable
// payload. Replacing a checkout's .git/common-dir cannot reuse an old plan.
func rootIdentity(path string, info fs.FileInfo) (string, error) {
	id, err := fileIdentity(info)
	if err != nil {
		return "", err
	}
	marker := filepath.Join(path, ".git")
	gitInfo, err := os.Lstat(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return id + ":no-git", nil
	}
	if err != nil {
		return "", errors.New("Git checkout identity is unreadable")
	}
	gitDir := marker
	var markerData []byte
	if gitInfo.Mode().IsRegular() {
		markerData, err = readGitIdentityFile(marker)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(string(markerData))
		if !strings.HasPrefix(text, "gitdir: ") {
			return "", errors.New("invalid Git checkout marker")
		}
		gitDir = strings.TrimPrefix(text, "gitdir: ")
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(path, gitDir)
		}
	} else if !gitInfo.IsDir() {
		return "", errors.New("Git checkout marker cannot be a symlink or special file")
	}
	gitInfo, err = os.Stat(gitDir)
	if err != nil || !gitInfo.IsDir() {
		return "", errors.New("Git administrative directory is unavailable")
	}
	gitID, err := fileIdentity(gitInfo)
	if err != nil {
		return "", err
	}
	common := gitDir
	data, err := readGitIdentityFile(filepath.Join(gitDir, "commondir"))
	if err == nil {
		common = strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	commonInfo, err := os.Stat(common)
	if err != nil || !commonInfo.IsDir() {
		return "", errors.New("Git common directory is unavailable")
	}
	commonID, err := fileIdentity(commonInfo)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append(append(markerData, 0), data...))
	return id + ":" + gitID + ":" + commonID + ":" + hex.EncodeToString(hash[:]), nil
}

func readGitIdentityFile(path string) ([]byte, error) {
	file, info, err := safefile.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return nil, errors.New("Git identity read failed")
	}
	if len(data) > 8192 {
		return nil, errors.New("Git identity file exceeds limit")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !safefile.SameFileState(info, current) {
		return nil, ErrStale
	}
	return data, nil
}
