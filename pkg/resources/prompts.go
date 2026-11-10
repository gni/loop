package resources

import (
	"context"
	"os"
	"path/filepath"
	"strings"

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
