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
)

type attachmentSourceKey struct{}
type attachmentSource struct{ prompt, source string }
type preExpandedPromptKey struct{}

// WithAttachmentSource separates explicit input syntax from clipboard text.
// Its scope is one prompt, not subsequent steering or followups.
func WithAttachmentSource(ctx context.Context, prompt, source string) context.Context {
	return context.WithValue(ctx, attachmentSourceKey{}, attachmentSource{prompt, source})
}

func AttachmentSource(ctx context.Context, prompt string) (string, bool) {
	input, ok := ctx.Value(attachmentSourceKey{}).(attachmentSource)
	return input.source, ok && input.prompt == prompt
}

// WithPreExpandedPrompt marks a prompt whose leading template command has already
// been expanded once by the interactive caller, preventing secondary injection.
func WithPreExpandedPrompt(ctx context.Context, prompt string) context.Context {
	return context.WithValue(ctx, preExpandedPromptKey{}, prompt)
}

func IsPreExpandedPrompt(ctx context.Context, prompt string) bool {
	val, ok := ctx.Value(preExpandedPromptKey{}).(string)
	return ok && val == prompt
}

func PromptDirectories(agentDir, workspace string, trusted bool, additional []string) []string {
	directories := []string{filepath.Join(agentDir, "prompts")}
	if trusted {
		directories = append(directories, filepath.Join(workspace, ".loop", "prompts"))
	}
	return append(directories, additional...)
}

func ExpandPrompt(input string, directories []string) (string, error) {
	if !strings.HasPrefix(input, "/") {
		return input, nil
	}
	parts := strings.SplitN(input[1:], " ", 2)
	arguments := ""
	if len(parts) > 1 {
		arguments = parts[1]
	}
	var selected *domaintool.PromptTemplate
	for _, dir := range directories {
		for _, template := range domaintool.LoadPromptTemplates(dir) {
			if template.Name == parts[0] {
				copy := template
				selected = &copy
			}
		}
	}
	if selected == nil {
		return input, nil
	}
	return domaintool.SubstituteArgs(selected.Content, domaintool.ParseCommandArgs(arguments)), nil
}

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
	var text strings.Builder
	var blocks []message.ContentBlock
	for _, word := range domaintool.ParseCommandArgs(input) {
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
		block, err := message.ReadAttachment(resolvedPath)
		if err != nil {
			return "", nil, fmt.Errorf("attachment %s: %w", rawPath, err)
		}
		if block.Type == "text" {
			fmt.Fprintf(&text, "\n\n<file path=%q>\n%s\n</file>", resolvedPath, block.Text)
		} else {
			blocks = append(blocks, block)
		}
	}
	return input + text.String(), blocks, nil
}

func AgentDirectory(configPath string) string {
	if configPath != "" {
		return filepath.Dir(configPath)
	}
	if dir := os.Getenv("LOOP_AGENT_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".loop")
}
