// Package ignore is the single source of truth for the ignored-directory name
// set shared by the search tools and the file tools. It holds one list of build,
// cache, VCS, and dependency directory names; callers choose a policy on top of
// that list (search treats any hidden directory as ignored, the read tool only
// blocks dependency/artifact directories so workspace-local hidden folders such
// as .loop/spill stay readable).
package ignore

import (
	"path/filepath"
	"strings"
)

// dirNames is the canonical ignored-directory name set. Keep this as the only
// list; policy decisions belong in the functions below, not in extra lists.
var dirNames = map[string]bool{
	".git":          true,
	".hg":           true,
	".svn":          true,
	"node_modules":  true,
	"venv":          true,
	".venv":         true,
	"env":           true,
	".env":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	".tox":          true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	"dist":          true,
	"build":         true,
	"target":        true,
	"out":           true,
	"bin":           true,
	"obj":           true,
	".idea":         true,
	".vscode":       true,
	".next":         true,
	".nuxt":         true,
	".cache":        true,
	"vendor":        true,
}

// IsDirName reports whether a single directory name belongs to the canonical set.
// When includeHidden is true, any hidden directory (a name starting with '.') is
// treated as ignored as well; that is the search policy.
func IsDirName(name string, includeHidden bool) bool {
	low := strings.ToLower(name)
	if dirNames[low] {
		return true
	}
	return includeHidden && strings.HasPrefix(low, ".") && low != "." && low != ".."
}

// HasComponent reports whether any directory component of path is ignored. When
// root is non-empty, segments belonging to the workspace root itself are never
// treated as ignored, so a workspace living inside a hidden directory (e.g. a
// temp dir under .gotmp) is still inspected.
func HasComponent(path, root string, includeHidden bool) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	if root != "" {
		cleanRoot := filepath.ToSlash(filepath.Clean(root))
		if clean == cleanRoot {
			return false
		}
		if strings.HasPrefix(clean, cleanRoot+"/") {
			clean = strings.TrimPrefix(clean, cleanRoot+"/")
		}
	}
	for _, part := range strings.Split(clean, "/") {
		if IsDirName(part, includeHidden) {
			return true
		}
	}
	return false
}
