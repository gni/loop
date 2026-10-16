package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/tool"
)

func PromptTemplateCommandDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".loop", "prompts")
}

func ExpandPromptTemplate(cmdName string, argsString string) (string, bool) {
	dir := PromptTemplateCommandDir()
	if dir == "" || !strings.HasPrefix(cmdName, "/") {
		return "", false
	}
	name := cmdName[1:]
	for _, t := range tool.LoadPromptTemplates(dir) {
		if t.Name == name {
			args := tool.ParseCommandArgs(argsString)
			return strings.TrimSpace(tool.SubstituteArgs(t.Content, args)), true
		}
	}
	return "", false
}

func HandlePromptTemplate(a *agent.Agent, cmdName string, argsString string, messages *[]db.Message, w io.Writer) bool {
	prompt, ok := ExpandPromptTemplate(cmdName, argsString)
	if !ok {
		return false
	}
	if messages != nil {
		*messages = append(*messages, db.Message{Role: "user", Content: prompt})
	}
	_ = a
	fmt.Fprintf(w, "template %s: %s\n", cmdName[1:], prompt)
	return true
}