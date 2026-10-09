package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleAgentCommand(a *agent.Agent, line string, parts []string, mam *swarm.MultiAgentManager, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader) {
	if len(parts) < 2 {
		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		_ = interactive.RunInteractiveAgentManager(mam, *theme, input, output)
		ui.InitStatusBar(os.Stderr)

		if kiReader != nil {
			ui.RedrawScreen(w, a, kiReader, kiReader.RL)
		} else {
			ui.RedrawScreen(w, a, nil, nil)
		}
		return
	}
	sub := parts[1]
	switch sub {
	case "list":
		agentsList := mam.ListAgents()
		if len(agentsList) == 0 {
			fmt.Fprintln(w, "no active multi-agents spawned. (default chat is routed to base agent)")
			return
		}
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("active agents:"))
		activeName := mam.ActiveAgentName()

		for _, name := range agentsList {
			marker := " "
			if name == activeName {
				marker = style.NewStyle().Foreground(theme.Success).Render("➔")
			}
			parentName := mam.GetParentName(name)
			parentStr := ""
			if parentName != "" {
				parentStr = fmt.Sprintf(" (subagent of %s)", parentName)
			}

			fmt.Fprintf(w, "  %s %-15s : %s\n", marker, style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), parentStr)
		}
		if activeName == "" {
			fmt.Fprintf(w, "  %s %-15s : (currently chatting with default base agent)\n", style.NewStyle().Foreground(theme.Success).Render("➔"), "base")
		}
	case "join":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /agent join <name> (use 'base' to return to base agent)")
			return
		}
		target := parts[2]
		if target == "base" || target == "main" {
			mam.JoinAgent("base")
			fmt.Fprintln(w, "switched back to default base agent.")
			return
		}
		joined := mam.JoinAgent(target)
		if joined {
			fmt.Fprintf(w, "switched chat focus to agent '%s'.\n", target)
		} else {
			fmt.Fprintf(w, "error: agent '%s' not found.\n", target)
		}
	case "spawn":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /agent spawn <name> <prompt> [parent_name] [skill_name]")
			return
		}
		name := parts[2]

		// Prompt can be multi-word, so we extract it carefully
		promptStart := fieldStartIndex(line, 3)
		if promptStart == -1 {
			fmt.Fprintln(w, "error: failed to parse prompt")
			return
		}
		prompt := line[promptStart:]
		parentName := ""
		skillName := ""

		// Loop to dynamically extract trailing arguments that match parent agents or active skills
		for {
			lastSpace := strings.LastIndex(prompt, " ")
			if lastSpace == -1 {
				break
			}
			lastWord := strings.TrimSpace(prompt[lastSpace+1:])

			// Check if it is a known skill name
			isSkill := false
			for _, s := range a.ActiveSkills {
				if s.Name == lastWord {
					isSkill = true
					break
				}
			}

			if isSkill && skillName == "" {
				skillName = lastWord
				prompt = strings.TrimSpace(prompt[:lastSpace])
				continue
			}

			// Check if it is an existing agent name
			if mam.HasAgent(lastWord) && parentName == "" {
				parentName = lastWord
				prompt = strings.TrimSpace(prompt[:lastSpace])
				continue
			}

			// If it matches neither, or both are already resolved, stop checking
			break
		}

		var skillNames []string
		if skillName != "" {
			skillNames = append(skillNames, skillName)
		}
		err := mam.SpawnAgent(name, prompt, parentName, skillNames)
		if err != nil {
			fmt.Fprintf(w, "error spawning agent: %v\n", err)
		} else {
			var info string
			if parentName != "" && skillName != "" {
				info = fmt.Sprintf("subagent '%s' under parent '%s' with dedicated skill '%s'", name, parentName, skillName)
			} else if parentName != "" {
				info = fmt.Sprintf("subagent '%s' under parent '%s'", name, parentName)
			} else if skillName != "" {
				info = fmt.Sprintf("independent agent '%s' with dedicated skill '%s'", name, skillName)
			} else {
				info = fmt.Sprintf("independent agent '%s'", name)
			}
			fmt.Fprintf(w, "successfully spawned %s.\n", info)
		}
	case "skill":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /agent skill [list <agent_name> | load <agent_name> <skill_name> | clear <agent_name>]")
			return
		}
		op := parts[2]
		agentName := parts[3]

		switch op {
		case "list":
			skills, err := mam.ListAgentSkills(agentName)
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
				return
			}
			if len(skills) == 0 {
				fmt.Fprintf(w, "agent '%s' has no loaded skills.\n", agentName)
				return
			}
			fmt.Fprintf(w, "skills loaded for agent '%s':\n", agentName)
			for _, s := range skills {
				fmt.Fprintf(w, "  - %s: %s\n", style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(s.Name), s.Description)
			}
		case "load":
			if len(parts) < 5 {
				fmt.Fprintln(w, "usage: /agent skill load <agent_name> <skill_name>")
				return
			}
			skillName := parts[4]
			err := mam.LoadAgentSkill(agentName, skillName)
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
			} else {
				fmt.Fprintf(w, "successfully loaded skill '%s' into agent '%s'.\n", skillName, agentName)
			}
		case "clear":
			err := mam.ClearAgentSkills(agentName)
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
			} else {
				fmt.Fprintf(w, "cleared all skills for agent '%s'.\n", agentName)
			}
		default:
			fmt.Fprintln(w, "unknown skill operation. usage: /agent skill [list <agent_name> | load <agent_name> <skill_name> | clear <agent_name>]")
		}
	case "remove":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /agent remove <name>")
			return
		}
		target := parts[2]
		err := mam.RemoveAgent(target)
		if err != nil {
			fmt.Fprintf(w, "error removing agent: %v\n", err)
		} else {
			fmt.Fprintf(w, "agent .%s. removed.\n", target)
		}
	default:
		fmt.Fprintln(w, "unknown agent subcommand. usage: /agent [list | join <name> | spawn <name> <prompt> [parent_name] [skill_name] | remove <name> | skill [list/load/clear] ...]")
	}
}

