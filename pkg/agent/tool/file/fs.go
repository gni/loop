package file

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"loop/pkg/domain/ignore"
	domaintool "loop/pkg/domain/tool"
)

type snapshotGuard interface {
	HasSnapshot(absPath string) bool
	DiscardSnapshot(absPath string)
}

func prepareSnapshotRollback(ctx domaintool.AgentContext, safePath string) func() {
	sg, ok := ctx.(snapshotGuard)
	if !ok || sg.HasSnapshot(safePath) {
		return func() {}
	}
	return func() {
		sg.DiscardSnapshot(safePath)
	}
}

func AtomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, fmt.Sprintf(".tmp-%s-*", filepath.Base(filename)))
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", filename, err)
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
	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return fmt.Errorf("atomically replace %s: %w", filename, err)
	}
	tmpName = ""
	return syncParentDirectory(dir)
}

func CommitFileMutation(ctx domaintool.AgentContext, safePath string, data []byte, perm os.FileMode) error {
	if ctx == nil || ctx.Context() == nil {
		return fmt.Errorf("file mutation requires an active context")
	}
	if err := ctx.Context().Err(); err != nil {
		return fmt.Errorf("file mutation canceled: %w", err)
	}
	// Recheck containment immediately before committing. The final path must
	// remain within the original workspace after parent creation.
	verified, err := ctx.SafePath(safePath)
	if err != nil {
		return fmt.Errorf("file mutation path no longer safe: %w", err)
	}
	if verified != safePath {
		return fmt.Errorf("file mutation target changed during execution")
	}
	if info, err := os.Lstat(safePath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite non-regular file %s", safePath)
		}
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target %s: %w", safePath, err)
	}
	useAtomic := true
	if cp, ok := ctx.(interface{ IsAtomicWritesEnabled() bool }); ok {
		useAtomic = cp.IsAtomicWritesEnabled()
	}
	if useAtomic {
		err = AtomicWriteFile(safePath, data, perm)
	} else {
		err = os.WriteFile(safePath, data, perm)
	}
	if err != nil {
		return err
	}
	if fo, ok := ctx.(domaintool.FileObserver); ok {
		fo.RecordMutation(safePath, data)
	}
	ctx.ReloadSkills()
	return nil
}

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

func SanitizeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), " ")
}

var omissionRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?://|#|/\*|<!--|--|;)\s*\.{2,}\s*(?:rest of|unchanged|same as|original|existing|remaining|previous|other)\s*(?:code|methods?|functions?|class(?:es)?|files?|implementations?|imports?|logic)?`),
	regexp.MustCompile(`(?i)(?:rest of|unchanged|same as|original|existing|remaining|previous)\s+(?:code|methods?|functions?|class(?:es)?|files?|implementations?|imports?|logic)\s*\.{2,}`),
	regexp.MustCompile(`(?i)\[\s*(?:rest of|remaining|existing|previous|unchanged)\s+(?:code|implementation|file)\s*(?:unchanged|remains|same)?\s*\]`),
	regexp.MustCompile(`(?i)^\s*(?://|#|/\*|<!--|--|;)\s*\.{2,}\s*(?:\*/|-->)?\s*$`),
	regexp.MustCompile(`^\s*\(\s*\.{2,}\s*\)\s*$`),
	regexp.MustCompile(`(?i)(?://|#|/\*|<!--)\s*todo\s*[\:\-\s]*\.*(?:\s*(?:implement|add|write)\s+(?:the\s+)?(rest|remaining|code|methods?|functions?))`),
}

func DetectOmissionPlaceholders(text string) []string {
	var matches []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
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

func LockPath(path string) func() {
	fileLocksMu.Lock()
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	if resolved, errRes := filepath.EvalSymlinks(absPath); errRes == nil && resolved != "" {
		absPath = resolved
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

func SplitBOM(data []byte) (bool, string) {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return true, string(data[3:])
	}
	return false, string(data)
}

func DetectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if lf == -1 || crlf == -1 {
		return "\n"
	}
	if crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func HasIgnoredComponent(path string) bool {
	return ignore.HasComponent(path, "", false)
}
