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
		if IsIgnoredDirName(name) || HasIgnoredComponentIn(path, workspaceRoot) || IsIgnoredByGitEntry(path, workspaceRoot, gitIgnorePatterns, true) {
			return true, filepath.SkipDir
		}
		return true, nil
	}
	if IsIgnoredFileName(name) || IsIgnoredByGitEntry(path, workspaceRoot, gitIgnorePatterns, false) {
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

// expandBracePatterns expands {a,b,c} brace groups iteratively, capped to avoid
// combinatorial blowup on crafted patterns.
func expandBracePatterns(pattern string) []string {
	const maxBraceExpansions = 64
	current := []string{pattern}
	for {
		expandedAny := false
		var next []string
		for _, item := range current {
			openIdx := strings.IndexByte(item, '{')
			if openIdx < 0 {
				next = append(next, item)
				continue
			}
			closeIdx := strings.IndexByte(item[openIdx:], '}')
			if closeIdx < 0 {
				next = append(next, item)
				continue
			}
			closeIdx += openIdx
			prefix := item[:openIdx]
			suffix := item[closeIdx+1:]
			parts := strings.Split(item[openIdx+1:closeIdx], ",")
			if len(next)+len(parts) > maxBraceExpansions {
				next = append(next, item)
				continue
			}
			expandedAny = true
			for _, part := range parts {
				next = append(next, prefix+strings.TrimSpace(part)+suffix)
			}
		}
		current = next
		if !expandedAny || len(current) >= maxBraceExpansions {
			break
		}
	}
	return current
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
	return IsIgnoredByGitEntry(path, workspaceRoot, patterns, false)
}

// IsIgnoredByGitEntry checks gitignore rules with knowledge of whether path is a directory,
// so trailing-slash directory-only rules match directories (and their descendants) without
// falsely ignoring regular files of the same name.
func IsIgnoredByGitEntry(path string, workspaceRoot string, patterns []string, isDir bool) bool {
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
	lowerRelSegs := strings.Split(strings.ToLower(rel), "/")
	maxDirDepth := len(lowerRelSegs) - 1
	if isDir {
		maxDirDepth = len(lowerRelSegs)
	}

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
			if maxDirDepth > 0 {
				lowerPat := strings.ToLower(cleanPat)
				if rooted {
					patSegs := strings.Split(lowerPat, "/")
					for depth := 1; depth <= maxDirDepth; depth++ {
						if matchPathSegments(patSegs, lowerRelSegs[:depth]) {
							matched = true
							break
						}
					}
				} else if !strings.Contains(lowerPat, "/") {
					for depth := 0; depth < maxDirDepth; depth++ {
						if ok, _ := filepath.Match(lowerPat, lowerRelSegs[depth]); ok {
							matched = true
							break
						}
					}
				} else {
					patSegs := strings.Split(lowerPat, "/")
					anyPrefix := append([]string{"**"}, patSegs...)
					for depth := 1; depth <= maxDirDepth; depth++ {
						prefixSegs := lowerRelSegs[:depth]
						if matchPathSegments(patSegs, prefixSegs) || matchPathSegments(anyPrefix, prefixSegs) {
							matched = true
							break
						}
					}
				}
			}
		} else if rooted {
			matched = matchPathSegments(strings.Split(strings.ToLower(cleanPat), "/"), lowerRelSegs)
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
