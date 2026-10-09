package commands

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func handleSkillsCommand(a *agent.Agent, parts []string, messages *[]db.Message, currentSessionID *string, theme *style.UITheme, w io.Writer) {
	if len(parts) > 1 && parts[1] == "load" {
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /skills load <skill-name>")
			return
		}
		name := parts[2]
		found := false
		for _, s := range a.ActiveSkills {
			if s.Name == name {
				*messages = append(*messages, db.Message{
					Role:    "system",
					Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", s.Name, s.Content),
				})
				_ = db.SaveMessage(*currentSessionID, (*messages)[len(*messages)-1])
				fmt.Fprintf(w, "loaded skill '%s' into the conversation context.\n", name)
				found = true
				break
			}
		}

		if !found {
			fmt.Fprintf(w, "error: skill '%s' not found.\n", name)
		}
	} else if len(parts) > 1 && parts[1] == "reload" {
		_ = a.ReloadSkills()
		fmt.Fprintln(w, "successfully reloaded all skills from disk.")
	} else {
		agent.RenderSkills(w, a.ActiveSkills, *theme)
		if len(a.ActiveSkills) == 0 && a.Config != nil {
			dirs := agent.SkillSearchDirs(a.Config.SkillsDir, a.GetWorkspaceRoot())
			if len(dirs) > 0 {
				fmt.Fprintf(w, "searched: %s\n", strings.Join(dirs, ", "))
				fmt.Fprintln(w, "create a skill as <skill-dir>/<name>/SKILL.md or <skill-dir>/<name>.md, then run /skills reload.")
			}
		}
	}
}
