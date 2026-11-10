package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

const maxAttachmentFiles = 32
const maxAttachmentBytes = 20 << 20

func isProtectedLoopPath(resolved string) bool {
	var loopDirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		homeResolved := home
		if r, evalErr := filepath.EvalSymlinks(home); evalErr == nil {
			homeResolved = r
		}
		loopDirs = append(loopDirs, filepath.Join(homeResolved, ".loop"))
	}
	if envDir := os.Getenv("LOOP_AGENT_DIR"); envDir != "" {
		if absEnv, err := filepath.Abs(envDir); err == nil {
			if r, evalErr := filepath.EvalSymlinks(absEnv); evalErr == nil {
				absEnv = r
			}
			loopDirs = append(loopDirs, absEnv)
		}
	}
	for _, loopDir := range loopDirs {
		if resolved == filepath.Join(loopDir, "LOOP.md") {
			continue
		}
		rel, err := filepath.Rel(loopDir, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	base := filepath.Base(resolved)
	parent := filepath.Base(filepath.Dir(resolved))
	if parent == ".loop" {
		switch base {
		case "auth.json", "config.json", "mcp.json", "providers.json", "packages.json":
			return true
		}
		if strings.HasSuffix(base, ".lock") {
			return true
		}
	}
	return false
}

func safeAttachmentPath(rawPath, workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		canonicalWorkspace = filepath.Clean(absWorkspace)
	}
	candidate := rawPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(canonicalWorkspace, candidate)
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absCandidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrNotExist
	}
	resolved, err := filepath.EvalSymlinks(absCandidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	rel, err := filepath.Rel(canonicalWorkspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace root (%s)", canonicalWorkspace)
	}
	if isProtectedLoopPath(resolved) {
		return "", fmt.Errorf("access to protected runtime configuration is denied")
	}
	return resolved, nil
}

func Attachments(input, workspace string) (string, []message.ContentBlock, error) {
	return AttachmentsContext(context.Background(), input, workspace, nil)
}

// AttachmentsContext shares one byte budget across unique files. The optional
// validator applies the active session's path policy without coupling resources
// to the agent implementation.
func AttachmentsContext(ctx context.Context, input, workspace string, validate func(string) (string, error)) (string, []message.ContentBlock, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	var text strings.Builder
	var blocks []message.ContentBlock
	seen := make(map[string]bool)
	remaining := int64(maxAttachmentBytes)
	for _, word := range domaintool.ParseCommandArgs(input) {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if !strings.HasPrefix(word, "@") || len(word) == 1 {
			continue
		}
		rawPath := word[1:]
		resolvedPath, err := safeAttachmentPath(rawPath, workspace)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", nil, fmt.Errorf("attachment %s: %w", rawPath, err)
		}
		if validate != nil {
			checked, err := validate(resolvedPath)
			if err != nil {
				return "", nil, fmt.Errorf("attachment %s: %w", rawPath, err)
			}
			if checked != resolvedPath {
				return "", nil, fmt.Errorf("attachment %s: target changed during validation", rawPath)
			}
		}
		if seen[resolvedPath] {
			continue
		}
		if len(seen) >= maxAttachmentFiles {
			return "", nil, fmt.Errorf("attachments exceed %d unique files; attach fewer files", maxAttachmentFiles)
		}
		seen[resolvedPath] = true
		data, err := storage.ReadFileContext(ctx, resolvedPath, remaining)
		if err != nil {
			return "", nil, fmt.Errorf("attachment %s (shared limit: 20 MiB): %w", rawPath, err)
		}
		remaining -= int64(len(data))
		block, err := message.AttachmentFromBytes(data)
		if err != nil {
			return "", nil, fmt.Errorf("attachment %s: %w", rawPath, err)
		}
		if block.Type == "text" {
			fmt.Fprintf(&text, "\n\n<file path=%q>\n%s\n</file>", resolvedPath, block.Text)
		} else {
			blocks = append(blocks, block)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	return input + text.String(), blocks, nil
}
