package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

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

func Attachments(input, workspace string) (string, []message.ContentBlock, error) {
	var text strings.Builder
	var blocks []message.ContentBlock
	for _, word := range domaintool.ParseCommandArgs(input) {
		if !strings.HasPrefix(word, "@") || len(word) == 1 {
			continue
		}
		path := word[1:]
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		block, err := message.ReadAttachment(path)
		if err != nil {
			return "", nil, fmt.Errorf("attachment %s: %w", path, err)
		}
		if block.Type == "text" {
			fmt.Fprintf(&text, "\n\n<file path=%q>\n%s\n</file>", path, block.Text)
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
