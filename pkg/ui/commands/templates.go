package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/tool"
)

// PromptTemplateCommandDir returns ~/.loop/prompts.
func PromptTemplateCommandDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".loop", "prompts")
}

// handlePromptTemplate executes a markdown template under ~/.loop/prompts/ as a
// slash command, substituting $1..$9/$@/$ARGUMENTS/${N:-default}/${@:N:L}.
// Returns handled=false when no template matches.
// HandlePromptTemplate is the dispatch hook for template slash commands; it is
// invoked from HandleSlashCommand when no built-in command matches.
func HandlePromptTemplate(a *agent.Agent, cmdName string, argsString string, messages *[]db.Message, w io.Writer) bool {
	dir := PromptTemplateCommandDir()
	if dir == "" {
		return false
	}
	name := cmdName[1:]
	var tpl *tool.PromptTemplate
	for _, t := range tool.LoadPromptTemplates(dir) {
		if t.Name == name {
			tpl = &t
			break
		}
	}
	if tpl == nil {
		return false
	}

	args := tool.ParseCommandArgs(argsString)
	prompt := tool.SubstituteArgs(tpl.Content, args)

	var msgs []db.Message
	if messages != nil {
		msgs = *messages
	}
	msgs = append(msgs, db.Message{Role: "user", Content: prompt})
	if messages != nil {
		*messages = msgs
	}

	_ = a
	fmt.Fprintf(w, "template %s: %s\n", name, prompt)
	return true
}