func handleReloadCommand(a *agent.Agent, w io.Writer) {
	err := a.ReloadPlugins()
	if err != nil {
		fmt.Fprintf(w, "error reloading plugins: %v\n", err)
	} else {
		fmt.Fprintln(w, "reload ok")
	}
}

func handlePluginsCommand(a *agent.Agent, theme *style.UITheme, w io.Writer) {
	executors := a.Registry.GetAllExecutors()
	var pluginNames []string
	for name := range executors {
		if strings.HasPrefix(name, "plugin__") {
			pluginNames = append(pluginNames, name)
		}
	}
	sort.Strings(pluginNames)
	if len(pluginNames) == 0 {
		fmt.Fprintln(w, "no custom plugins registered.")
		return
	}
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("⊞ custom plugin tools:"))
	for _, name := range pluginNames {
		exec := executors[name]
		fmt.Fprintf(w, "  - %-25s : %s\n",
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
			exec.Definition().Function.Description,
		)
	}
}

func handleExtensionsCommand(a *agent.Agent, theme *style.UITheme, w io.Writer) {
	var dirs []string
	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".loop", "extensions"))
	}
	dirs = append(dirs, filepath.Join(a.GetWorkspaceRoot(), "extensions"))

	type extInfo struct {
		name string
		path string
		loc  string
	}
	var exts []extInfo
	seen := make(map[string]bool)

	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		loc := "project"
		if strings.Contains(dir, ".loop") {
			loc = "global"
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// Check executable permission
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err == nil && info.Mode()&0111 != 0 {
				base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				cmdName := "/" + strings.ToLower(base)
				if !seen[cmdName] {
					seen[cmdName] = true
					exts = append(exts, extInfo{
						name: cmdName,
						path: path,
						loc:  loc,
					})
				}
			}
		}
	}

	sort.Slice(exts, func(i, j int) bool {
		return exts[i].name < exts[j].name
	})

	if len(exts) == 0 {
		fmt.Fprintln(w, "no custom slash command extensions found.")
		return
	}

	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("⌁ custom slash command extensions:"))
	for _, ext := range exts {
		fmt.Fprintf(w, "  - %-20s : %s (%s)\n",
			style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(ext.name),
			ext.path,
			ext.loc,
		)
	}
}

// fieldStartIndex returns the start index of the fieldIndex-th word (0-based) in s.
func fieldStartIndex(s string, fieldIndex int) int {
	inWord := false
	wordCount := 0
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inWord = false
		} else {
			if !inWord {
				if wordCount == fieldIndex {
					return i
				}
				wordCount++
				inWord = true
			}
		}
	}
	return -1
}
