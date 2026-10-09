package file

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// AtomicWriteFile writes data to a temporary file in the destination's directory,
// flushes it to disk via Sync, and atomically renames it to filename.
// This prevents corrupted or empty files during crashes, timeouts, or interruptions.
func AtomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, fmt.Sprintf(".tmp-%s-*", filepath.Base(filename)))
	if err != nil {
		return os.WriteFile(filename, data, perm)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, perm)

	if err := os.Rename(tmpName, filename); err != nil {
		return os.WriteFile(filename, data, perm)
	}
	tmpName = ""
	return nil
}

func atomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	return AtomicWriteFile(filename, data, perm)
}

// IsBinary inspects initial bytes to detect if a file is binary.
func IsBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func isBinary(data []byte) bool {
	return IsBinary(data)
}

// SanitizeUTF8 replaces invalid UTF-8 byte sequences with spaces.
func SanitizeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), " ")
}

var omissionRegexes = []*regexp.Regexp{
	// Matches lines containing "rest of code", "rest of method(s)", "unchanged code", etc.
	regexp.MustCompile(`(?i)(?:rest of|unchanged|same as|original|existing)\s+(?:code|methods?|functions?|class(?:es)?|files?|implementations?)\s*\.{3,}`),
	// Matches lines with just comments and dots: e.g. // ... or # ... or /* ... */
	regexp.MustCompile(`(?i)^\s*(?://|#|/\*)\s*\.{3,}\s*(?:\*/)?\s*$`),
	// Matches brackets with dots: (...)
	regexp.MustCompile(`^\s*\(\s*\.{3,}\s*\)\s*$`),
	// Matches TODO comments that suggest omission: e.g. // TODO: implement the rest or // TODO ...
	regexp.MustCompile(`(?i)(?://|#|/\*)\s*todo\s*[\:\-\s]*\.*(?:\s*(?:implement|add|write)\s+(?:the\s+)?(rest|remaining|code|methods?))?\s*\.{3,}`),
}

// DetectOmissionPlaceholders searches for code omission comments like '// ... rest of code'.
func DetectOmissionPlaceholders(text string) []string {
	var matches []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, rx := range omissionRegexes {
			if rx.MatchString(trimmed) {
				matches = append(matches, line)
				break
			}
		}
	}
	return matches
}

type refMutex struct {
	mu   sync.Mutex
	refs int
}

var (
	fileLocks   = make(map[string]*refMutex)
	fileLocksMu sync.Mutex
)

// LockPath locks a file path across concurrent operations.
func LockPath(path string) func() {
	fileLocksMu.Lock()
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	entry, exists := fileLocks[absPath]
	if !exists {
		entry = &refMutex{}
		fileLocks[absPath] = entry
	}
	entry.refs++
	fileLocksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		fileLocksMu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(fileLocks, absPath)
		}
		fileLocksMu.Unlock()
	}
}

func lockPath(path string) func() {
	return LockPath(path)
}

// SplitBOM detects and strips a UTF-8 BOM prefix, returning whether it existed.
func SplitBOM(data []byte) (bool, string) {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return true, string(data[3:])
	}
	return false, string(data)
}

func splitBOM(data []byte) (bool, string) {
	return SplitBOM(data)
}

// DetectLineEnding returns the dominant line ending (\r\n or \n).
func DetectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if lf == -1 {
		return "\n"
	}
	if crlf == -1 {
		return "\n"
	}
	if crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func detectLineEnding(content string) string {
	return DetectLineEnding(content)
}

// HasIgnoredComponent returns whether path has components like node_modules or .git.
func HasIgnoredComponent(path string) bool {
	path = filepath.Clean(path)
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		low := strings.ToLower(part)
		if low == "node_modules" || low == "venv" || low == ".venv" || low == ".git" || low == "__pycache__" {
			return true
		}
	}
	return false
}

func hasIgnoredComponent(path string) bool {
	return HasIgnoredComponent(path)
}
