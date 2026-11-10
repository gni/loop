package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	"loop/pkg/storage"
)

const (
	DefaultMaxToolOutputBytes = limits.DefaultRetainedOutputBytes
	DefaultHeadLines          = 25
	DefaultTailLines          = 25
	MaxSpillFilesPerSession   = 200
	SpillFileMaxAge           = 24 * time.Hour
)

// PruneSpillDir removes only Loop-owned logs. Unrecognized files and links
// remain untouched, including legacy spills referenced by older sessions.
func PruneSpillDir(path string, maxFiles int, maxAge time.Duration) error {
	return storage.PruneSpillDirectory(path, maxFiles, maxAge)
}

// SanitizeFileID keeps short safe IDs and disambiguates altered or long IDs.
func SanitizeFileID(id string) string {
	if id == "" {
		return fmt.Sprintf("call_%d", time.Now().UnixNano())
	}
	if len(id) <= 40 && strings.IndexFunc(id, func(r rune) bool { return !fileIDRune(r) }) < 0 {
		return id
	}
	var prefix strings.Builder
	for _, r := range id {
		if !fileIDRune(r) {
			r = '_'
		}
		prefix.WriteRune(r)
		if prefix.Len() == 23 {
			break
		}
	}
	sum := sha256.Sum256([]byte(id))
	return prefix.String() + "-" + hex.EncodeToString(sum[:8])
}

func fileIDRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}

func SpillAndPruneOutput(workspaceRoot, sessionID, toolCallID, toolName, output string, maxBytes int) (string, string, bool) {
	return spillAndPruneOutput(context.Background(), workspaceRoot, sessionID, toolCallID, toolName, output, maxBytes, limits.Current())
}

// SpillToolOutput uses the execution context's limits and cancellation.
func SpillToolOutput(ctx AgentContext, sessionID, toolCallID, toolName, output string, maxBytes int) (string, string, bool) {
	return spillAndPruneOutput(ctx.Context(), ctx.GetWorkspaceRoot(), sessionID, toolCallID, toolName, output, maxBytes, limits.For(ctx))
}

func spillAndPruneOutput(ctx context.Context, workspace, sessionID, callID, name, output string, budget int, bounds limits.Limits) (string, string, bool) {
	if budget <= 0 {
		budget = DefaultMaxToolOutputBytes
	}
	budget = min(budget, bounds.MaxOutputBytes, limits.AbsoluteMaxToolOutputBytes)
	text := strings.ToValidUTF8(output, "\uFFFD")
	lineCount := strings.Count(output, "\n") + 1
	// Native pages count their data rows before appending continuation notes.
	// Do not re-truncate an already bounded page because of those notes.
	tooManyLines := lineCount > bounds.MaxOutputLines
	switch NormalizeName(name) {
	case "read", "grep", "find_files", "list_directory", "search_symbols":
		tooManyLines = false
	}
	if len(text) <= budget && !tooManyLines {
		return text, "", false
	}
	if sessionID == "" {
		sessionID = "session"
	}
	path, locator := "", ""
	if workspace != "" && len(output) <= storage.MaxSpillBytes && ctx.Err() == nil {
		candidate := storage.NewSpillPath(SanitizeFileID(sessionID), SanitizeFileID(name)+"-"+SanitizeFileID(callID))
		hint := strings.TrimSpace(file.SpillFullOutputNotice(candidate))
		if !strings.Contains(hint, candidate) || len(hint) > budget {
			hint = "[Read " + candidate + "]"
		}
		// Do not write a file whose locator cannot be included in the result.
		if len(hint) <= budget {
			unlock, err := file.LockPathContext(ctx, filepath.Join(workspace, filepath.Dir(candidate)))
			if err == nil {
				err = storage.WriteSpill(ctx, workspace, candidate, output, MaxSpillFilesPerSession, SpillFileMaxAge)
				unlock()
			}
			if err == nil {
				path, locator = candidate, hint
			}
		}
	}
	return outputPreview(text, locator, budget, lineCount, bounds), path, true
}

func outputPreview(output, locator string, budget, lineCount int, bounds limits.Limits) string {
	notice := "[Output truncated; full output unavailable.]"
	if locator != "" {
		notice = locator
	}
	if len(notice) > budget {
		if budget >= len("[Truncated]") {
			return "[Truncated]"
		}
		return strings.Repeat(".", min(3, max(0, budget)))
	}
	lineBudget := min(DefaultHeadLines+DefaultTailLines, bounds.MaxOutputLines)
	if lineCount > lineBudget {
		head, tail := outputWindows(output, (lineBudget+1)/2, lineBudget/2, bounds.GrepMaxLineLength)
		for len(head)+len(tail) > 0 {
			detail := fmt.Sprintf("[Showing %d first and %d last lines of %d]", len(head), len(tail), lineCount)
			if locator != "" && len(head) > 0 {
				detail = fmt.Sprintf("[Showing %d first and %d last lines of %d; next offset=%d]", len(head), len(tail), lineCount, len(head)+1)
			}
			parts := make([]string, 0, 4)
			if len(head) > 0 {
				parts = append(parts, strings.Join(head, "\n"))
			}
			parts = append(parts, detail, notice)
			if len(tail) > 0 {
				parts = append(parts, strings.Join(tail, "\n"))
			}
			preview := strings.Join(parts, "\n")
			if len(preview) <= budget {
				return preview
			}
			if len(head) >= len(tail) {
				head = head[:len(head)-1]
			} else {
				tail = tail[1:]
			}
		}
		return notice
	}

	return file.PreviewWithNotice(output, notice, budget)
}

func outputWindows(output string, headCount, tailCount, lineLimit int) ([]string, []string) {
	head := make([]string, 0, headCount)
	remaining := output
	for range headCount {
		line, rest, _ := strings.Cut(remaining, "\n")
		head = append(head, file.TruncateLine(line, lineLimit))
		remaining = rest
	}
	tail := make([]string, tailCount)
	remaining = output
	for i := len(tail) - 1; i >= 0; i-- {
		end := strings.LastIndexByte(remaining, '\n')
		tail[i] = file.TruncateLine(remaining[end+1:], lineLimit)
		remaining = remaining[:end]
	}
	return head, tail
}
