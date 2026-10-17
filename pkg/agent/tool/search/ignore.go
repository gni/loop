package search

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/ignore"
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

func matchPathSegments(patSegs, pathSegs []string) bool {
	for len(patSegs) > 0 {
		if patSegs[0] == "**" {
			for len(patSegs) > 1 && patSegs[1] == "**" {
				patSegs = patSegs[1:]
			}
			if len(patSegs) == 1 {
				return true
			}
			for i := 0; i <= len(pathSegs); i++ {
				if matchPathSegments(patSegs[1:], pathSegs[i:]) {
					return true
				}
			}
			return false
		}
		if len(pathSegs) == 0 {
			return false
		}
		matched, err := filepath.Match(patSegs[0], pathSegs[0])
		if err != nil || !matched {
			return false
		}
		patSegs = patSegs[1:]
		pathSegs = pathSegs[1:]
	}
	return len(pathSegs) == 0
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