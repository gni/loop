package commands

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui/render"
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
			if s.Name != name {
				continue
			}
			found = true
			msg := db.Message{
				Role:    "system",
				Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", s.Name, s.Content),
			}
			for _, previous := range *messages {
				if previous.Role == "system" && previous.Content == msg.Content {
					fmt.Fprintf(w, "skill '%s' is already loaded in this conversation.\n", name)
					return
				}
			}
			if currentSessionID != nil && *currentSessionID != "" {
				if err := db.SaveMessage(*currentSessionID, msg); err != nil {
					fmt.Fprintf(w, "could not load skill '%s': %v\n", name, err)
					break
				}
			}
			*messages = append(*messages, msg)
			fmt.Fprintf(w, "loaded skill '%s' into the conversation context.\n", name)
			break
		}

		if !found {
			fmt.Fprintf(w, "error: skill '%s' not found.\n", name)
		}
	} else if len(parts) > 1 && parts[1] == "reload" {
		if _, err := a.ReloadSkillsChecked(); err != nil {
			fmt.Fprintf(w, "could not reload skills: %v\n", err)
			return
		}
		fmt.Fprintln(w, "successfully reloaded all skills from disk.")
	} else {
		render.RenderSkills(w, a.ActiveSkills, *theme)
		if len(a.ActiveSkills) == 0 && a.Config != nil {
			dirs := agent.SkillSearchDirs(a.Config.SkillsDir, a.GetWorkspaceRoot())
			if len(dirs) > 0 {
				fmt.Fprintf(w, "searched: %s\n", strings.Join(dirs, ", "))
				fmt.Fprintln(w, "create a skill as <skill-dir>/<name>/SKILL.md or <skill-dir>/<name>.md, then run /skills reload.")
			}
		}
	}
}
