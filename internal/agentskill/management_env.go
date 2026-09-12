package agentskill

import (
	"os"
	"path/filepath"
	"strings"
)

// Native installers may use env/node or an npm shim. The selected checkout
// must not supply that interpreter through PATH or Windows' implicit cwd search.
func managementEnvironment(root string, scope Scope) []string {
	if scope == ScopeGlobal {
		root = ""
	}
	if root != "" {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
	}
	paths := []string{}
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(path) || pathUsesNodeModulesBin(path) {
			continue
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || pathUsesNodeModulesBin(real) || root != "" && lexicalPathInside(root, real) {
			continue
		}
		paths = append(paths, path)
	}
	return []string{"PATH=" + strings.Join(paths, string(os.PathListSeparator)), "NoDefaultCurrentDirectoryInExePath=1"}
}
