package search

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/ignore"
)

// IsIgnoredDirName checks if directory name belongs to common build, cache, or VCS
// directories. The name set lives in pkg/domain/ignore; the search policy also treats
// any hidden directory as ignored.
func IsIgnoredDirName(name string) bool {
	return ignore.IsDirName(name, true)
}

// HasIgnoredComponent returns true if any directory in the given path is ignored.
func HasIgnoredComponent(path string) bool {
	return HasIgnoredComponentIn(path, "")
}

// HasIgnoredComponentIn checks ignored directory components within path, relative to
// root. Segments that belong to the workspace root itself are never treated as ignored,
// so a workspace living inside a hidden directory (e.g. a temp dir under .gotmp) is still
// searched.
func HasIgnoredComponentIn(path, root string) bool {
	return ignore.HasComponent(path, root, true)
}

// IsIgnoredFileName returns true if the file extension or basename indicates a binary, image, lock, or cache file.
func IsIgnoredFileName(name string) bool {
	low := strings.ToLower(name)
	ext := filepath.Ext(low)
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".iso", ".zip", ".tar", ".gz",
		".7z", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".pdf", ".lock",
		".pyc", ".pyo", ".wasm", ".sqlite", ".db", ".woff", ".woff2", ".ttf":
		return true
	}
	return low == ".ds_store" || low == "thumbs.db"
}

// LoadGitIgnore reads and parses .gitignore from workspace root.
func LoadGitIgnore(workspaceRoot string) []string {
	var patterns []string
	gitIgnorePath := filepath.Join(workspaceRoot, ".gitignore")
	data, err := os.ReadFile(gitIgnorePath)
	if err != nil {
		return nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// IsIgnoredByGit checks if path matches any gitignore patterns.
func IsIgnoredByGit(path string, workspaceRoot string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)

	for _, pat := range patterns {
		pat = strings.TrimPrefix(pat, "/")
		isDirOnly := strings.HasSuffix(pat, "/")
		cleanPat := strings.TrimSuffix(pat, "/")

		if isDirOnly {
			if strings.Contains(rel, cleanPat+"/") || rel == cleanPat {
				return true
			}
		}

		if matched, _ := filepath.Match(cleanPat, base); matched {
			return true
		}
		if matched, _ := filepath.Match(cleanPat, rel); matched {
			return true
		}
	}
	return false
}

// IsBinary returns true if data contains null bytes.
func IsBinary(data []byte) bool {
	return filetool.IsBinary(data)
}

// SanitizeUTF8 replaces invalid UTF-8 sequences with spaces or replacement characters.
func SanitizeUTF8(data []byte) string {
	return filetool.SanitizeUTF8(data)
}
