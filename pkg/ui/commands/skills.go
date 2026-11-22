package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	iskills "loop/pkg/ui/interactive/skills"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handleSkillsCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	printSkillsList := func() {
		activeSkills := a.GetActiveSkills()
		render.RenderSkills(w, activeSkills, *theme)
		if len(activeSkills) == 0 && a.Config != nil {
			dirs := a.ConfiguredSkillDirs()
			if len(dirs) > 0 {
				fmt.Fprintf(w, "searched: %s\n", strings.Join(dirs, ", "))
				fmt.Fprintln(w, "create a skill as <skill-dir>/<name>/SKILL.md or <skill-dir>/<name>.md, then run /skills reload.")
			}
		}
	}

	openSkillPicker := func() {
		rl := terminalFromReader(kiReader)
		input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		selected, err := iskills.RunSkillPickerContext(
			commandContext(a, kiReader),
			a.GetActiveSkills,
			a.ReloadSkillsChecked,
			func(active []tool.Skill) map[string]bool {
				return loadedSkillNames(a, messages, active)
			},
			*theme,
			input,
			output,
		)
		ui.InitStatusBar(os.Stderr)
		ui.RestoreAfterModal(w, a, kiReader, rl)

		if err != nil {
			if !errors.Is(err, interactive.ErrSelectionCancelled) {
				fmt.Fprintf(w, "cannot open skill picker: %v\n", err)
			}
			return
		}
		if selected == "" {
			return
		}
		if loadSkillIntoConversation(a, selected, messages, currentSessionID, w) {
			syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
		}
	}

	if len(parts) > 1 {
		switch strings.ToLower(parts[1]) {
		case "load":
			if len(parts) < 3 {
				if kiReader != nil || term.IsTerminal(int(os.Stdin.Fd())) {
					if len(a.GetActiveSkills()) == 0 {
						printSkillsList()
						return
					}
					openSkillPicker()
					return
				}
				fmt.Fprintln(w, "usage: /skills load <skill-name>")
				return
			}
			if loadSkillIntoConversation(a, parts[2], messages, currentSessionID, w) {
				syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
			}
			return
		case "reload":
			if _, err := a.ReloadSkillsChecked(); err != nil {
				fmt.Fprintf(w, "could not reload skills: %v\n", err)
				return
			}
			fmt.Fprintln(w, "successfully reloaded all skills from disk.")
			return
		case "list", "show":
			printSkillsList()
			return
		case "select", "pick", "menu":
			if kiReader != nil || term.IsTerminal(int(os.Stdin.Fd())) {
				openSkillPicker()
				return
			}
			printSkillsList()
			return
		default:
			if hasActiveSkill(a.GetActiveSkills(), parts[1]) {
				if loadSkillIntoConversation(a, parts[1], messages, currentSessionID, w) {
					syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
				}
				return
			}
			fmt.Fprintf(w, "error: skill or subcommand '%s' not found. usage: /skills [list | load [name] | reload]\n", parts[1])
			return
		}
	}

	activeSkills := a.GetActiveSkills()
	if len(activeSkills) == 0 {
		printSkillsList()
		return
	}
	if kiReader != nil || term.IsTerminal(int(os.Stdin.Fd())) {
		openSkillPicker()
		return
	}
	printSkillsList()
}

func hasActiveSkill(skills []tool.Skill, name string) bool {
	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

func loadedSkillNames(a *agent.Agent, messages *[]db.Message, active []tool.Skill) map[string]bool {
	loaded := make(map[string]bool)
	if a == nil || messages == nil || len(active) == 0 {
		return loaded
	}
	byName := make(map[string]string, len(active))
	for _, s := range active {
		byName[strings.ToLower(s.Name)] = tool.FormatSkillInstructions(s)
	}
	seen := make(map[string]bool, len(active))
	a.MessagesMu.RLock()
	for index := len(*messages) - 1; index >= 0; index-- {
		previous := (*messages)[index]
		if previous.SkillName == "" || previous.IsError {
			continue
		}
		key := strings.ToLower(previous.SkillName)
		if seen[key] {
			continue
		}
		seen[key] = true
		if expected, ok := byName[key]; ok && previous.Content == expected {
			loaded[key] = true
		}
	}
	a.MessagesMu.RUnlock()
	return loaded
}

func loadSkillIntoConversation(
	a *agent.Agent,
	name string,
	messages *[]db.Message,
	currentSessionID *string,
	w io.Writer,
) bool {
	for _, s := range a.GetActiveSkills() {
		if !strings.EqualFold(s.Name, name) {
			continue
		}
		msg := db.Message{
			Role:        "system",
			ContextKind: "skill",
			SkillName:   s.Name,
			Content:     tool.FormatSkillInstructions(s),
		}
		alreadyLoaded := false
		if messages != nil {
			a.MessagesMu.RLock()
			for index := len(*messages) - 1; index >= 0; index-- {
				previous := (*messages)[index]
				if strings.EqualFold(previous.SkillName, s.Name) && !previous.IsError {
					alreadyLoaded = previous.Content == msg.Content
					break
				}
			}
			a.MessagesMu.RUnlock()
		}
		if alreadyLoaded {
			fmt.Fprintf(w, "skill '%s' is already loaded in this conversation.\n", s.Name)
			return false
		}
		id := ""
		if currentSessionID != nil {
			id = *currentSessionID
		}
		if err := a.AppendInteractiveMessage(messages, id, msg); err != nil {
			fmt.Fprintf(w, "skill '%s' loaded in memory, but could not save: %v\n", s.Name, err)
			return true
		}
		fmt.Fprintf(w, "loaded skill '%s' into the conversation context.\n", s.Name)
		return true
	}

	fmt.Fprintf(w, "error: skill '%s' not found.\n", name)
	return false
}
