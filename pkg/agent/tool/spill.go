package tool

import (
	"fmt"
	"os"
	"path/filepath"
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
)

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

	if workspaceRoot != "" {
		spillRelDir := filepath.Join(".loop", "spill", safeSessionID)
		spillAbsDir := filepath.Join(workspaceRoot, spillRelDir)

		spillFileMu.Lock()
		_ = os.MkdirAll(spillAbsDir, 0755)
		spillFileMu.Unlock()

		fileName := fmt.Sprintf("%s_%s.log", SanitizeFileID(toolName), safeCallID)
		relativeSpillPath = filepath.Join(spillRelDir, fileName)
		absoluteSpillPath = filepath.Join(spillAbsDir, fileName)

		// Write full unpruned output to spill file
		spillFileMu.Lock()
		_ = os.WriteFile(absoluteSpillPath, []byte(output), 0644)
		spillFileMu.Unlock()
	}

	locatorHint := relativeSpillPath
	if locatorHint == "" {
		locatorHint = ".loop/spill/" + safeSessionID + "/" + SanitizeFileID(toolName) + ".log"
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

	// Single very long line or few long lines
	headBytes := 3072
	tailBytes := 1024
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
