package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxToolOutputBytes is the default threshold beyond which tool output
	// is pruned and spilled to disk to protect context token budget.
	DefaultMaxToolOutputBytes = 8192

	// DefaultHeadLines is the number of leading lines retained when pruning multi-line output.
	DefaultHeadLines = 25

	// DefaultTailLines is the number of trailing lines retained when pruning multi-line output.
	DefaultTailLines = 25

	// MaxSpillFilesPerSession bounds how many spill files one session may keep. Without
	// a cap every oversized tool output leaves a file that is never rotated.
	MaxSpillFilesPerSession = 200

	// SpillFileMaxAge is the retention window for spill files; entries older than this
	// are removed on the next spill write.
	SpillFileMaxAge = 24 * time.Hour
)

// PruneSpillDir enforces the retention policy for a session's spill directory: it drops
// entries older than SpillFileMaxAge and, if the directory is still larger than
// maxFiles, removes the oldest remaining files. A maxFiles of 0 or a maxAge of 0 only
// applies whichever limit is nonzero.
func PruneSpillDir(absDir string, maxFiles int, maxAge time.Duration) error {
	if absDir == "" {
		return nil
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}

	type entry struct {
		name    string
		age     time.Duration
		removed bool
	}

	items := make([]entry, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, entry{name: e.Name(), age: now.Sub(info.ModTime())})
	}

	for i := range items {
		if maxAge > 0 && items[i].age > maxAge {
			if err := os.Remove(filepath.Join(absDir, items[i].name)); err == nil {
				items[i].removed = true
			}
		}
	}

	if maxFiles <= 0 {
		return nil
	}

	kept := 0
	for _, it := range items {
		if !it.removed {
			kept++
		}
	}
	if kept <= maxFiles {
		return nil
	}

	// Evict the oldest survivors first so the freshest context hints stay resolvable.
	var survivors []entry
	for _, it := range items {
		if !it.removed {
			survivors = append(survivors, it)
		}
	}
	sort.Slice(survivors, func(i, j int) bool { return survivors[i].age > survivors[j].age })
	for _, it := range survivors[:kept-maxFiles] {
		_ = os.Remove(filepath.Join(absDir, it.name))
	}
	return nil
}

var spillFileMu sync.Mutex

// SanitizeFileID ensures an identifier is safe for filenames.
func SanitizeFileID(id string) string {
	if id == "" {
		return fmt.Sprintf("call_%d", time.Now().UnixNano())
	}
	var sb strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	res := sb.String()
	if len(res) > 40 {
		res = res[:40]
	}
	return res
}

// SpillAndPruneOutput inspects tool output and, if it exceeds maxBytes, writes the full
// unpruned content to a local spill file under .loop/spill/<sessionID>/ and returns a compact
// representation with leading and trailing context plus retrieval guidance.
// If maxBytes <= 0, DefaultMaxToolOutputBytes is used.
func SpillAndPruneOutput(workspaceRoot, sessionID, toolCallID, toolName, output string, maxBytes int) (string, string, bool) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxToolOutputBytes
	}

	if len(output) <= maxBytes {
		return output, "", false
	}

	if sessionID == "" {
		sessionID = "session"
	}
	safeSessionID := SanitizeFileID(sessionID)
	safeCallID := SanitizeFileID(toolCallID)

	var relativeSpillPath string
	var absoluteSpillPath string
	var spillErr error

	if workspaceRoot != "" {
		spillRelDir := filepath.Join(".loop", "spill", safeSessionID)
		spillAbsDir := filepath.Join(workspaceRoot, spillRelDir)

		spillFileMu.Lock()
		spillErr = os.MkdirAll(spillAbsDir, 0755)
		// Apply retention before writing so a long session cannot grow the spill
		// directory without bound. A prune failure must not be reported as a write
		// failure, so it is not folded into spillErr.
		if spillErr == nil {
			PruneSpillDir(spillAbsDir, MaxSpillFilesPerSession, SpillFileMaxAge)
		}
		spillFileMu.Unlock()

		fileName := fmt.Sprintf("%s_%s.log", SanitizeFileID(toolName), safeCallID)
		relativeSpillPath = filepath.Join(spillRelDir, fileName)
		absoluteSpillPath = filepath.Join(spillAbsDir, fileName)

		// Write full unpruned output to spill file. A failed write must be surfaced:
		// the pruned output would otherwise point at a file that does not exist.
		spillFileMu.Lock()
		if spillErr == nil {
			spillErr = os.WriteFile(absoluteSpillPath, []byte(output), 0644)
		}
		spillFileMu.Unlock()
	}

	// The fallback must include the call id: without it the hint points at a path that
	// was never written (the real filename is <tool>_<call>.log).
	locatorHint := relativeSpillPath
	if locatorHint == "" {
		locatorHint = ".loop/spill/" + safeSessionID + "/" + SanitizeFileID(toolName) + "_" + safeCallID + ".log"
	}
	if spillErr != nil {
		locatorHint = fmt.Sprintf("%s (spill write failed: %v)", locatorHint, spillErr)
		return output, locatorHint, false
	}

	lines := strings.Split(output, "\n")
	headCount := DefaultHeadLines
	tailCount := DefaultTailLines

	if len(lines) > (headCount + tailCount + 5) {
		head := strings.Join(lines[:headCount], "\n")
		tail := strings.Join(lines[len(lines)-tailCount:], "\n")
		omittedLines := len(lines) - headCount - tailCount
		omittedBytes := len(output) - len(head) - len(tail)
		if omittedBytes < 0 {
			omittedBytes = len(output) / 2
		}

		pruned := fmt.Sprintf("%s\n\n[... %d lines (%d bytes) omitted to protect context window ...]\n[Full raw output preserved at: %s]\n[Hint: Call 'read' with offset/limit, or 'grep' on the spill file to inspect omitted sections]\n\n%s",
			head, omittedLines, omittedBytes, locatorHint, tail)
		return pruned, locatorHint, true
	}

	// Single very long line or few long lines.
	// Head/tail budgets must never exceed maxBytes, otherwise an output that is over
	// maxBytes but shorter than the default budgets would be returned in full, breaking
	// the documented contract that oversized output is always pruned.
	headBytes := 3072
	tailBytes := 1024
	if headBytes+tailBytes > maxBytes {
		headBytes = maxBytes / 2
		tailBytes = maxBytes / 4
	}
	if headBytes < 1 {
		headBytes = 1
	}
	if tailBytes < 1 {
		tailBytes = 1
	}
	if len(output) > headBytes+tailBytes {
		head := output[:headBytes]
		tail := output[len(output)-tailBytes:]
		omittedBytes := len(output) - headBytes - tailBytes

		pruned := fmt.Sprintf("%s\n\n[... %d bytes omitted to protect context window ...]\n[Full raw output preserved at: %s]\n[Hint: Call 'read' with offset/limit, or 'grep' on the spill file to inspect omitted sections]\n\n%s",
			head, omittedBytes, locatorHint, tail)
		return pruned, locatorHint, true
	}

	return output, locatorHint, false
}
