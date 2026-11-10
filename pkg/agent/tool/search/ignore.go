package search

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/ignore"
	"loop/pkg/storage"
)

// RelSlashPath resolves targetPath relative to workspaceRoot and normalizes separators to forward slashes.
func RelSlashPath(workspaceRoot, targetPath string) string {
	rel, err := filepath.Rel(workspaceRoot, targetPath)
	if err != nil {
		rel = targetPath
	}
	return filepath.ToSlash(rel)
}

// ShouldSkipWalkEntry evaluates directory and file ignore policies during filepath.WalkDir.
func ShouldSkipWalkEntry(path string, d fs.DirEntry, workspaceRoot string, gitIgnorePatterns []string) (bool, error) {
	name := d.Name()
	if d.IsDir() {
		if filepath.Clean(path) == filepath.Clean(workspaceRoot) {
			return true, nil
		}
		if IsIgnoredDirName(name) || HasIgnoredComponentIn(path, workspaceRoot) || IsIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
			return true, filepath.SkipDir
		}
		return true, nil
	}
	if IsIgnoredFileName(name) || IsIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
		return true, nil
	}
	return false, nil
}

// validateSearchPath also checks explicitly supplied roots and files. A caller
// must not bypass pruning by starting a search inside a dependency directory.
func validateSearchPath(path, root string) error {
	if ignore.HasComponent(path, root, false) {
		return fmt.Errorf("cannot search dependency or generated directory: %s; inspect project source instead", RelSlashPath(root, path))
	}
	return nil
}

// IsIgnoredDirName checks if directory name belongs to common build, cache, or VCS
// directories. The name set lives in pkg/domain/ignore; the search policy also treats
// any hidden directory as ignored.
func IsIgnoredDirName(name string) bool {
	return ignore.IsDirName(name, true)
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
	data, err := storage.ReadFile(gitIgnorePath, 1<<20)
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

// expandBracePatterns expands a single level of {a,b,c} alternatives in a glob pattern.
func expandBracePatterns(pattern string) []string {
	openIdx := strings.IndexByte(pattern, '{')
	if openIdx < 0 {
		return []string{pattern}
	}
	closeIdx := strings.IndexByte(pattern[openIdx:], '}')
	if closeIdx < 0 {
		return []string{pattern}
	}
	closeIdx += openIdx
	prefix := pattern[:openIdx]
	suffix := pattern[closeIdx+1:]
	parts := strings.Split(pattern[openIdx+1:closeIdx], ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, prefix+strings.TrimSpace(part)+suffix)
	}
	return out
}

// Track reachable path positions iteratively. Repeated ** segments must not
// cause exponential backtracking on an otherwise ordinary nonmatching path.
func matchPathSegments(pattern, path []string) bool {
	current := make([]bool, len(path)+1)
	next := make([]bool, len(path)+1)
	current[0] = true
	for _, segment := range pattern {
		clear(next)
		if segment == "**" {
			next[0] = current[0]
			for index := 1; index <= len(path); index++ {
				next[index] = current[index] || next[index-1]
			}
		} else {
			for index, name := range path {
				if current[index] {
					matched, err := filepath.Match(segment, name)
					next[index+1] = err == nil && matched
				}
			}
		}
		current, next = next, current
	}
	return current[len(path)]
}

// MatchGlob matches a case-insensitive glob pattern (supporting **, path segments, and {a,b} braces)
// against a workspace-relative slash path and file base name.
func MatchGlob(pattern, relPath, baseName string) bool {
	normPat := strings.ToLower(strings.TrimSpace(filepath.ToSlash(pattern)))
	normPat = strings.TrimPrefix(normPat, "./")
	if normPat == "" {
		return false
	}

	lowerRel := strings.ToLower(strings.TrimPrefix(filepath.ToSlash(relPath), "./"))
	lowerBase := strings.ToLower(baseName)
	if lowerBase == "" && lowerRel != "" {
		lowerBase = filepath.Base(lowerRel)
	}
	relSegs := strings.Split(lowerRel, "/")

	for _, candidate := range expandBracePatterns(normPat) {
		if candidate == "" {
			continue
		}
		if !strings.Contains(candidate, "/") {
			if matched, _ := filepath.Match(candidate, lowerBase); matched {
				return true
			}
			if matched, _ := filepath.Match(candidate, lowerRel); matched {
				return true
			}
			continue
		}

		patSegs := strings.Split(strings.TrimPrefix(candidate, "/"), "/")
		if matchPathSegments(patSegs, relSegs) {
			return true
		}
		if !strings.HasPrefix(candidate, "**/") && !strings.HasPrefix(candidate, "/") {
			withAnyPrefix := append([]string{"**"}, patSegs...)
			if matchPathSegments(withAnyPrefix, relSegs) {
				return true
			}
		}
	}
	return false
}

// IsIgnoredByGit checks if path matches any gitignore patterns, respecting negation rules.
func IsIgnoredByGit(path string, workspaceRoot string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return false
	}
	base := filepath.Base(rel)

	ignored := false
	for _, rawPat := range patterns {
		pat := strings.TrimSpace(rawPat)
		if pat == "" || strings.HasPrefix(pat, "#") {
			continue
		}
		negated := strings.HasPrefix(pat, "!")
		if negated {
			pat = strings.TrimSpace(pat[1:])
			if pat == "" {
				continue
			}
		}

		rooted := strings.HasPrefix(pat, "/")
		pat = strings.TrimPrefix(pat, "/")
		isDirOnly := strings.HasSuffix(pat, "/")
		cleanPat := strings.TrimSuffix(pat, "/")
		if cleanPat == "" {
			continue
		}

		matched := false
		if isDirOnly {
			if rooted {
				matched = rel == cleanPat || strings.HasPrefix(rel, cleanPat+"/")
			} else {
				matched = rel == cleanPat ||
					strings.HasPrefix(rel, cleanPat+"/") ||
					strings.Contains(rel, "/"+cleanPat+"/") ||
					strings.HasSuffix(rel, "/"+cleanPat)
			}
		} else if rooted {
			matched = matchPathSegments(strings.Split(strings.ToLower(cleanPat), "/"), strings.Split(strings.ToLower(rel), "/"))
		} else {
			matched = MatchGlob(cleanPat, rel, base)
		}

		if matched {
			ignored = !negated
		}
	}
	return ignored
}

// IsBinary returns true if data contains null bytes.
func IsBinary(data []byte) bool {
	return filetool.IsBinary(data)
}

// SanitizeUTF8 replaces invalid UTF-8 sequences with spaces or replacement characters.
func SanitizeUTF8(data []byte) string {
	return filetool.SanitizeUTF8(data)
}
