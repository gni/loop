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
	"loop/pkg/resources"
)

func PromptTemplateCommandDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".loop", "prompts")
}

func PromptTemplateDirs(a *agent.Agent) []string {
	if a == nil {
		if dir := PromptTemplateCommandDir(); dir != "" {
			return []string{dir}
		}
		return nil
	}
	includeProject := a.Config == nil || (!a.Config.DisableProjectPlugins && !a.Config.DisableLocalPlugins)
	var extra []string
	if a.Config != nil {
		for _, dir := range a.Config.PromptDirectories {
			if a.ProjectResourceAllowed(dir) {
				extra = append(extra, dir)
			}
		}
	}
	dirs := resources.PromptDirectories(resources.AgentDirectory(a.ConfigPath), a.GetWorkspaceRoot(), includeProject, extra)
	if len(dirs) == 0 {
		if dir := PromptTemplateCommandDir(); dir != "" {
			return []string{dir}
		}
	}
	return dirs
}

func ExpandPromptTemplate(a *agent.Agent, cmdName string, argsString string) (string, bool) {
	if !strings.HasPrefix(cmdName, "/") {
		return "", false
	}
	name := cmdName[1:]
	var selected *tool.PromptTemplate
	for _, dir := range PromptTemplateDirs(a) {
		for _, t := range tool.LoadPromptTemplates(dir) {
			if t.Name == name {
				cp := t
				selected = &cp
			}
		}
	}
	if selected != nil {
		args := tool.ParseCommandArgs(argsString)
		return strings.TrimSpace(tool.SubstituteArgs(selected.Content, args)), true
	}
	return "", false
}

func HandlePromptTemplate(a *agent.Agent, cmdName string, argsString string, messages *[]db.Message, w io.Writer) bool {
	prompt, ok := ExpandPromptTemplate(a, cmdName, argsString)
	if !ok {
		return false
	}
	if messages != nil {
		if a != nil {
			a.MessagesMu.Lock()
			*messages = append(*messages, db.Message{Role: "user", Content: prompt})
			a.MessagesMu.Unlock()
		} else {
			*messages = append(*messages, db.Message{Role: "user", Content: prompt})
		}
	}
	fmt.Fprintf(w, "template %s: %s\n", cmdName[1:], prompt)
	return true
}
